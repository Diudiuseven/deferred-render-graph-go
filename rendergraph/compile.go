package rendergraph

import "sort"

// Level 是一组可以并行执行的通道（按名称）。
type Level []string

// AllocSlot 描述资源在内存池中的位置。
type AllocSlot struct {
	Buffer int
	Offset int
	Size   int
}

// Compiled 是编译后的渲染图：拓扑分层、资源生命周期与分配计划。
type Compiled struct {
	Levels []Level
	// Lifetime 记录每个资源首次/末次使用的层号。
	FirstUse map[ResourceID]int
	LastUse  map[ResourceID]int
	// Allocation 记录每个资源分配到的内存块编号与字节偏移。
	Allocation map[ResourceID]AllocSlot
	// TotalBytes 为复用后总内存需求。
	TotalBytes int

	graph      *Graph
	preds      map[string][]string // 每个通道的前驱
	passConfig map[string]string   // 通道名 -> ConfigKey
	resDescs   map[ResourceID]ResourceDesc
	passIndex  map[string]int // 通道声明顺序
}

// Compile 解析依赖并生成调度计划；循环依赖返回 *CycleError。
func (g *Graph) Compile() (*Compiled, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	c := &Compiled{
		graph:      g,
		preds:      map[string][]string{},
		passConfig: map[string]string{},
		resDescs:   map[ResourceID]ResourceDesc{},
		passIndex:  map[string]int{},
		FirstUse:   map[ResourceID]int{},
		LastUse:    map[ResourceID]int{},
		Allocation: map[ResourceID]AllocSlot{},
	}
	for i, p := range g.Passes() {
		c.passIndex[p.Name] = i
		c.passConfig[p.Name] = p.ConfigKey
		c.preds[p.Name] = nil
	}
	for id, d := range g.resources {
		c.resDescs[id] = d
	}

	// 建边（渲染图标准语义）：
	//   RAW：读者依赖该资源的每一个写者（读者必须看到写者输出）
	//   WAW：多个写者按声明顺序排序，保证确定性
	// 只读共享不产生边，可并行；同一资源的读与写形成依赖即构成偏序，
	// 若相互依赖（如 p1 读 p3 写的资源，p3 又读 p1 写的资源）即为循环依赖。
	edges := map[string]map[string]bool{}
	writers := map[ResourceID][]string{}
	readers := map[ResourceID][]string{}
	for _, p := range g.Passes() {
		for _, id := range p.Reads {
			readers[id] = append(readers[id], p.Name)
		}
		for _, id := range p.Writes {
			writers[id] = append(writers[id], p.Name)
		}
	}
	addEdge := func(from, to string) {
		if from == to {
			return
		}
		if edges[from] == nil {
			edges[from] = map[string]bool{}
		}
		edges[from][to] = true
	}
	for id, ws := range writers {
		for _, r := range readers[id] {
			for _, w := range ws {
				addEdge(w, r) // RAW
			}
		}
		for i := 1; i < len(ws); i++ {
			addEdge(ws[i-1], ws[i]) // WAW，按声明顺序
		}
	}
	for from, tos := range edges {
		for to := range tos {
			c.preds[to] = append(c.preds[to], from)
		}
	}
	for name := range c.preds {
		sort.Strings(c.preds[name])
	}

	// Kahn 分层拓扑排序；层内按声明顺序保证确定性。
	indeg := map[string]int{}
	for name, ps := range c.preds {
		indeg[name] = len(ps)
	}
	remaining := map[string]bool{}
	for name := range c.preds {
		remaining[name] = true
	}
	byOrder := func(names []string) {
		sort.Slice(names, func(i, j int) bool { return c.passIndex[names[i]] < c.passIndex[names[j]] })
	}
	var frontier []string
	for name := range remaining {
		if indeg[name] == 0 {
			frontier = append(frontier, name)
		}
	}
	byOrder(frontier)
	for len(frontier) > 0 {
		lvl := append(Level(nil), frontier...)
		c.Levels = append(c.Levels, lvl)
		var next []string
		for _, name := range frontier {
			delete(remaining, name)
			for to := range edges[name] {
				indeg[to]--
				if indeg[to] == 0 {
					next = append(next, to)
				}
			}
		}
		byOrder(next)
		frontier = next
	}
	if len(remaining) > 0 {
		return nil, &CycleError{Path: findCycle(remaining, edges)}
	}

	// 生命周期：资源首次/末次使用所在层。
	levelOf := map[string]int{}
	for li, lvl := range c.Levels {
		for _, name := range lvl {
			levelOf[name] = li
		}
	}
	for _, p := range g.Passes() {
		li := levelOf[p.Name]
		for _, id := range append(append([]ResourceID{}, p.Reads...), p.Writes...) {
			if _, ok := c.FirstUse[id]; !ok || li < c.FirstUse[id] {
				c.FirstUse[id] = li
			}
			if li > c.LastUse[id] {
				c.LastUse[id] = li
			}
		}
	}

	// 分配计划：按 (firstUse, 名称) 排序贪心复用内存块。
	res := g.Resources()
	sort.Slice(res, func(i, j int) bool {
		fi, fj := c.FirstUse[res[i].Name], c.FirstUse[res[j].Name]
		if fi != fj {
			return fi < fj
		}
		return res[i].Name < res[j].Name
	})
	type buf struct {
		size     int
		freeFrom int // 该块从哪一层开始空闲
	}
	var bufs []buf
	for _, d := range res {
		need := d.Bytes()
		last := c.LastUse[d.Name]
		_ = last
		placed := false
		for bi := range bufs {
			if bufs[bi].freeFrom <= c.FirstUse[d.Name] && bufs[bi].size >= need {
				c.Allocation[d.Name] = AllocSlot{Buffer: bi, Offset: 0, Size: need}
				bufs[bi].freeFrom = c.LastUse[d.Name] + 1
				placed = true
				break
			}
		}
		if !placed {
			bufs = append(bufs, buf{size: need, freeFrom: c.LastUse[d.Name] + 1})
			c.Allocation[d.Name] = AllocSlot{Buffer: len(bufs) - 1, Offset: 0, Size: need}
		}
	}
	for _, b := range bufs {
		c.TotalBytes += b.size
	}
	return c, nil
}

