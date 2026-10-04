package rendergraph

import "errors"

// ErrResourceExhausted 表示内存预算不足，是可区分的失败（非崩溃）。
var ErrResourceExhausted = errors.New("rendergraph: resource budget exhausted")

// Pool 管理中间结果的生命周期与复用。
// Acquire/Release 成对使用；归还的块进入空闲列表，可被后续 Acquire 复用。
// 池本身不会无限增长：空闲块数量受历史峰值约束，长时间运行内存占用稳定。
type Pool struct {
	BudgetBytes int // 0 表示不限
	used        int // 当前已分配（含空闲块）字节数
	inUse       int // 正在使用中的字节数
	highWater   int
	free        []*Image // 空闲块，按描述匹配复用
}

// NewPool 创建内存池，budget 为字节上限（0 表示不限）。
func NewPool(budget int) *Pool { return &Pool{BudgetBytes: budget} }

// Acquire 获取一块图像内存，优先复用尺寸匹配的空闲块。
// 超出预算时返回 ErrResourceExhausted。
func (p *Pool) Acquire(d ResourceDesc) (*Image, error) {
	need := d.Bytes()
	// 先找可复用的块（尺寸一致，避免内部碎片）
	for i, img := range p.free {
		if img.Desc == d {
			p.free = append(p.free[:i], p.free[i+1:]...)
			p.inUse += need
			if p.inUse > p.highWater {
				p.highWater = p.inUse
			}
			clear(img.Pixels)
			return img, nil
		}
	}
	if p.BudgetBytes > 0 && p.used+need > p.BudgetBytes {
		return nil, ErrResourceExhausted
	}
	img := NewImage(d)
	p.used += need
	p.inUse += need
	if p.inUse > p.highWater {
		p.highWater = p.inUse
	}
	return img, nil
}

// Release 归还图像内存，供后续复用。
func (p *Pool) Release(img *Image) {
	if img == nil {
		return
	}
	p.inUse -= img.Desc.Bytes()
	p.free = append(p.free, img)
}

// HighWater 返回历史峰值占用字节数（在用部分）。
func (p *Pool) HighWater() int { return p.highWater }

// Used 返回当前占用字节数（含空闲块）。
func (p *Pool) Used() int { return p.used }

// IdleBlocks 返回空闲块数量（用于测试复用行为）。
func (p *Pool) IdleBlocks() int { return len(p.free) }
