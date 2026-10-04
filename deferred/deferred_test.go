package deferred

import (
	"math"
	"testing"

	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

// 构造一个确定性的测试场景：一个三角形地面 + 若干点光源。
func testScene() *Scene {
	s := &Scene{
		Materials: []Material{
			{Albedo: Vec3{0.8, 0.3, 0.2}, Specular: 0.6},
			{Albedo: Vec3{0.2, 0.6, 0.8}, Specular: 0.3},
		},
		Camera: Camera{
			Position: Vec3{0, 2, 6},
			Target:   Vec3{0, 0, 0},
			Up:       Vec3{0, 1, 0},
			FovY:     float32(math.Pi) / 3,
			Near:     0.1,
			Far:      100,
		},
	}
	// 地面（两个大三角形）。
	s.Triangles = append(s.Triangles,
		Triangle{Vec3{-10, 0, -10}, Vec3{10, 0, -10}, Vec3{10, 0, 10}, 0},
		Triangle{Vec3{-10, 0, -10}, Vec3{10, 0, 10}, Vec3{-10, 0, 10}, 0},
		// 一个立起的三角形。
		Triangle{Vec3{-1, 0, 0}, Vec3{1, 0, 0}, Vec3{0, 2, 0}, 1},
	)
	// 多个光源，ID 故意乱序。
	s.Lights = []Light{
		{ID: 3, Position: Vec3{2, 3, 2}, Color: Vec3{1, 0.9, 0.8}, Radius: 20},
		{ID: 1, Position: Vec3{-3, 2, 1}, Color: Vec3{0.5, 0.6, 1}, Radius: 8},
		{ID: 2, Position: Vec3{0, 1, -4}, Color: Vec3{0.8, 1, 0.5}, Radius: 5},
		{ID: 4, Position: Vec3{5, 1, 5}, Color: Vec3{1, 0.3, 0.3}, Radius: 3},
	}
	return s
}

// renderOnce 用给定配置渲染一帧，返回最终颜色缓冲的拷贝。
func renderOnce(t *testing.T, scene *Scene, layout GBufferLayout, opts LightingOptions) []float32 {
	t.Helper()
	p := NewPipeline(scene, layout, opts)
	c, err := p.Compile()
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	s := graph.NewScheduler(graph.SchedulerOptions{MaxParallelPasses: 4})
	_, _, buffers, err := s.Run(c, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buffers[p.Output]
	if out == nil {
		t.Fatal("no output buffer")
	}
	return append([]float32(nil), out.Data...)
}

func buffersEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] { // 逐位比较，要求严格一致
			return false
		}
	}
	return true
}

func TestDeferredShadingProducesImage(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(64, 48)
	img := renderOnce(t, scene, layout, LightingOptions{TileSize: 1, Ambient: 0.05})
	if len(img) != 64*48*3 {
		t.Fatalf("unexpected image size %d", len(img))
	}
	// 画面不应全黑。
	var sum float64
	for _, v := range img {
		sum += float64(v)
	}
	if sum == 0 {
		t.Fatal("image is all black")
	}
}

func TestTiledMatchesPerPixel(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(64, 48)
	ref := renderOnce(t, scene, layout, LightingOptions{TileSize: 1, TileWorkers: 1, Ambient: 0.05})
	for _, ts := range []int{4, 8, 16, 64} {
		for _, workers := range []int{1, 2, 8} {
			got := renderOnce(t, scene, layout, LightingOptions{TileSize: ts, TileWorkers: workers, Ambient: 0.05})
			if !buffersEqual(ref, got) {
				t.Fatalf("tiled (ts=%d, workers=%d) differs from per-pixel", ts, workers)
			}
		}
	}
}

