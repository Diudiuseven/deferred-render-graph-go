// demo 渲染一帧延迟着色画面，导出 PPM 图像与调度轨迹。
package main

import (
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/Diudiuseven/deferred-render-graph-go/deferred"
	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

func main() {
	w := flag.Int("w", 160, "图像宽度")
	h := flag.Int("h", 120, "图像高度")
	tile := flag.Int("tile", 16, "光照分块大小（<=1 表示逐像素）")
	workers := flag.Int("workers", 4, "分块并行度")
	par := flag.Int("par", 4, "通道并发上限")
	out := flag.String("o", "out.ppm", "输出 PPM 路径")
	traceOut := flag.String("trace", "trace.txt", "调度轨迹输出路径")
	flag.Parse()

	scene := &deferred.Scene{
		Materials: []deferred.Material{
			{Albedo: deferred.Vec3{X: 0.8, Y: 0.3, Z: 0.2}, Specular: 0.6},
			{Albedo: deferred.Vec3{X: 0.2, Y: 0.6, Z: 0.8}, Specular: 0.3},
		},
		Camera: deferred.Camera{
			Position: deferred.Vec3{X: 0, Y: 2, Z: 6},
			Target:   deferred.Vec3{X: 0, Y: 0.5, Z: 0},
			Up:       deferred.Vec3{X: 0, Y: 1, Z: 0},
			FovY:     float32(math.Pi) / 3,
			Near:     0.1, Far: 100,
		},
	}
	scene.Triangles = append(scene.Triangles,
		deferred.Triangle{V0: deferred.Vec3{X: -10, Y: 0, Z: -10}, V1: deferred.Vec3{X: 10, Y: 0, Z: -10}, V2: deferred.Vec3{X: 10, Y: 0, Z: 10}, Material: 0},
		deferred.Triangle{V0: deferred.Vec3{X: -10, Y: 0, Z: -10}, V1: deferred.Vec3{X: 10, Y: 0, Z: 10}, V2: deferred.Vec3{X: -10, Y: 0, Z: 10}, Material: 0},
		deferred.Triangle{V0: deferred.Vec3{X: -1.5, Y: 0, Z: 0}, V1: deferred.Vec3{X: 1.5, Y: 0, Z: 0}, V2: deferred.Vec3{X: 0, Y: 2.5, Z: 0}, Material: 1},
	)
	scene.Lights = []deferred.Light{
		{ID: 1, Position: deferred.Vec3{X: -3, Y: 3, Z: 2}, Color: deferred.Vec3{X: 0.6, Y: 0.7, Z: 1}, Radius: 12},
		{ID: 2, Position: deferred.Vec3{X: 3, Y: 2, Z: 3}, Color: deferred.Vec3{X: 1, Y: 0.8, Z: 0.6}, Radius: 10},
		{ID: 3, Position: deferred.Vec3{X: 0, Y: 4, Z: -3}, Color: deferred.Vec3{X: 0.7, Y: 1, Z: 0.7}, Radius: 14},
	}

	layout := deferred.FullLayout(*w, *h)
	p := deferred.NewPipeline(scene, layout, deferred.LightingOptions{
		TileSize: *tile, TileWorkers: *workers, Ambient: 0.06,
	})
	compiled, err := p.Compile()
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}
	sched := graph.NewScheduler(graph.SchedulerOptions{MaxParallelPasses: *par})
	_, trace, buffers, err := sched.Run(compiled, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}

	// 导出调度轨迹（调度顺序 + 资源分配结果）。
	f, err := os.Create(*traceOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, e := range trace.Events {
		fmt.Fprintf(f, "pass %-12s level=%d kind=%s\n", e.Pass, e.Level, e.Kind)
	}
	for _, a := range trace.Allocations {
		fmt.Fprintf(f, "alloc %-16s level=%d source=%s bytes=%d\n", a.Resource, a.Level, a.Source, a.Bytes)
	}
	f.Close()

	// 导出 PPM。
	img := buffers[p.Output]
	pf, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pf.Close()
	fmt.Fprintf(pf, "P6\n%d %d\n255\n", *w, *h)
	buf := make([]byte, 0, *w**h*3)
	for i := 0; i < *w**h; i++ {
		for c := 0; c < 3; c++ {
			v := img.Data[i*3+c]
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			// 简单 gamma。
			buf = append(buf, byte(math.Sqrt(float64(v))*255+0.5))
		}
	}
	pf.Write(buf)
	fmt.Printf("wrote %s and %s\n", *out, *traceOut)
}
