package deferred

import (
	"fmt"

	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

// G-Buffer 目标语义索引。布局中的目标按此顺序解释：
// 位置、法线、反照率、（可选）材质参数。
const (
	TargetPosition = iota
	TargetNormal
	TargetAlbedo
	TargetMaterial
)

// GBufferLayout 描述 G-Buffer 的目标数量与格式，可配置。
// Targets 的长度决定中间结果数量，每个元素的格式决定存储精度。
type GBufferLayout struct {
	Width, Height int
	Targets       []graph.Format
}

// DefaultLayout 返回默认 G-Buffer 布局：位置、法线、反照率。
func DefaultLayout(w, h int) GBufferLayout {
	return GBufferLayout{
		Width:  w,
		Height: h,
		Targets: []graph.Format{
			graph.FormatRGB32F, // 位置
			graph.FormatRGB32F, // 法线
			graph.FormatRGBA8,  // 反照率
		},
	}
}

// FullLayout 返回带材质参数的完整布局。
func FullLayout(w, h int) GBufferLayout {
	l := DefaultLayout(w, h)
	l.Targets = append(l.Targets, graph.FormatR32F) // 高光系数
	return l
}

// TargetNames 返回各目标的资源名（gbuffer0, gbuffer1, ...）。
func (l GBufferLayout) TargetNames() []string {
	names := make([]string, len(l.Targets))
	for i := range l.Targets {
		names[i] = fmt.Sprintf("gbuffer%d", i)
	}
	return names
}

// DepthName 返回深度资源名。
func (l GBufferLayout) DepthName() string { return "gbuffer-depth" }

// Descs 返回各目标的资源描述（含深度）。
func (l GBufferLayout) Descs() map[string]graph.ResourceDesc {
	d := make(map[string]graph.ResourceDesc, len(l.Targets)+1)
	for i, f := range l.Targets {
		d[fmt.Sprintf("gbuffer%d", i)] = graph.ResourceDesc{Width: l.Width, Height: l.Height, Format: f}
	}
	d[l.DepthName()] = graph.ResourceDesc{Width: l.Width, Height: l.Height, Format: graph.FormatR32F}
	return d
}