func TestDeterministicAcrossRuns(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(64, 48)
	opts := LightingOptions{TileSize: 8, TileWorkers: 4, Ambient: 0.05}
	first := renderOnce(t, scene, layout, opts)
	for i := 0; i < 5; i++ {
		got := renderOnce(t, scene, layout, opts)
		if !buffersEqual(first, got) {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestLightOrderDoesNotChangeResult(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(64, 48)
	opts := LightingOptions{TileSize: 8, TileWorkers: 2, Ambient: 0.05}
	a := renderOnce(t, scene, layout, opts)
	// 打乱光源顺序（ID 不变），结果必须一致。
	for i, j := 0, len(scene.Lights)-1; i < j; i, j = i+1, j-1 {
		scene.Lights[i], scene.Lights[j] = scene.Lights[j], scene.Lights[i]
	}
	b := renderOnce(t, scene, layout, opts)
	if !buffersEqual(a, b) {
		t.Fatal("light order changed the result")
	}
}

func TestConfigurableGBufferLayouts(t *testing.T) {
	scene := testScene()
	for _, layout := range []GBufferLayout{
		DefaultLayout(32, 32),
		FullLayout(32, 32),
		{Width: 32, Height: 32, Targets: []graph.Format{graph.FormatRGBA32F, graph.FormatRGBA32F, graph.FormatRGBA32F, graph.FormatR32F}},
	} {
		img := renderOnce(t, scene, layout, LightingOptions{TileSize: 8, Ambient: 0.05})
		var sum float64
		for _, v := range img {
			sum += float64(v)
		}
		if sum == 0 {
			t.Fatalf("layout %+v produced black image", layout.Targets)
		}
	}
}

func TestPipelineMutationRebuildsAffected(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(64, 48)
	p := NewPipeline(scene, layout, LightingOptions{TileSize: 8, Ambient: 0.05})
	sched := graph.NewScheduler(graph.SchedulerOptions{})

	c1, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	prev, _, buf1, err := sched.Run(c1, nil)
	if err != nil {
		t.Fatal(err)
	}
	img1 := append([]float32(nil), buf1[p.Output].Data...)

	// 管线变更：环境光改变（替换 lighting 通道配置）。
	p.Light.Ambient = 0.5
	p.Graph.AddPass(LightingPass(scene, layout, p.Light, p.Output))
	c2, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	_, trace, buf2, err := sched.Run(c2, prev)
	if err != nil {
		t.Fatal(err)
	}
	img2 := buf2[p.Output].Data

	// 几何通道未受影响，应被跳过；光照通道应重新执行。
	kinds := map[string]string{}
	for _, e := range trace.Events {
		kinds[e.Pass] = e.Kind
	}
	if kinds["geometry"] != "skip" {
		t.Fatalf("geometry should be skipped, got %v", kinds)
	}
	if kinds["lighting"] != "exec" {
		t.Fatalf("lighting should re-execute, got %v", kinds)
	}
	// 画面应发生变化且仍有效。
	if buffersEqual(img1, img2) {
		t.Fatal("ambient change had no effect")
	}
	var sum float64
	for _, v := range img2 {
		sum += float64(v)
	}
	if sum == 0 {
		t.Fatal("image after mutation is black")
	}
}

func TestAddAndRemovePass(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(32, 32)
	p := NewPipeline(scene, layout, LightingOptions{TileSize: 8, Ambient: 0.05})
	sched := graph.NewScheduler(graph.SchedulerOptions{})

	c1, _ := p.Compile()
	prev, _, _, err := sched.Run(c1, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 增加一个后处理通道：把最终颜色取反写入新资源。
	p.Graph.DeclareResource("inverted", graph.ResourceDesc{Width: 32, Height: 32, Format: graph.FormatRGB32F})
	p.Graph.AddPass(&graph.Pass{
		Name:       "invert",
		Reads:      []string{p.Output},
		Writes:     []string{"inverted"},
		ConfigHash: 42,
		Execute: func(ctx *graph.PassContext) error {
			src := ctx.MustGet(p.Output)
			dst := ctx.MustGet("inverted")
			for i, v := range src.Data {
				dst.Data[i] = 1 - v
			}
			return nil
		},
	})
	c2, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	_, trace, bufs, err := sched.Run(c2, prev)
	if err != nil {
		t.Fatal(err)
	}
	// 只有新通道执行，其余跳过。
	for _, e := range trace.Events {
		want := "skip"
		if e.Pass == "invert" {
			want = "exec"
		}
		if e.Kind != want {
			t.Fatalf("pass %s: want %s got %s", e.Pass, want, e.Kind)
		}
	}
	inv := bufs["inverted"]
	if inv == nil {
		t.Fatal("inverted resource missing")
	}

	// 删除该通道后重新编译，不应再出现。
	p.Graph.RemovePass("invert")
	c3, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, ps := range c3.Order {
		if ps.Name == "invert" {
			t.Fatal("invert pass still scheduled after removal")
		}
	}
}

func TestTraceExportForComparison(t *testing.T) {
	scene := testScene()
	layout := DefaultLayout(32, 32)
	render := func() *graph.Trace {
		p := NewPipeline(scene, layout, LightingOptions{TileSize: 8, TileWorkers: 2, Ambient: 0.05})
		c, _ := p.Compile()
		s := graph.NewScheduler(graph.SchedulerOptions{MaxParallelPasses: 2})
		_, trace, _, err := s.Run(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}
	t1 := render()
	t2 := render()
	if len(t1.Events) != len(t2.Events) || len(t1.Allocations) != len(t2.Allocations) {
		t.Fatal("trace mismatch")
	}
	for i := range t1.Events {
		if t1.Events[i] != t2.Events[i] {
			t.Fatalf("trace event %d differs", i)
		}
	}
	for i := range t1.Allocations {
		if t1.Allocations[i] != t2.Allocations[i] {
			t.Fatalf("alloc %d differs", i)
		}
	}
}
