package graph

import "errors"

// ErrOutOfMemory 表示资源预算不足，是可区分的失败（可用 errors.Is 判断）。
var ErrOutOfMemory = errors.New("graph: memory budget exceeded")

// Pool 管理中间结果的分配与复用。
// 生命周期不重叠的资源会复用同一块底层内存；
// 长期运行时空闲块被保留复用，占用不会持续增长。
type Pool struct {
	budget int64
	used   int64
	free   []*Buffer
	inUse  int
}

// NewPool 创建内存池，budget 为字节上限（<=0 表示不限）。
func NewPool(budget int64) *Pool { return &Pool{budget: budget} }

// Acquire 获取一块匹配 desc 的缓冲区，可能复用空闲块。
// 返回 (buffer, reused, error)；预算不足时返回 ErrOutOfMemory。
func (p *Pool) Acquire(desc ResourceDesc) (*Buffer, bool, error) {
	// 优先精确匹配的空闲块，保证确定性。
	for i, b := range p.free {
		if b.Desc == desc {
			p.free = append(p.free[:i], p.free[i+1:]...)
			b.Clear()
			p.inUse++
			return b, true, nil
		}
	}
	need := desc.Size()
	if p.budget > 0 && p.used+need > p.budget {
		return nil, false, ErrOutOfMemory
	}
	buf := NewBuffer(desc)
	p.used += need
	p.inUse++
	return buf, false, nil
}

// Release 归还缓冲区，供后续生命周期不重叠的资源复用。
func (p *Pool) Release(b *Buffer) {
	if b == nil {
		return
	}
	p.inUse--
	p.free = append(p.free, b)
}

// Reclaim 把尚未被复用的缓冲区从空闲列表取回（内容保持完好）。
// 返回 false 表示缓冲区不在空闲列表中（可能已被复用）。
func (p *Pool) Reclaim(b *Buffer) bool {
	for i, x := range p.free {
		if x == b {
			p.free = append(p.free[:i], p.free[i+1:]...)
			p.inUse++
			return true
		}
	}
	return false
}

// Stats 返回当前已分配总字节数与空闲块数。
func (p *Pool) Stats() (used int64, freeBlocks int) { return p.used, len(p.free) }

// InUse 返回正在使用中的块数。
func (p *Pool) InUse() int { return p.inUse }
