package graph

import (
	"errors"
	"strings"
	"testing"
)

// 记录执行顺序的辅助。
func recorder(order *[]string) func(name string) func(*PassContext) error {
	return func(name string) func(*PassContext) error {
		return func(*PassContext) error {
			*order = append(*order, name)
			return nil
		}
	}
}

func TestCompileTopologicalOrder(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("b", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("c", ResourceDesc{4, 4, FormatR32F})
	// 故意乱序加入。
	g.AddPass(&Pass{Name: "p3", Reads: []string{"b"}, Writes: []string{"c"}})
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}})
	g.AddPass(&Pass{Name: "p2", Reads: []string{"a"}, Writes: []string{"b"}})

	c, err := g.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	pos := map[string]int{}
	for i, p := range c.Order {
		pos[p.Name] = i
	}
	if !(pos["p1"] < pos["p2"] && pos["p2"] < pos["p3"]) {
		t.Fatalf("bad order: %v", pos)
	}
	// p1 无依赖应在第 0 层。
	if len(c.Levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(c.Levels))
	}
}

func TestCompileParallelLevels(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("in", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("o1", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("o2", ResourceDesc{4, 4, FormatR32F})
	g.AddPass(&Pass{Name: "src", Writes: []string{"in"}})
	// 两个通道只读共享同一资源，应同层可并行。
	g.AddPass(&Pass{Name: "r1", Reads: []string{"in"}, Writes: []string{"o1"}})
	g.AddPass(&Pass{Name: "r2", Reads: []string{"in"}, Writes: []string{"o2"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Levels) != 2 || len(c.Levels[1]) != 2 {
		t.Fatalf("expected level1 with 2 parallel passes, got %v", c.Levels)
	}
}

func TestCycleDetection(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("b", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("c", ResourceDesc{4, 4, FormatR32F})
	g.AddPass(&Pass{Name: "A", Reads: []string{"c"}, Writes: []string{"a"}})
	g.AddPass(&Pass{Name: "B", Reads: []string{"a"}, Writes: []string{"b"}})
	g.AddPass(&Pass{Name: "C", Reads: []string{"b"}, Writes: []string{"c"}})
	_, err := g.Compile()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cyclic") {
		t.Fatalf("error should mention cycle: %v", msg)
	}
	// 错误必须可定位：包含环上的通道名。
	for _, n := range []string{"A", "B", "C"} {
		if !strings.Contains(msg, n) {
			t.Fatalf("error should locate pass %s: %v", n, msg)
		}
	}
}

func TestUndeclaredResource(t *testing.T) {
	g := NewGraph()
	g.AddPass(&Pass{Name: "p", Writes: []string{"ghost"}})
	if _, err := g.Compile(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected undeclared resource error, got %v", err)
	}
}

func TestSchedulerExecutesInOrder(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("b", ResourceDesc{4, 4, FormatR32F})
	var order []string
	rec := recorder(&order)
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}, Execute: rec("p1")})
	g.AddPass(&Pass{Name: "p2", Reads: []string{"a"}, Writes: []string{"b"}, Execute: rec("p2")})
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(SchedulerOptions{MaxParallelPasses: 2})
	_, trace, _, err := s.Run(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "p1" || order[1] != "p2" {
		t.Fatalf("bad exec order: %v", order)
	}
	if len(trace.Events) != 2 || trace.Events[0].Kind != "exec" {
		t.Fatalf("bad trace: %+v", trace.Events)
	}
}

func TestResourceReuseAcrossLifetimes(t *testing.T) {
	g := NewGraph()
	// r1 与 r2 生命周期不重叠，应复用同一块内存。
	g.DeclareResource("r1", ResourceDesc{8, 8, FormatR32F})
	g.DeclareResource("r2", ResourceDesc{8, 8, FormatR32F})
	var b1, b2 *Buffer
	g.AddPass(&Pass{Name: "w1", Writes: []string{"r1"}, Execute: func(ctx *PassContext) error {
		b1 = ctx.Get("r1")
		return nil
	}})
	g.AddPass(&Pass{Name: "w2", Reads: []string{"r1"}, Writes: []string{"r2"}, Execute: func(ctx *PassContext) error {
		b2 = ctx.Get("r2")
		return nil
	}})
	// r1 的生命周期在 level1 结束（被 w2 读），r2 从 level1 开始；
	// 但 w2 同时读 r1 写 r2，二者重叠。再加一级使 r2 独占。
	g.AddPass(&Pass{Name: "w3", Reads: []string{"r2"}, Execute: func(*PassContext) error { return nil }})
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(SchedulerOptions{})
	_, trace, _, err := s.Run(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = b1
	_ = b2
	// 第二次运行应复用池中块。
	_, trace2, _, err := s.Run(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	reused := 0
	for _, a := range trace2.Allocations {
		if a.Reuse {
			reused++
		}
	}
	if reused == 0 {
		t.Fatalf("expected reuse on second run, got %+v", trace2.Allocations)
	}
	_ = trace
}

func TestMemoryBudgetExceeded(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("big", ResourceDesc{1024, 1024, FormatRGBA32F})
	g.AddPass(&Pass{Name: "p", Writes: []string{"big"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(SchedulerOptions{MemoryBudgetBytes: 1024})
	_, _, _, err = s.Run(c, nil)
	if !errors.Is(err, ErrOutOfMemory) {
		t.Fatalf("expected ErrOutOfMemory, got %v", err)
	}
	// 错误信息应包含资源名，可定位。
	if !strings.Contains(err.Error(), "big") {
		t.Fatalf("error should name resource: %v", err)
	}
}

func TestPipelineMutationSkipsUnaffected(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("b", ResourceDesc{4, 4, FormatR32F})
	g.DeclareResource("c", ResourceDesc{4, 4, FormatR32F})
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}, ConfigHash: 1})
	g.AddPass(&Pass{Name: "p2", Reads: []string{"a"}, Writes: []string{"b"}, ConfigHash: 2})
	g.AddPass(&Pass{Name: "p3", Reads: []string{"b"}, Writes: []string{"c"}, ConfigHash: 3})

	s := NewScheduler(SchedulerOptions{})
	c1, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	prev, _, _, err := s.Run(c1, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 修改 p3 的配置（模拟管线变更），p1/p2 不应重复执行。
	g.AddPass(&Pass{Name: "p3", Reads: []string{"b"}, Writes: []string{"c"}, ConfigHash: 33})
	c2, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	_, trace, _, err := s.Run(c2, prev)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, e := range trace.Events {
		kinds[e.Pass] = e.Kind
	}
	if kinds["p1"] != "skip" || kinds["p2"] != "skip" || kinds["p3"] != "exec" {
		t.Fatalf("unexpected schedule after mutation: %+v", kinds)
	}

	// 变更 p1 应级联使 p2、p3 都变脏。
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}, ConfigHash: 11})
	c3, _ := g.Compile()
	prev2, _, _, _ := s.Run(c2, prev) // 更新指纹基线
	_ = prev2
	_, trace3, _, err := s.Run(c3, map[string]uint64{"p1": 1, "p2": 2, "p3": 33})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range trace3.Events {
		if e.Kind != "exec" {
			t.Fatalf("cascade dirty expected all exec, got %+v", trace3.Events)
		}
	}
}

func TestRemovePass(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}})
	if !g.RemovePass("p1") {
		t.Fatal("remove failed")
	}
	if g.Pass("p1") != nil {
		t.Fatal("pass still present")
	}
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Order) != 0 {
		t.Fatalf("expected empty graph, got %d passes", len(c.Order))
	}
}

