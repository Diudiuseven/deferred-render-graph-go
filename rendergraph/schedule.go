package rendergraph

import (
	"context"
	"fmt"
	"sync"
)

// RunOptions 控制一次执行的并发与资源约束。
type RunOptions struct {
	MaxConcurrency int             // 并发执行的通道数上限，<=0 表示不限制
	Pool           *Pool           // 内存池；nil 时使用无限制池
	OnlyDirty      bool            // 只执行 Dirty 集合中的通道
	Dirty          map[string]bool // 需要执行的通道集合
	Kept           map[ResourceID]*Image // 上一次执行保留的资源（被跳过通道的产物）
	Keep           map[ResourceID]bool   // 本次执行后需要保留（不释放）的资源
}

// RunReport 记录一次执行的结果，可用于导出比对。
type RunReport struct {
	Executed  []string // 实际执行的通道（按层、层内按声明顺序）
	Skipped   []string
	HighWater int
	Images    map[ResourceID]*Image // 保留资源 + 最终仍存活的资源
}

// Run 按编译结果调度执行通道。
// 依赖顺序由分层保证；同层通道无依赖或只读共享，可并行。
func (c *Compiled) Run(ctx context.Context, opts RunOptions) (*RunReport, error) {
	pool := opts.Pool
	if pool == nil {
		pool = NewPool(0)
	}
	report := &RunReport{Images: map[ResourceID]*Image{}}
	images := map[ResourceID]*Image{}
	for id, img := range opts.Kept {
		images[id] = img
	}

	shouldRun := func(name string) bool {
		if !opts.OnlyDirty {
			return true
		}
		return opts.Dirty[name]
	}

	for li, lvl := range c.Levels {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		// 本层要执行的通道：先为其输出获取内存
		var toRun []string
		for _, name := range lvl {
			if shouldRun(name) {
				toRun = append(toRun, name)
			} else {
				report.Skipped = append(report.Skipped, name)
			}
		}
		for _, name := range toRun {
			p := c.graph.passes[name]
			for _, id := range p.Writes {
				if img := images[id]; img != nil {
					if img.Desc == c.resDescs[id] {
						continue // 保留的资源描述一致，直接复用
					}
					// 描述已变化（配置变更）：归还旧块，重新分配
					pool.Release(img)
					delete(images, id)
				}
				img, err := pool.Acquire(c.resDescs[id])
				if err != nil {
					return report, fmt.Errorf("pass %q: acquire %q: %w", name, id, err)
				}
				images[id] = img
			}
		}
		// 并行执行本层通道，受 MaxConcurrency 限制
		if err := c.runLevel(ctx, toRun, images, opts.MaxConcurrency); err != nil {
			return report, err
		}
		report.Executed = append(report.Executed, toRun...)
		// 释放生命周期在本层结束且无需保留的资源
		for id, last := range c.LastUse {
			if last != li || opts.Keep[id] {
				continue
			}
			if img := images[id]; img != nil {
				pool.Release(img)
				delete(images, id)
			}
		}
	}
	// 收尾：保留的进入报告，其余释放（包括 Kept 中已不再使用的）
	for id, img := range images {
		if opts.Keep[id] {
			report.Images[id] = img
		} else {
			pool.Release(img)
		}
	}
	report.HighWater = pool.HighWater()
	return report, nil
}

// runLevel 并行执行一层通道；信号量限制并发度，ctx 取消时及时退出，不死锁。
func (c *Compiled) runLevel(ctx context.Context, names []string, images map[ResourceID]*Image, maxConc int) error {
	if len(names) == 0 {
		return nil
	}
	var sem chan struct{}
	if maxConc > 0 {
		sem = make(chan struct{}, maxConc)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, name := range names {
		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if sem != nil {
				defer func() { <-sem }()
			}
			p := c.graph.passes[name]
			if p.Execute == nil {
				return
			}
			execCtx := &ExecContext{images: images}
			if err := p.Execute(execCtx); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("pass %q: %w", name, err)
				}
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	return firstErr
}
