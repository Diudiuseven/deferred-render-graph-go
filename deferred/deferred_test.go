package deferred

import (
	"context"
	"errors"
	"testing"

	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

func testScene() *Scene {
	return &Scene{
		Camera:  Camera{Origin: Vec3{0, 0, 0}, FovScale: 1},
		Ambient: Vec3{0.05, 0.04, 0.06},
		Spheres: []Sphere{
			{Center: Vec3{-0.8, 0, -4}, Radius: 1, Albedo: Vec3{0.8, 0.2, 0.2}, Shininess: 32},
			{Center: Vec3{0.9, 0.2, -5}, Radius: 1.2, Albedo: Vec3{0.2, 0.7, 0.3}, Shininess: 8},
			{Center: Vec3{0, -1.5, -6}, Radius: 1.5, Albedo: Vec3{0.3, 0.3, 0.8}, Shininess: 64},
		},
		Lights: []Light{
			{ID: 3, Position: Vec3{0, 3, -6}, Color: Vec3{0.8, 0.4, 0.2}, Radius: 6},
			{ID: 1, Position: Vec3{-3, 1, -3}, Color: Vec3{0.4, 0.6, 1}, Radius: 10},
			{ID: 2, Position: Vec3{2, 2, -2}, Color: Vec3{1, 0.9, 0.8}, Radius: 12},
			{ID: 4, Position: Vec3{5, -2, -8}, Color: Vec3{0.2, 0.9, 0.5}, Radius: 3},
		},
	}
}

func render(t *testing.T, cfg Config) *rendergraph.Image {
	t.Helper()
	p := New(cfg, testScene())
	img, err := p.Render(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return img
}

func pixelsEqual(a, b *rendergraph.Image) bool {
	if a.Desc != b.Desc || len(a.Pixels) != len(b.Pixels) {
		return false
	}
	for i := range a.Pixels {
		if a.Pixels[i] != b.Pixels[i] {
			return false
		}
	}
	return true
}

// 分块光照结果必须与逐像素处理逐位一致（不同块尺寸、不同并行度）。
func TestTiledMatchesPerPixel(t *testing.T) {
	base := Config{Width: 48, Height: 32, TileSize: 0}
	ref := render(t, base)
	for _, tile := range []int{1, 5, 16, 48} {
		for _, workers := range []int{1, 4} {
			cfg := base
			cfg.TileSize = tile
			cfg.TileWorkers = workers
			got := render(t, cfg)
			if !pixelsEqual(ref, got) {
				t.Fatalf("tileSize=%d workers=%d differs from per-pixel result", tile, workers)
			}
		}
	}
}

// 相同场景与配置多次渲染输出必须一致。
func TestDeterministicAcrossRuns(t *testing.T) {
	cfg := Config{Width: 48, Height: 32, TileSize: 8, TileWorkers: 4, PassConcurrency: 2}
	a := render(t, cfg)
	b := render(t, cfg)
	if !pixelsEqual(a, b) {
		t.Fatal("same config rendered different results")
	}
}

// 管线变更：只改分块大小 -> 仅光照通道重执行；改 GBuffer 布局 -> 几何与光照都重执行。
func TestReconfigureOnlyRerunsAffected(t *testing.T) {
	cfg := Config{Width: 32, Height: 24, TileSize: 8}
	p := New(cfg, testScene())
	img1, err := p.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.LastReport().Executed; len(got) != 2 {
		t.Fatalf("first render should execute both passes, got %v", got)
	}

	// 无变化再渲染：不应有任何通道重执行，输出不变
	img2, err := p.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.LastReport().Executed; len(got) != 0 {
		t.Fatalf("unchanged pipeline should execute nothing, got %v", got)
	}
	if !pixelsEqual(img1, img2) {
		t.Fatal("unchanged pipeline produced different output")
	}

	// 只改分块大小：仅光照通道受影响
	cfg.TileSize = 4
	p.Reconfigure(cfg)
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := p.LastReport().Executed
	if len(got) != 1 || got[0] != "lighting" {
		t.Fatalf("tile-size change should only rerun lighting, got %v", got)
	}

	// 改 GBuffer 布局：几何与光照都受影响
	cfg.Layout = GBufferLayout{Targets: []TargetSpec{
		{Kind: TargetAlbedo, Format: rendergraph.FormatRGBA16F},
		{Kind: TargetNormal, Format: rendergraph.FormatRGBA32F},
		{Kind: TargetDepth, Format: rendergraph.FormatR32F},
		{Kind: TargetMaterial, Format: rendergraph.FormatRGBA32F},
	}}
	p.Reconfigure(cfg)
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.LastReport().Executed; len(got) != 2 {
		t.Fatalf("layout change should rerun both passes, got %v", got)
	}
}

// GBuffer 数量与格式可配置。
func TestConfigurableGBufferLayout(t *testing.T) {
	layout := GBufferLayout{Targets: []TargetSpec{
		{Kind: TargetAlbedo, Format: rendergraph.FormatRGBA8},
		{Kind: TargetNormal, Format: rendergraph.FormatRGBA16F},
	}}
	cfg := Config{Width: 16, Height: 16, Layout: layout}
	img := render(t, cfg)
	if img.Desc.Width != 16 || img.Desc.Height != 16 {
		t.Fatalf("unexpected output desc: %+v", img.Desc)
	}
}

// 资源不足必须给出可区分的失败而不是崩溃。
func TestBudgetExhaustionIsDistinguishable(t *testing.T) {
	cfg := Config{Width: 64, Height: 64, BudgetBytes: 1024} // 远小于需求
	p := New(cfg, testScene())
	_, err := p.Render(context.Background())
	if !errors.Is(err, rendergraph.ErrResourceExhausted) {
		t.Fatalf("expected ErrResourceExhausted, got %v", err)
	}
}

// 长时间运行内存不得持续增长。
func TestMemoryStableOverManyFrames(t *testing.T) {
	cfg := Config{Width: 32, Height: 24, TileSize: 8}
	p := New(cfg, testScene())
	var firstHighWater int
	for i := 0; i < 20; i++ {
		if _, err := p.Render(context.Background()); err != nil {
			t.Fatal(err)
		}
		if i == 1 { // 第二帧起进入稳态
			firstHighWater = p.Pool().HighWater()
		}
	}
	if p.Pool().HighWater() != firstHighWater {
		t.Fatalf("memory grew over frames: %d -> %d", firstHighWater, p.Pool().HighWater())
	}
}

// 导出调度与分配计划可用于比对，且多次导出一致。
func TestExportPlanStable(t *testing.T) {
	cfg := Config{Width: 32, Height: 24, TileSize: 8}
	p := New(cfg, testScene())
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	e1, err := p.Compiled().ExportPlan()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	e2, err := p.Compiled().ExportPlan()
	if err != nil {
		t.Fatal(err)
	}
	if string(e1) != string(e2) {
		t.Fatal("export plan changed between identical runs")
	}
}

// 运行中增删通道：删除光照通道后渲染必须报错（颜色缺失），
// 增加新通道后依赖重新解析且被正确执行。
func TestPipelineMutation(t *testing.T) {
	cfg := Config{Width: 16, Height: 16}
	p := New(cfg, testScene())
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 删除光照通道 -> 颜色资源无人产出，必须报错而非静默产出错误画面
	p.Graph().RemovePass("lighting")
	if _, err := p.Render(context.Background()); err == nil {
		t.Fatal("expected error after removing lighting pass")
	}

	// 重新加入光照通道（带一个额外的调试输出通道）
	p.Reconfigure(cfg)
	extra := rendergraph.ResourceID("debug.copy")
	p.Graph().DeclareResource(rendergraph.ResourceDesc{
		Name: extra, Format: rendergraph.FormatRGBA32F, Width: 16, Height: 16,
	})
	executed := false
	if err := p.Graph().AddPass(&rendergraph.Pass{
		Name:      "debugcopy",
		Reads:     []rendergraph.ResourceID{ColorResource},
		Writes:    []rendergraph.ResourceID{extra},
		ConfigKey: "debugcopy-v1",
		Execute: func(ctx *rendergraph.ExecContext) error {
			src := ctx.Get(ColorResource)
			dst := ctx.Get(extra)
			copy(dst.Pixels, src.Pixels)
			executed = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("added pass was not executed")
	}
}

// 光源在场景中的给出顺序不得影响最终颜色（按 ID 固定累加顺序）。
func TestLightOrderIrrelevant(t *testing.T) {
	cfg := Config{Width: 32, Height: 24, TileSize: 8, TileWorkers: 2}
	s1 := testScene()
	p1 := New(cfg, s1)
	img1, err := p1.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 反转光源顺序
	s2 := testScene()
	for i, j := 0, len(s2.Lights)-1; i < j; i, j = i+1, j-1 {
		s2.Lights[i], s2.Lights[j] = s2.Lights[j], s2.Lights[i]
	}
	p2 := New(cfg, s2)
	img2, err := p2.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !pixelsEqual(img1, img2) {
		t.Fatal("light slice order changed the final image")
	}
}
