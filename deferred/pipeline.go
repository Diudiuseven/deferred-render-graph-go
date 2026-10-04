package deferred

import (
	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

// OutputName 是最终颜色资源名。
const OutputName = "finalColor"

// Pipeline 组装延迟着色渲染图：几何阶段 + 光照阶段。
type Pipeline struct {
	Graph  *graph.Graph
	Layout GBufferLayout
	Light  LightingOptions
	Output string // 最终颜色资源名
}

// NewPipeline 创建标准延迟着色管线。
func NewPipeline(scene *Scene, layout GBufferLayout, opts LightingOptions) *Pipeline {
	g := graph.NewGraph()
	for name, desc := range layout.Descs() {
		g.DeclareResource(name, desc)
	}
	g.DeclareResource(OutputName, graph.ResourceDesc{
		Width: layout.Width, Height: layout.Height, Format: graph.FormatRGB32F,
	})
	g.AddPass(GeometryPass(scene, layout))
	g.AddPass(LightingPass(scene, layout, opts, OutputName))
	return &Pipeline{
		Graph:  g,
		Layout: layout,
		Light:  opts,
		Output: OutputName,
	}
}

// Compile 编译管线（依赖解析 + 调度计划）。
func (p *Pipeline) Compile() (*graph.Compiled, error) {
	return p.Graph.Compile()
}
