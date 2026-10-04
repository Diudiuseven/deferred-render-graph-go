package graph

// PassContext 在执行通道时提供对资源的访问。
type PassContext struct {
	// Buffers 保存本帧已分配的中间结果。
	Buffers map[string]*Buffer
}

// Get 返回指定名称资源的缓冲区，不存在时返回 nil。
func (c *PassContext) Get(name string) *Buffer { return c.Buffers[name] }

// MustGet 返回指定资源，不存在时 panic（说明图编译有误）。
func (c *PassContext) MustGet(name string) *Buffer {
	b := c.Buffers[name]
	if b == nil {
		panic("graph: missing resource " + name)
	}
	return b
}

// Pass 描述渲染图中的一个渲染通道。
type Pass struct {
	Name   string
	Reads  []string
	Writes []string
	// ConfigHash 用于管线变更后判断通道配置是否变化；
	// 由通道构造方根据配置计算。
	ConfigHash uint64
	// Execute 为通道的实际工作。
	Execute func(ctx *PassContext) error
}
