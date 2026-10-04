package graph

import (
	"sync/atomic"
	"testing"
	"time"
)

// 验证并发执行的通道数不超过配置上限，且不会死锁。
func TestMaxParallelPassesEnforced(t *testing.T) {
	g := NewGraph()
	g.DeclareResource("in", ResourceDesc{4, 4, FormatR32F})
	g.AddPass(&Pass{Name: "src", Writes: []string{"in"}})
	const n = 8
	var cur, maxSeen int64
	for i := 0; i < n; i++ {
		name := string(rune('a'+i)) + "x"
		out := name + "out"
		g.DeclareResource(out, ResourceDesc{4, 4, FormatR32F})
		g.AddPass(&Pass{
			Name: name, Reads: []string{"in"}, Writes: []string{out},
			Execute: func(*PassContext) error {
				c := atomic.AddInt64(&cur, 1)
				for {
					m := atomic.LoadInt64(&maxSeen)
					if c <= m || atomic.CompareAndSwapInt64(&maxSeen, m, c) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt64(&cur, -1)
				return nil
			},
		})
	}
	c, err := g.Compile()
	if err != nil {
		t.Fatal(err)
	}
	const limit = 3
	s := NewScheduler(SchedulerOptions{MaxParallelPasses: limit})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := s.Run(c, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run deadlocked or blocked too long")
	}
	if maxSeen > limit {
		t.Fatalf("concurrency %d exceeded limit %d", maxSeen, limit)
	}
	if maxSeen < 2 {
		t.Fatalf("expected some parallelism, got %d", maxSeen)
	}
}
