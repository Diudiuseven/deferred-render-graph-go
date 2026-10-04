package rendergraph

import (
	"errors"
	"testing"
)

func desc(name string, w, h int) ResourceDesc {
	return ResourceDesc{Name: ResourceID(name), Format: FormatRGBA32F, Width: w, Height: h}
}

func TestPoolReuse(t *testing.T) {
	p := NewPool(0)
	a, err := p.Acquire(desc("a", 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	usedBefore := p.Used()
	p.Release(a)
	if p.IdleBlocks() != 1 {
		t.Fatalf("expected 1 idle block, got %d", p.IdleBlocks())
	}
	// 相同描述应复用同一块内存，总占用不增长
	b, err := p.Acquire(desc("a", 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	if b != a {
		t.Fatal("expected reused block")
	}
	if p.Used() != usedBefore {
		t.Fatalf("memory grew after reuse: before=%d after=%d", usedBefore, p.Used())
	}
}

func TestPoolBudgetExceeded(t *testing.T) {
	d := desc("a", 4, 4) // 4*4*16 = 256 字节
	p := NewPool(d.Bytes())
	if _, err := p.Acquire(d); err != nil {
		t.Fatal(err)
	}
	_, err := p.Acquire(desc("b", 4, 4))
	if !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("expected ErrResourceExhausted, got %v", err)
	}
}

func TestPoolNoGrowthOverFrames(t *testing.T) {
	p := NewPool(0)
	d1 := desc("a", 8, 8)
	d2 := desc("b", 8, 8)
	// 模拟多帧：每帧获取-释放，峰值与总量应稳定
	var firstPeak int
	for frame := 0; frame < 10; frame++ {
		x, err := p.Acquire(d1)
		if err != nil {
			t.Fatal(err)
		}
		y, err := p.Acquire(d2)
		if err != nil {
			t.Fatal(err)
		}
		p.Release(x)
		p.Release(y)
		if frame == 0 {
			firstPeak = p.HighWater()
		}
	}
	if p.HighWater() != firstPeak {
		t.Fatalf("high water grew: first=%d now=%d", firstPeak, p.HighWater())
	}
	if p.Used() != firstPeak {
		t.Fatalf("pool kept growing: used=%d peak=%d", p.Used(), firstPeak)
	}
}

func TestPoolDifferentDescsNotMixed(t *testing.T) {
	p := NewPool(0)
	a, _ := p.Acquire(desc("a", 4, 4))
	p.Release(a)
	// 不同尺寸不应复用
	b, err := p.Acquire(desc("b", 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	if b == a {
		t.Fatal("different desc should not reuse block")
	}
}