// findCycle 在剩余节点中找出一个环，返回可定位的通道名路径。
func findCycle(remaining map[string]bool, edges map[string]map[string]bool) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		stack = append(stack, n)
		// 确定性：按名称排序遍历邻边
		var tos []string
		for to := range edges[n] {
			if remaining[to] {
				tos = append(tos, to)
			}
		}
		sort.Strings(tos)
		for _, to := range tos {
			if color[to] == gray {
				// 找到环：从 stack 中 to 的位置到栈顶
				idx := 0
				for i, s := range stack {
					if s == to {
						idx = i
						break
					}
				}
				cycle = append(append([]string{}, stack[idx:]...), to)
				return true
			}
			if color[to] == white && dfs(to) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	var names []string
	for n := range remaining {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white && dfs(n) {
			return cycle
		}
	}
	return names
}

// DirtyPasses 与上一版本对比，返回需要重新执行的通道名（新增、配置变化、
// 资源描述变化或依赖链上游变化），按声明顺序返回。
func (c *Compiled) DirtyPasses(prev *Compiled) []string {
	dirty := map[string]bool{}
	if prev == nil {
		for name := range c.passConfig {
			dirty[name] = true
		}
	} else {
		for name, key := range c.passConfig {
			prevKey, ok := prev.passConfig[name]
			if !ok || prevKey != key {
				dirty[name] = true
			}
		}
		// 资源描述变化 -> 所有访问该资源的通道变脏
		for id, d := range c.resDescs {
			if pd, ok := prev.resDescs[id]; !ok || pd != d {
				for _, p := range c.graph.Passes() {
					if accessesResource(p, id) {
						dirty[p.Name] = true
					}
				}
			}
		}
		// 沿依赖边传播：前驱脏则本通道脏
		for _, lvl := range c.Levels {
			for _, name := range lvl {
				if dirty[name] {
					continue
				}
				for _, pre := range c.preds[name] {
					if dirty[pre] {
						dirty[name] = true
						break
					}
				}
			}
		}
	}
	var out []string
	for _, p := range c.graph.Passes() {
		if dirty[p.Name] {
			out = append(out, p.Name)
		}
	}
	return out
}

func accessesResource(p *Pass, id ResourceID) bool {
	for _, r := range p.Reads {
		if r == id {
			return true
		}
	}
	for _, w := range p.Writes {
		if w == id {
			return true
		}
	}
	return false
}
