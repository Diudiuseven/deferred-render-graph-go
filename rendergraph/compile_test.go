package rendergraph

import "testing"

// 生命周期不重叠的资源应复用同一内存块；重叠的不得复用。
func TestAllocationAliasing(t *testing.T) {
	g := New()
	decl(g, "tmp1")
	decl(g, "tmp2")
	decl(g, "out")
	// p1 写 tmp1（层0），p2 读 tmp1 写 tmp2（层1），p3 读 tmp2 写 out（层2）
	// tmp1 生命周期 [0,1]，tmp2 [1,2] —— 在层1重叠，不得共享；
	// 再构造 tmp3 生命周期 [2,2]，可与 tmp1 共享。
	decl(g, "tmp3")
	g.AddPass(&Pass{Name: "p1", Writes: []ResourceID{"tmp1"}})
	g.AddPass(&Pass{Name: "p2", Reads: []ResourceID{"tmp1"}, Writes: []ResourceID{"tmp2"}})
	g.AddPass(&Pass{Name: "p3", Reads: []ResourceID{"tmp2"}, Writes: []ResourceID{"out", "tmp3"}})
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	a1 := c.Allocation["tmp1"]
	a2 := c.Allocation["tmp2"]
	a3 := c.Allocation["tmp3"]
	aOut := c.Allocation["out"]
	if a1.Buffer == a2.Buffer {
		t.Fatal("overlapping lifetimes must not share a buffer")
	}
	// tmp1 在层 1 结束后释放，层 2 的 out/tmp3 之一应复用其内存块
	if a3.Buffer != a1.Buffer && aOut.Buffer != a1.Buffer {
		t.Fatalf("expected layer-2 resource to reuse tmp1's buffer: tmp1=%d out=%d tmp3=%d",
			a1.Buffer, aOut.Buffer, a3.Buffer)
	}
	// 4 份资源生命周期错开后应只需要 3 块内存
	buffers := map[int]bool{}
	for _, slot := range c.Allocation {
		buffers[slot.Buffer] = true
	}
	if len(buffers) != 3 {
		t.Fatalf("expected 3 buffers after reuse, got %d", len(buffers))
	}
	// 总需求应小于不复用的简单求和
	sum := 0
	for _, d := range g.Resources() {
		sum += d.Bytes()
	}
	if c.TotalBytes >= sum {
		t.Fatalf("expected reuse to reduce total bytes: total=%d sum=%d", c.TotalBytes, sum)
	}
}

// 移除通道后重新编译，依赖关系必须重新解析。
func TestRecompileAfterRemoval(t *testing.T) {
	g := New()
	decl(g, "a")
	decl(g, "b")
	g.AddPass(&Pass{Name: "p1", Writes: []ResourceID{"a"}})
	g.AddPass(&Pass{Name: "p2", Reads: []ResourceID{"a"}, Writes: []ResourceID{"b"}})
	c1, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(c1.Levels) != 2 {
		t.Fatalf("expected 2 levels, got %v", c1.Levels)
	}
	g.RemovePass("p2")
	c2, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Levels) != 1 || len(c2.Levels[0]) != 1 {
		t.Fatalf("expected single level after removal, got %v", c2.Levels)
	}
}
