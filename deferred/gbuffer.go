package deferred

import (
	"fmt"

	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

// TargetKind GBuffer 目标的语义。
type TargetKind int

const (
	TargetAlbedo TargetKind = iota
	TargetNormal
	TargetDepth
	TargetMaterial // shininess 等材质参数
)

func (k TargetKind) String() string {
	switch k {
	case TargetAlbedo:
		return "albedo"
	case TargetNormal:
		return "normal"
	case TargetDepth:
		return "depth"
	case TargetMaterial:
		return "material"
	}
	return fmt.Sprintf("kind%d", int(k))
}

// TargetSpec 描述一个 GBuffer 目标。
type TargetSpec struct {
	Kind   TargetKind
	Format rendergraph.Format
}

// GBufferLayout 可配置的 GBuffer 布局（数量与格式）。
type GBufferLayout struct {
	Targets []TargetSpec
}

// DefaultLayout 默认四目标布局。
func DefaultLayout() GBufferLayout {
	return GBufferLayout{Targets: []TargetSpec{
		{Kind: TargetAlbedo, Format: rendergraph.FormatRGBA32F},
		{Kind: TargetNormal, Format: rendergraph.FormatRGBA32F},
		{Kind: TargetDepth, Format: rendergraph.FormatR32F},
		{Kind: TargetMaterial, Format: rendergraph.FormatRGBA32F},
	}}
}

// ResourceName 返回目标对应的资源名。
func ResourceName(k TargetKind) rendergraph.ResourceID {
	return rendergraph.ResourceID("gbuffer." + k.String())
}

// Has 判断布局是否包含某类目标。
func (l GBufferLayout) Has(k TargetKind) bool {
	for _, t := range l.Targets {
		if t.Kind == k {
			return true
		}
	}
	return false
}

// Key 返回布局的稳定标识，用于配置变更检测。
func (l GBufferLayout) Key() string {
	s := ""
	for _, t := range l.Targets {
		s += fmt.Sprintf("%d:%d;", t.Kind, t.Format)
	}
	return s
}
