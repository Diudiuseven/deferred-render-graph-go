package rendergraph

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CycleError 表示检测到循环依赖，Path 给出构成环的通道名（可定位）。
type CycleError struct {
	Path []string
}

func (e *CycleError) Error() string {
	return "rendergraph: dependency cycle detected: " + strings.Join(e.Path, " -> ")
}

// Graph 是渲染图，包含通道与资源声明。
type Graph struct {
	passes    map[string]*Pass
	passOrder []string
	resources map[ResourceID]ResourceDesc
}

// New 创建空渲染图。
func New() *Graph {
	return &Graph{
		passes:    map[string]*Pass{},
		resources: map[ResourceID]ResourceDesc{},
	}
}

// DeclareResource 声明一份中间结果。
func (g *Graph) DeclareResource(d ResourceDesc) {
	g.resources[d.Name] = d
}

// Resource 返回资源描述。
func (g *Graph) Resource(id ResourceID) (ResourceDesc, bool) {
	d, ok := g.resources[id]
	return d, ok
}

// Resources 按名称排序返回全部资源描述（确定性顺序）。
func (g *Graph) Resources() []ResourceDesc {
	names := make([]string, 0, len(g.resources))
	for id := range g.resources {
		names = append(names, string(id))
	}
	sort.Strings(names)
	out := make([]ResourceDesc, 0, len(names))
	for _, n := range names {
		out = append(out, g.resources[ResourceID(n)])
	}
	return out
}

// AddPass 添加通道；同名覆盖（用于管线变更）。
func (g *Graph) AddPass(p *Pass) error {
	if p.Name == "" {
		return errors.New("rendergraph: pass name must not be empty")
	}
	if _, exists := g.passes[p.Name]; !exists {
		g.passOrder = append(g.passOrder, p.Name)
	}
	g.passes[p.Name] = p
	return nil
}

// RemovePass 移除通道。
func (g *Graph) RemovePass(name string) {
	if _, ok := g.passes[name]; !ok {
		return
	}
	delete(g.passes, name)
	for i, n := range g.passOrder {
		if n == name {
			g.passOrder = append(g.passOrder[:i], g.passOrder[i+1:]...)
			break
		}
	}
}

// Pass 返回指定名称的通道。
func (g *Graph) Pass(name string) *Pass { return g.passes[name] }

// Passes 按添加顺序返回全部通道。
func (g *Graph) Passes() []*Pass {
	out := make([]*Pass, 0, len(g.passOrder))
	for _, n := range g.passOrder {
		out = append(out, g.passes[n])
	}
	return out
}

// validate 检查通道引用的资源是否均已声明。
func (g *Graph) validate() error {
	for _, p := range g.Passes() {
		for _, id := range append(append([]ResourceID{}, p.Reads...), p.Writes...) {
			if _, ok := g.resources[id]; !ok {
				return fmt.Errorf("%w: pass %q references %q", ErrUnknownResource, p.Name, id)
			}
		}
	}
	return nil
}

// ErrUnknownResource 表示通道引用了未声明的资源。
var ErrUnknownResource = errors.New("rendergraph: unknown resource")
