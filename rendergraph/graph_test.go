package rendergraph

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// 辅助：声明一个资源
func decl(g *Graph, name string) {
	g.DeclareResource(ResourceDesc{Name: ResourceID(name), Format: FormatRGBA32F, Width: 4, Height: 4})
}

func TestTopoOrder(t *testing.T) {
	g := New()
	decl(g, "a")
	decl(g, "b")
	decl(g, "c")
	// p1 写 a，p2 读 a 写 b，p3 读 b 写 c
	g.AddPass(&Pass{Name: "p1", Writes: []ResourceID{"a"}})
	g.AddPass(&Pass{Name: "p2", Reads: []ResourceID{"a"}, Writes: []ResourceID{"b"}})
	g.AddPass(&Pass{Name: "p3", Reads: []ResourceID{"b"}, Writes: []ResourceID{"c"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Levels) != 3 {
		t.Fatalf("expected 3 levels, got %v", c.Levels)
	}
	if c.Levels[0][0] != "p1" || c.Levels[1][0] != "p2" || c.Levels[2][0] != "p3" {
		t.Fatalf("wrong order: %v", c.Levels)
	}
}

func TestParallelLevelsForIndependentAndSharedRead(t *testing.T) {
	g := New()
	decl(g, "src")
	decl(g, "x")
	decl(g, "y")
	decl(g, "z")
	g.AddPass(&Pass{Name: "gen", Writes: []ResourceID{"src"}})
	// r1、r2 只读共享 src，应同层；w 与它们无依赖，也应同层
	g.AddPass(&Pass{Name: "r1", Reads: []ResourceID{"src"}, Writes: []ResourceID{"x"}})
	g.AddPass(&Pass{Name: "r2", Reads: []ResourceID{"src"}, Writes: []ResourceID{"y"}})
	g.AddPass(&Pass{Name: "w", Writes: []ResourceID{"z"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Levels) != 2 {
		t.Fatalf("expected 2 levels, got %v", c.Levels)
	}
	// w 无依赖，与 gen 同在第 0 层；r1、r2 只读共享 src，同在第 1 层
	if len(c.Levels[0]) != 2 || len(c.Levels[1]) != 2 {
		t.Fatalf("unexpected leveling: %v", c.Levels)
	}
}

func TestReaderRunsAfterWriter(t *testing.T) {
	g := New()
	decl(g, "a")
	// reader 先声明但读 writer 写的资源：RAW 依赖要求 writer 先执行
	g.AddPass(&Pass{Name: "reader", Reads: []ResourceID{"a"}})
	g.AddPass(&Pass{Name: "writer", Writes: []ResourceID{"a"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(c.Levels) != 2 || c.Levels[0][0] != "writer" || c.Levels[1][0] != "reader" {
		t.Fatalf("RAW dependency not ordered: %v", c.Levels)
	}
}

func TestCycleDetection(t *testing.T) {
	g := New()
	decl(g, "a")
	decl(g, "b")
	decl(g, "c")
	g.AddPass(&Pass{Name: "p1", Reads: []ResourceID{"c"}, Writes: []ResourceID{"a"}})
	g.AddPass(&Pass{Name: "p2", Reads: []ResourceID{"a"}, Writes: []ResourceID{"b"}})
	g.AddPass(&Pass{Name: "p3", Reads: []ResourceID{"b"}, Writes: []ResourceID{"c"}})
	_, err := g.Compile()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CycleError, got %T: %v", err, err)
	}
	// 错误必须可定位：路径中应包含构成环的通道
	msg := err.Error()
	for _, name := range []string{"p1", "p2", "p3"} {
		if !strings.Contains(msg, name) {
			t.Fatalf("cycle error should mention %q: %s", name, msg)
		}
	}
}

func TestUnknownResource(t *testing.T) {
	g := New()
	g.AddPass(&Pass{Name: "p", Reads: []ResourceID{"missing"}})
	_, err := g.Compile()
	if !errors.Is(err, ErrUnknownResource) {
		t.Fatalf("expected ErrUnknownResource, got %v", err)
	}
}

func TestRunExecutesInDependencyOrder(t *testing.T) {
	g := New()
	decl(g, "a")
	decl(g, "b")
	var order atomic.Int64
	steps := map[string]int64{}
	mk := func(name string, reads, writes []ResourceID) *Pass {
		return &Pass{
			Name: name, Reads: reads, Writes: writes,
			Execute: func(ctx *ExecContext) error {
				steps[name] = order.Add(1)
				return nil
			},
		}
	}
	g.AddPass(mk("first", nil, []ResourceID{"a"}))
	g.AddPass(mk("second", []ResourceID{"a"}, []ResourceID{"b"}))
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if steps["first"] >= steps["second"] {
		t.Fatalf("wrong execution order: %v", steps)
	}
	if len(rep.Executed) != 2 {
		t.Fatalf("expected 2 executed, got %v", rep.Executed)
	}
}

func TestConcurrencyLimitRespected(t *testing.T) {
	g := New()
	decl(g, "s")
	var cur, maxSeen atomic.Int64
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		out := ResourceID("out_" + name)
		decl(g, string(out))
		g.AddPass(&Pass{
			Name: name, Writes: []ResourceID{out},
			Execute: func(ctx *ExecContext) error {
				n := cur.Add(1)
				for {
					m := maxSeen.Load()
					if n <= m || maxSeen.CompareAndSwap(m, n) {
						break
					}
				}
				// 简单忙等制造重叠窗口
				for i := 0; i < 100000; i++ {
				}
				cur.Add(-1)
				return nil
			},
		})
	}
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), RunOptions{MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if maxSeen.Load() > 2 {
		t.Fatalf("concurrency limit violated: saw %d", maxSeen.Load())
	}
}

func TestContextCancellation(t *testing.T) {
	g := New()
	decl(g, "a")
	g.AddPass(&Pass{Name: "p", Writes: []ResourceID{"a"}})
	c, _ := g.Compile()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Run(ctx, RunOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDirtyPasses(t *testing.T) {
	build := func(cfgB, cfgC string) *Graph {
		g := New()
		decl(g, "a")
		decl(g, "b")
		decl(g, "c")
		g.AddPass(&Pass{Name: "pa", Writes: []ResourceID{"a"}, ConfigKey: "A"})
		g.AddPass(&Pass{Name: "pb", Reads: []ResourceID{"a"}, Writes: []ResourceID{"b"}, ConfigKey: cfgB})
		g.AddPass(&Pass{Name: "pc", Reads: []ResourceID{"b"}, Writes: []ResourceID{"c"}, ConfigKey: cfgC})
		return g
	}
	g1 := build("B1", "C1")
	c1, err := g1.Compile()
	if err != nil {
		t.Fatal(err)
	}
	// 首次：全部脏
	if got := c1.DirtyPasses(nil); len(got) != 3 {
		t.Fatalf("first compile should mark all dirty, got %v", got)
	}
	// 无变化：无脏通道
	g2 := build("B1", "C1")
	c2, _ := g2.Compile()
	if got := c2.DirtyPasses(c1); len(got) != 0 {
		t.Fatalf("no change should mean no dirty passes, got %v", got)
	}
	// pb 配置变化：pb 与下游 pc 脏，pa 不脏
	g3 := build("B2", "C1")
	c3, _ := g3.Compile()
	got := c3.DirtyPasses(c2)
	if len(got) != 2 || got[0] != "pb" || got[1] != "pc" {
		t.Fatalf("expected [pb pc] dirty, got %v", got)
	}
}

func TestExportPlanDeterministic(t *testing.T) {
	build := func() *Compiled {
		g := New()
		decl(g, "a")
		decl(g, "b")
		g.AddPass(&Pass{Name: "p1", Writes: []ResourceID{"a"}})
		g.AddPass(&Pass{Name: "p2", Reads: []ResourceID{"a"}, Writes: []ResourceID{"b"}})
		c, err := g.Compile()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	e1, err := build().ExportPlan()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := build().ExportPlan()
	if err != nil {
		t.Fatal(err)
	}
	if string(e1) != string(e2) {
		t.Fatal("export plan not deterministic")
	}
}