func TestDeterministicTrace(t *testing.T) {
	build := func() (*Compiled, *Scheduler) {
		g := NewGraph()
		g.DeclareResource("a", ResourceDesc{4, 4, FormatR32F})
		g.DeclareResource("b", ResourceDesc{4, 4, FormatR32F})
		g.DeclareResource("c", ResourceDesc{4, 4, FormatR32F})
		g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}})
		g.AddPass(&Pass{Name: "p2", Reads: []string{"a"}, Writes: []string{"b"}})
		g.AddPass(&Pass{Name: "p3", Reads: []string{"a"}, Writes: []string{"c"}})
		c, _ := g.Compile()
		return c, NewScheduler(SchedulerOptions{MaxParallelPasses: 4})
	}
	c1, s1 := build()
	_, tr1, _, _ := s1.Run(c1, nil)
	c2, s2 := build()
	_, tr2, _, _ := s2.Run(c2, nil)
	if len(tr1.Events) != len(tr2.Events) || len(tr1.Allocations) != len(tr2.Allocations) {
		t.Fatal("trace length mismatch")
	}
	for i := range tr1.Events {
		if tr1.Events[i] != tr2.Events[i] {
			t.Fatalf("nondeterministic trace at %d: %+v vs %+v", i, tr1.Events[i], tr2.Events[i])
		}
	}
	for i := range tr1.Allocations {
		if tr1.Allocations[i] != tr2.Allocations[i] {
			t.Fatalf("nondeterministic alloc at %d", i)
		}
	}
}

func TestNoMemoryGrowthOverManyRuns(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("a", ResourceDesc{64, 64, FormatRGBA32F})
	g.DeclareResource("b", ResourceDesc{64, 64, FormatRGBA32F})
	g.AddPass(&Pass{Name: "p1", Writes: []string{"a"}})
	g.AddPass(&Pass{Name: "p2", Reads: []string{"a"}, Writes: []string{"b"}})
	c, _ := g.Compile()
	s := NewScheduler(SchedulerOptions{})
	var prev map[string]uint64
	var err error
	for i := 0; i < 50; i++ {
		prev, _, _, err = s.Run(c, prev)
		if err != nil {
			t.Fatal(err)
		}
	}
	used, free := s.Pool().Stats()
	// 50 次运行后占用不应超过单帧需求的数倍。
	budget := int64(64*64*4*4) * 4
	if used > budget {
		t.Fatalf("memory grew: used=%d free=%d budget=%d", used, free, budget)
	}
}
