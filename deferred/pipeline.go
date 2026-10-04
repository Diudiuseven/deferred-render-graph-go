package deferred

import (
	"context"
	"fmt"

	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

// Config 管线配置。
type Config struct {
	Width, Height   int
	Layout          GBufferLayout
	TileSize        int // 分块光照的块边长（像素），0 表示逐像素不分块
	TileWorkers     int // 分块并行度，<=0 表示 1
	PassConcurrency int // 渲染图通道并发上限，<=0 表示不限制
	BudgetBytes     int // 内存池预算，0 不限
}

// Pipeline 延迟着色管线。
type Pipeline struct {
	cfg      Config
	scene    *Scene
	graph    *rendergraph.Graph
	compiled *rendergraph.Compiled
	kept     map[rendergraph.ResourceID]*rendergraph.Image
	pool     *rendergraph.Pool
	lastRep  *rendergraph.RunReport
}

// New 创建管线。
func New(cfg Config, scene *Scene) *Pipeline {
	if cfg.Layout.Targets == nil {
		cfg.Layout = DefaultLayout()
	}
	p := &Pipeline{
		cfg:   cfg,
		scene: scene,
		pool:  rendergraph.NewPool(cfg.BudgetBytes),
	}
	p.rebuild()
	return p
}

// rebuild 按当前配置重建渲染图（资源声明 + 通道）。
// 旧的 compiled 保留用于脏通道比对；kept 资源由下一次 Run 回收或复用。
func (p *Pipeline) rebuild() {
	cfg := p.cfg
	g := rendergraph.New()
	var gbufIDs []rendergraph.ResourceID
	for _, t := range cfg.Layout.Targets {
		id := ResourceName(t.Kind)
		g.DeclareResource(rendergraph.ResourceDesc{
			Name: id, Format: t.Format, Width: cfg.Width, Height: cfg.Height,
		})
		gbufIDs = append(gbufIDs, id)
	}
	g.DeclareResource(rendergraph.ResourceDesc{
		Name: ColorResource, Format: rendergraph.FormatRGBA32F, Width: cfg.Width, Height: cfg.Height,
	})

	scene := p.scene
	layout := cfg.Layout
	geoKey := fmt.Sprintf("geo|%dx%d|%s", cfg.Width, cfg.Height, layout.Key())
	if err := g.AddPass(&rendergraph.Pass{
		Name:      "geometry",
		Writes:    gbufIDs,
		ConfigKey: geoKey,
		Execute: func(ctx *rendergraph.ExecContext) error {
			renderGeometry(scene, layout, cfg.Width, cfg.Height, ctx)
			return nil
		},
	}); err != nil {
		panic(err)
	}
	lightKey := fmt.Sprintf("light|%dx%d|%s|tile=%d|workers=%d",
		cfg.Width, cfg.Height, layout.Key(), cfg.TileSize, cfg.TileWorkers)
	reads := append([]rendergraph.ResourceID(nil), gbufIDs...)
	if err := g.AddPass(&rendergraph.Pass{
		Name:      "lighting",
		Reads:     reads,
		Writes:    []rendergraph.ResourceID{ColorResource},
		ConfigKey: lightKey,
		Execute: func(ctx *rendergraph.ExecContext) error {
			renderLighting(scene, layout, cfg.Width, cfg.Height, cfg.TileSize, cfg.TileWorkers, ctx)
			return nil
		},
	}); err != nil {
		panic(err)
	}
	p.graph = g
}

// Render 渲染一帧，返回最终颜色图（调用方持有的副本）。
// 资源不足时返回包装了 rendergraph.ErrResourceExhausted 的错误。
func (p *Pipeline) Render(ctx context.Context) (*rendergraph.Image, error) {
	compiled, err := p.graph.Compile()
	if err != nil {
		return nil, err
	}
	// 颜色资源必须有产出者，否则报错而非静默复用旧画面
	hasProducer := false
	for _, pass := range p.graph.Passes() {
		for _, id := range pass.Writes {
			if id == ColorResource {
				hasProducer = true
			}
		}
	}
	if !hasProducer {
		return nil, fmt.Errorf("deferred: no pass writes %q", ColorResource)
	}
	dirty := compiled.DirtyPasses(p.compiled)
	dirtySet := make(map[string]bool, len(dirty))
	for _, name := range dirty {
		dirtySet[name] = true
	}
	// 保留所有仍被图中通道产出的资源，供后续增量渲染复用
	keep := map[rendergraph.ResourceID]bool{}
	for _, id := range compiled.SortedResourceIDs() {
		keep[id] = true
	}
	report, err := compiled.Run(ctx, rendergraph.RunOptions{
		MaxConcurrency: p.cfg.PassConcurrency,
		Pool:           p.pool,
		OnlyDirty:      true,
		Dirty:          dirtySet,
		Kept:           p.kept,
		Keep:           keep,
	})
	if err != nil {
		return nil, err
	}
	p.kept = report.Images
	p.compiled = compiled
	p.lastRep = report
	color := report.Images[ColorResource]
	if color == nil {
		return nil, fmt.Errorf("deferred: color resource missing after run")
	}
	// 返回副本，内部图像继续由池管理
	out := rendergraph.NewImage(color.Desc)
	copy(out.Pixels, color.Pixels)
	return out, nil
}

// Reconfigure 修改配置并重新解析依赖；只重执行受影响的通道。
func (p *Pipeline) Reconfigure(cfg Config) {
	if cfg.Layout.Targets == nil {
		cfg.Layout = DefaultLayout()
	}
	p.cfg = cfg
	p.pool.BudgetBytes = cfg.BudgetBytes
	p.rebuild()
}

// Graph 返回内部渲染图（用于测试与导出）。
func (p *Pipeline) Graph() *rendergraph.Graph { return p.graph }

// Compiled 返回最近一次编译结果（用于导出调度计划）。
func (p *Pipeline) Compiled() *rendergraph.Compiled { return p.compiled }

// LastReport 返回最近一次执行的报告。
func (p *Pipeline) LastReport() *rendergraph.RunReport { return p.lastRep }

// Pool 返回内存池（用于测试内存复用）。
func (p *Pipeline) Pool() *rendergraph.Pool { return p.pool }
