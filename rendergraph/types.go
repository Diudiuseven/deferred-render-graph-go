// Package rendergraph 提供渲染图的核心数据结构：通道、资源、依赖声明。
package rendergraph

// Format 描述中间结果的像素格式。
type Format int

const (
	FormatRGBA8 Format = iota
	FormatRGBA16F
	FormatRGBA32F
	FormatR32F
)

// BytesPerPixel 返回该格式每像素占用的字节数。
func (f Format) BytesPerPixel() int {
	switch f {
	case FormatRGBA8:
		return 4
	case FormatRGBA16F:
		return 8
	case FormatRGBA32F:
		return 16
	case FormatR32F:
		return 4
	}
	return 4
}

// ResourceID 唯一标识一份中间结果。
type ResourceID string

// ResourceDesc 描述一份中间结果的尺寸与格式。
type ResourceDesc struct {
	Name   ResourceID
	Format Format
	Width  int
	Height int
}

// Bytes 返回该资源占用的字节数。
func (d ResourceDesc) Bytes() int {
	return d.Format.BytesPerPixel() * d.Width * d.Height
}

// Image 是一份中间结果的实际存储。
type Image struct {
	Desc   ResourceDesc
	Pixels []float32 // 每像素 4 个 float32，内部统一表示
}

// NewImage 按描述创建图像。
func NewImage(d ResourceDesc) *Image {
	return &Image{Desc: d, Pixels: make([]float32, d.Width*d.Height*4)}
}

// AccessMode 通道对资源的访问方式。
type AccessMode int

const (
	Read AccessMode = iota
	Write
)

// Access 声明一次资源访问。
type Access struct {
	Resource ResourceID
	Mode     AccessMode
}

// ExecContext 在通道执行时提供对资源的访问。
type ExecContext struct {
	images map[ResourceID]*Image
}

// Get 返回资源对应的图像，不存在时返回 nil。
func (c *ExecContext) Get(id ResourceID) *Image {
	if c == nil {
		return nil
	}
	return c.images[id]
}

// Pass 是渲染图中的一个渲染通道。
type Pass struct {
	Name    string
	Reads   []ResourceID
	Writes  []ResourceID
	Execute func(ctx *ExecContext) error
	// ConfigKey 标识通道配置；配置变化时该 key 必须变化，用于增量重执行。
	ConfigKey string
}
