package graph

import (
	"fmt"
	"sort"
	"sync"
)

// TraceEvent 记录一次调度事件，用于导出与比对。
type TraceEvent struct {
	Pass  string
	Level int
	Kind  string // "exec" | "skip"
}

// Trace 记录一次渲染的调度顺序与资源分配结果。
type Trace struct {
	Events      []TraceEvent
	Allocations []AllocEvent
}

// AllocEvent 记录资源分配/复用事件。
type AllocEvent struct {
	Resource string
	Level    int
	Reuse    bool   // 是否复用（池内空闲块或跨帧保留）
	Source   string // "pool" | "retained" | "new"
	Bytes    int64
}

// SchedulerOptions 控制并发与资源约束。
type SchedulerOptions struct {
	// MaxParallelPasses 为并发执行的通道数上限，<=0 时取 4。
	MaxParallelPasses int
	// MemoryBudgetBytes 为中间结果的内存预算（字节），<=0 表示不限。
	MemoryBudgetBytes int64
}

// Scheduler 执行编译后的渲染图。
//
// 增量执行与跨帧保留：
//   - 调度器记录每个资源内容是否仍然有效（valid）。
//   - 运行结束时，未被其他资源复用覆盖的缓冲区被保留（retained），
//     下次运行时干净通道的输出可直接沿用，无需重复执行。
//   - 若某资源的缓冲区在运行中被生命周期不重叠的其他资源复用，
//     其内容失效，下次运行时写出它的通道会被自动标脏重跑，
//     保证结果与全量执行一致。
//
// 保留集合以资源名为键，大小以图中资源数为界，不会随运行时间增长。
type Scheduler struct {
	Opts     SchedulerOptions
	pool     *Pool
	retained map[string]*Buffer
	valid    map[string]bool
}

// NewScheduler 创建调度器。
func NewScheduler(opts SchedulerOptions) *Scheduler {
	if opts.MaxParallelPasses <= 0 {
		opts.MaxParallelPasses = 4
	}
	return &Scheduler{
		Opts:     opts,
		pool:     NewPool(opts.MemoryBudgetBytes),
		retained: map[string]*Buffer{},
		valid:    map[string]bool{},
	}
}

// Pool 返回调度器的内存池（用于观测内存占用）。
func (s *Scheduler) Pool() *Pool { return s.pool }

// RetainedCount 返回跨帧保留的资源数。
func (s *Scheduler) RetainedCount() int { return len(s.retained) }

