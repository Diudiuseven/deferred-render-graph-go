package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Graph 组织渲染通道及其对中间结果的读写依赖。
type Graph struct {
	passes    []*Pass
	resources map[string]ResourceDesc
}

// NewGraph 创建空渲染图。
func NewGraph() *Graph { return &Graph{resources: map[string]ResourceDesc{}} }

// DeclareResource 声明一个中间结果及其描述。
func (g *Graph) DeclareResource(name string, desc ResourceDesc) {
	g.resources[name] = desc
}

// ResourceDesc 返回资源的描述。
func (g *Graph) ResourceDesc(name string) (ResourceDesc, bool) {
	d, ok := g.resources[name]
	return d, ok
}

// AddPass 向图中加入通道；同名通道会被替换（用于管线变更）。
func (g *Graph) AddPass(p *Pass) {
	for i, q := range g.passes {
		if q.Name == p.Name {
			g.passes[i] = p
			return
		}
	}
	g.passes = append(g.passes, p)
}

// RemovePass 按名称移除通道。
func (g *Graph) RemovePass(name string) bool {
	for i, p := range g.passes {
		if p.Name == name {
			g.passes = append(g.passes[:i], g.passes[i+1:]...)
			return true
		}
	}
	return false
}

// Pass 按名称查找通道。
func (g *Graph) Pass(name string) *Pass {
	for _, p := range g.passes {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Lifetime 描述资源的生命周期区间（按调度层级计，闭区间）。
type Lifetime struct {
	First, Last int
}

// Compiled 是编译后的调度结果。
type Compiled struct {
	// Levels 为拓扑分层；同层通道互不依赖，可并行执行。
	Levels [][]*Pass
	// Order 为确定性的总执行顺序（层序 + 层内按名称排序）。
	Order []*Pass
	// Lifetimes 记录每个资源的生命周期区间。
	Lifetimes map[string]Lifetime
	// Resources 保存资源描述。
	Resources map[string]ResourceDesc
	// Deps 记录每个通道直接依赖的前驱通道名（按名称排序）。
	Deps map[string][]string
}

// Compile 解析依赖并生成调度计划；循环依赖返回可定位错误。
func (g *Graph) Compile() (*Compiled, error) {
	// 校验引用的资源均已声明。
	for _, p := range g.passes {
		for _, r := range append(append([]string{}, p.Reads...), p.Writes...) {
			if _, ok := g.resources[r]; !ok {
				return nil, fmt.Errorf("graph: pass %q references undeclared resource %q", p.Name, r)
			}
		}
	}

	n := len(g.passes)
	adj := make([][]int, n) // 边: 前驱 -> 后继
	indeg := make([]int, n)
	addEdge := func(from, to int) {
		if from == to {
			return
		}
		adj[from] = append(adj[from], to)
		indeg[to]++
	}

	// 依赖规则（对每个资源独立分析，按通道声明顺序）：
	//  1. WAW：多个写者按声明顺序排列。
	//  2. RAW：读者依赖它消费的那个写者——声明顺序上它之前最近的写者；
	//     若它之前没有写者，则它消费的是所有写者（经 WAW 链传递）的最终结果。
	//  3. WAR：写者 w 必须排在“消费上一个写者结果”的读者之后。
	type access struct {
		pass  int
		write bool
	}
	accesses := map[string][]access{}
	for i, p := range g.passes {
		for _, w := range p.Writes {
			accesses[w] = append(accesses[w], access{i, true})
		}
		for _, r := range p.Reads {
			// 同一通道对同一资源既读又写时，写记录已存在，跳过。
			if !contains(p.Writes, r) {
				accesses[r] = append(accesses[r], access{i, false})
			}
		}
	}
	for _, accs := range accesses {
		// 按声明顺序遍历，维护最近写者。
		lastWriter := -1
		for k, a := range accs {
			if a.write {
				if lastWriter >= 0 {
					addEdge(lastWriter, a.pass) // WAW
				}
				// WAR：上一写者与当前写者之间的读者必须先于当前写者。
				if lastWriter >= 0 {
					for j := 0; j < k; j++ {
						if !accs[j].write && accs[j].pass > lastWriter {
							addEdge(accs[j].pass, a.pass)
						}
					}
				}
				lastWriter = a.pass
			} else {
				if lastWriter >= 0 {
					addEdge(lastWriter, a.pass) // RAW
				} else {
					// 读者之前没有写者：依赖所有写者。
					for _, b := range accs {
						if b.write {
							addEdge(b.pass, a.pass)
						}
					}
				}
			}
		}
	}

	// Kahn 分层拓扑排序；层内按名称排序保证确定性。
	level := make([]int, n)
	remaining := make([]int, n)
	copy(remaining, indeg)
	var cur []int
	for i := 0; i < n; i++ {
		if remaining[i] == 0 {
			cur = append(cur, i)
		}
	}
	processed := 0
	for len(cur) > 0 {
		sort.Slice(cur, func(a, b int) bool { return g.passes[cur[a]].Name < g.passes[cur[b]].Name })
		var next []int
		for _, i := range cur {
			processed++
			for _, j := range adj[i] {
				remaining[j]--
				if remaining[j] == 0 {
					level[j] = level[i] + 1
					next = append(next, j)
				}
			}
		}
		cur = next
	}
	if processed < n {
		return nil, fmt.Errorf("graph: cyclic dependency detected: %s", findCycle(g.passes, adj, remaining))
	}

	maxLevel := 0
	for _, l := range level {
		if l > maxLevel {
			maxLevel = l
		}
	}
	c := &Compiled{
		Levels:    make([][]*Pass, maxLevel+1),
		Lifetimes: map[string]Lifetime{},
		Resources: g.resources,
		Deps:      map[string][]string{},
	}
	for i, p := range g.passes {
		c.Levels[level[i]] = append(c.Levels[level[i]], p)
	}
	// 记录直接前驱（去重、按名称排序）。
	for from := 0; from < n; from++ {
		for _, to := range adj[from] {
			name := g.passes[to].Name
			if !contains(c.Deps[name], g.passes[from].Name) {
				c.Deps[name] = append(c.Deps[name], g.passes[from].Name)
			}
		}
	}
	for _, deps := range c.Deps {
		sort.Strings(deps)
	}
	for _, lv := range c.Levels {
		c.Order = append(c.Order, lv...)
	}

	// 计算资源生命周期：从首次写入到最后一次读写。
	for name := range g.resources {
		first, last := -1, -1
		for i, p := range g.passes {
			touches := false
			for _, w := range p.Writes {
				if w == name {
					touches = true
				}
			}
			for _, r := range p.Reads {
				if r == name {
					touches = true
				}
			}
			if touches {
				if first == -1 || level[i] < first {
					first = level[i]
				}
				if level[i] > last {
					last = level[i]
				}
			}
		}
		if first >= 0 {
			c.Lifetimes[name] = Lifetime{First: first, Last: last}
		}
	}
	return c, nil
}

// findCycle 在未完成的节点中找出一条循环路径，用于错误定位。
func findCycle(passes []*Pass, adj [][]int, remaining []int) string {
	n := len(passes)
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, n)
	var stack []int
	var cycle []int
	var dfs func(u int) bool
	dfs = func(u int) bool {
		color[u] = gray
		stack = append(stack, u)
		for _, v := range adj[u] {
			if remaining[v] == 0 { // 只关心未完成的子图
				continue
			}
			if color[v] == gray {
				// 找到回边，截取环
				for i, x := range stack {
					if x == v {
						cycle = append(append([]int{}, stack[i:]...), v)
						return true
					}
				}
			} else if color[v] == white && dfs(v) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[u] = black
		return false
	}
	for i := 0; i < n; i++ {
		if remaining[i] > 0 && color[i] == white {
			if dfs(i) {
				names := make([]string, len(cycle))
				for k, x := range cycle {
					names[k] = passes[x].Name
				}
				return strings.Join(names, " -> ")
			}
		}
	}
	// 理论上不会到这里；兜底列出未完成节点。
	var names []string
	for i := 0; i < n; i++ {
		if remaining[i] > 0 {
			names = append(names, passes[i].Name)
		}
	}
	return "unresolved passes: " + strings.Join(names, ", ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
