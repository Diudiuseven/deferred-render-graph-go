package graph

// Format 描述中间结果的像素格式。
type Format int

const (
	FormatRGBA32F Format = iota
	FormatRGB32F
	FormatR32F
	FormatRGBA8
)

// channels 返回每像素的浮点通道数（RGBA8 以 4 个 float 存储以保持确定性）。
func (f Format) channels() int {
	switch f {
	case FormatRGBA32F, FormatRGBA8:
		return 4
	case FormatRGB32F:
		return 3
	case FormatR32F:
		return 1
	default:
		return 4
	}
}

// ResourceDesc 描述一张中间结果（纹理）的尺寸与格式。
type ResourceDesc struct {
	Width  int
	Height int
	Format Format
}

// Pixels 返回像素总数。
func (d ResourceDesc) Pixels() int { return d.Width * d.Height }

// Size 返回该资源占用的字节数。
func (d ResourceDesc) Size() int64 {
	return int64(d.Pixels()) * int64(d.Format.channels()) * 4
}

// Buffer 是中间结果的实际存储。
type Buffer struct {
	Desc ResourceDesc
	Data []float32
	// tag 记录当前内容属于哪个资源（由调度器维护），
	// 用于检测缓冲区被复用后原资源内容是否仍然有效。
	tag string
}

// NewBuffer 按描述分配缓冲区。
func NewBuffer(desc ResourceDesc) *Buffer {
	return &Buffer{Desc: desc, Data: make([]float32, desc.Pixels()*desc.Format.channels())}
}

// Clear 将缓冲区清零。
func (b *Buffer) Clear() {
	for i := range b.Data {
		b.Data[i] = 0
	}
}
