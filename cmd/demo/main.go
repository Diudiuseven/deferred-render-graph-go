package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"os"

	"github.com/Diudiuseven/deferred-render-graph-go/deferred"
	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

func v(x, y, z float32) deferred.Vec3 { return deferred.Vec3{X: x, Y: y, Z: z} }

func demoScene() *deferred.Scene {
	return &deferred.Scene{
		Camera:  deferred.Camera{Origin: v(0, 0, 0), FovScale: 1},
		Ambient: v(0.05, 0.05, 0.06),
		Spheres: []deferred.Sphere{
			{Center: v(-0.8, 0, -4), Radius: 1, Albedo: v(0.8, 0.2, 0.2), Shininess: 32},
			{Center: v(0.9, 0.2, -5), Radius: 1.2, Albedo: v(0.2, 0.7, 0.3), Shininess: 8},
			{Center: v(0, -1.5, -6), Radius: 1.5, Albedo: v(0.3, 0.3, 0.8), Shininess: 64},
		},
		Lights: []deferred.Light{
			{ID: 2, Position: v(2, 2, -2), Color: v(1, 0.9, 0.8), Radius: 12},
			{ID: 1, Position: v(-3, 1, -3), Color: v(0.4, 0.6, 1), Radius: 10},
			{ID: 3, Position: v(0, 3, -6), Color: v(0.8, 0.4, 0.2), Radius: 6},
		},
	}
}

func checksum(img *rendergraph.Image) uint64 {
	h := fnv.New64a()
	for _, val := range img.Pixels {
		var b [4]byte
		u := math.Float32bits(val)
		b[0], b[1], b[2], b[3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
		h.Write(b[:])
	}
	return h.Sum64()
}

func main() {
	scene := demoScene()
	cfg := deferred.Config{
		Width: 64, Height: 48,
		TileSize: 16, TileWorkers: 4, PassConcurrency: 2,
	}
	p := deferred.New(cfg, scene)
	img, err := p.Render(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "render failed:", err)
		os.Exit(1)
	}
	fmt.Printf("rendered %dx%d, checksum=%016x\n", img.Desc.Width, img.Desc.Height, checksum(img))
	fmt.Printf("executed passes: %v\n", p.LastReport().Executed)

	// 再渲染一次验证确定性与增量执行
	img2, err := p.Render(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "render failed:", err)
		os.Exit(1)
	}
	fmt.Printf("second render checksum=%016x (deterministic=%v), executed=%v skipped=%v\n",
		checksum(img2), checksum(img) == checksum(img2), p.LastReport().Executed, p.LastReport().Skipped)

	// 切换分块大小：只有光照通道重执行
	cfg.TileSize = 8
	p.Reconfigure(cfg)
	if _, err := p.Render(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "render failed:", err)
		os.Exit(1)
	}
	fmt.Printf("after reconfigure(tileSize=8): executed=%v\n", p.LastReport().Executed)

	plan, err := p.Compiled().ExportPlan()
	if err != nil {
		fmt.Fprintln(os.Stderr, "export failed:", err)
		os.Exit(1)
	}
	fmt.Printf("schedule plan:\n%s\n", plan)
}