// Run 按编译结果执行通道。
//
// prev 为上一次运行的通道指纹（通道名 -> ConfigHash），首次运行传 nil。
// 返回本次运行的指纹、调度轨迹与错误。
// 配置未变、依赖未受影响且输出内容仍有效的通道会被跳过；
// 被跳过的通道其输出来自保留内容，画面与全量重跑一致。
//
// 返回的 buffers 包含本次运行涉及的所有资源，其内容在下一次 Run 前有效。
func (s *Scheduler) Run(c *Compiled, prev map[string]uint64) (cur map[string]uint64, trace *Trace, buffers map[string]*Buffer, err error) {
	trace = &Trace{}
	cur = make(map[string]uint64, len(c.Order))
	buffers = map[string]*Buffer{}

	// 清理已不在图中的保留项（管线变更删除资源后释放内存）。
	for name, b := range s.retained {
		if _, ok := c.Resources[name]; !ok {
			s.pool.Release(b)
			delete(s.retained, name)
			delete(s.valid, name)
		}
	}

	// 计算脏集合，分两步：
	//  1. 前向（拓扑序）：配置变化（或首次运行）或任一前驱脏的通道为脏。
	//  2. 反向（逆拓扑序）：干净通道写出的资源若被需要（被脏通道读取，
	//     或是图的最终输出）但内容已失效，则该通道必须重跑；
	//     这会使其读取的资源也变为“被需要”，继续向前传播。
	dirty := make(map[string]bool, len(c.Order))
	for _, p := range c.Order {
		d := prev == nil || prev[p.Name] != p.ConfigHash
		if !d {
			for _, dep := range c.Deps[p.Name] {
				if dirty[dep] {
					d = true
					break
				}
			}
		}
		dirty[p.Name] = d
		cur[p.Name] = p.ConfigHash
	}
	// 图的最终输出（没有任何读者的资源）始终被需要。
	required := map[string]bool{}
	for name := range c.Resources {
		hasReader := false
		for _, p := range c.Order {
			if contains(p.Reads, name) {
				hasReader = true
				break
			}
		}
		if !hasReader {
			required[name] = true
		}
	}
	for i := len(c.Order) - 1; i >= 0; i-- {
		p := c.Order[i]
		if dirty[p.Name] {
			for _, r := range p.Reads {
				required[r] = true
			}
			continue
		}
		for _, w := range p.Writes {
			if required[w] && !s.valid[w] {
				dirty[p.Name] = true
				for _, r := range p.Reads {
					required[r] = true
				}
				break
			}
		}
	}

	// 需要物化的资源：被脏通道读写的资源。
	needed := map[string]bool{}
	for _, p := range c.Order {
		if !dirty[p.Name] {
			continue
		}
		for _, w := range p.Writes {
			needed[w] = true
		}
		for _, r := range p.Reads {
			needed[r] = true
		}
	}

	// 写出某资源的所有通道都干净时，该资源内容可跨帧保留。
	cleanOutput := map[string]bool{}
	for name := range c.Resources {
		allClean := true
		hasWriter := false
		for _, p := range c.Order {
			if contains(p.Writes, name) {
				hasWriter = true
				if dirty[p.Name] {
					allClean = false
					break
				}
			}
		}
		cleanOutput[name] = hasWriter && allClean
	}

	releaseAll := func() {
		for name, b := range buffers {
			if _, kept := s.retained[name]; !kept {
				s.pool.Release(b)
			}
		}
	}

	// 逐层执行；层内并行，层间以 WaitGroup 屏障同步，不会死锁。
	for lvl, passes := range c.Levels {
		// 1. 为生命周期起始于本层的资源准备内存（按名称排序保证确定性）。
		var names []string
		for name, lt := range c.Lifetimes {
			if lt.First == lvl && (needed[name] || cleanOutput[name]) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			desc := c.Resources[name]
			// 跨帧保留且内容有效的干净资源：直接沿用，不清除。
			if cleanOutput[name] && s.valid[name] {
				if rb, ok := s.retained[name]; ok {
					delete(s.retained, name)
					if rb.Desc == desc {
						buffers[name] = rb
						trace.Allocations = append(trace.Allocations, AllocEvent{
							Resource: name, Level: lvl, Reuse: true, Source: "retained", Bytes: desc.Size(),
						})
						continue
					}
					s.pool.Release(rb) // 描述已变化：旧块归还池中
				}
			} else if rb, ok := s.retained[name]; ok && rb.Desc == desc {
				// 保留块存在但内容将被重写：复用内存，清除内容。
				delete(s.retained, name)
				rb.Clear()
				rb.tag = name
				buffers[name] = rb
				trace.Allocations = append(trace.Allocations, AllocEvent{
					Resource: name, Level: lvl, Reuse: true, Source: "retained", Bytes: desc.Size(),
				})
				continue
			}
			buf, reused, aerr := s.pool.Acquire(desc)
			if aerr != nil {
				releaseAll()
				return nil, nil, nil, fmt.Errorf("graph: allocate resource %q (%d bytes): %w", name, desc.Size(), aerr)
			}
			buf.tag = name
			src := "new"
			if reused {
				src = "pool"
			}
			buffers[name] = buf
			trace.Allocations = append(trace.Allocations, AllocEvent{
				Resource: name, Level: lvl, Reuse: reused, Source: src, Bytes: desc.Size(),
			})
		}

		// 2. 并行执行本层通道，受 MaxParallelPasses 限制。
		type result struct{ err error }
		results := make([]result, len(passes))
		sem := make(chan struct{}, s.Opts.MaxParallelPasses)
		var wg sync.WaitGroup
		for i, p := range passes {
			if !dirty[p.Name] {
				continue
			}
			wg.Add(1)
			go func(i int, p *Pass) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if p.Execute != nil {
					ctx := &PassContext{Buffers: buffers}
					results[i].err = p.Execute(ctx)
				}
			}(i, p)
		}
		wg.Wait()
		// 按层内确定性顺序（编译时已按名称排序）记录事件并汇总错误。
		for i, p := range passes {
			if !dirty[p.Name] {
				trace.Events = append(trace.Events, TraceEvent{Pass: p.Name, Level: lvl, Kind: "skip"})
				continue
			}
			if results[i].err != nil {
				releaseAll()
				return nil, nil, nil, fmt.Errorf("graph: pass %q: %w", p.Name, results[i].err)
			}
			trace.Events = append(trace.Events, TraceEvent{Pass: p.Name, Level: lvl, Kind: "exec"})
		}

		// 3. 生命周期结束于本层的资源归还池中（内容在池中保持完好，
		//    直到被其他资源复用；运行结束时会尝试取回保留）。
		names = names[:0]
		for name, lt := range c.Lifetimes {
			if lt.Last == lvl && buffers[name] != nil {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			s.pool.Release(buffers[name])
		}
	}

	// 运行结束：取回内容仍然完好的缓冲区作为跨帧保留，
	// 并更新每个资源的内容有效性标记。
	for name, b := range buffers {
		if b.tag != name {
			// 缓冲区已被其他资源复用，内容丢失。
			s.valid[name] = false
			continue
		}
		if s.pool.Reclaim(b) {
			s.retained[name] = b
			s.valid[name] = true
		} else {
			// 不在空闲列表中：可能仍被引用，保守起见直接保留。
			s.retained[name] = b
			s.valid[name] = true
		}
	}
	return cur, trace, buffers, nil
}
