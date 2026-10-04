package deferred

import (
	"math"
	"sort"
	"sync"

	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

// LightingOptions 控制光照阶段。
type LightingOptions struct {
	// TileSize 为屏幕分块边长（像素），<=1 表示逐像素处理（不分块）。
	TileSize int
	// TileWorkers 为分块并行度，<=1 表示串行。
	TileWorkers int
	// Ambient 为环境光强度。
	Ambient float32
}

// LightingPass 基于 G-Buffer 完成着色，写出最终颜色。
//
// 确定性保证：每个像素的光源按 Light.ID 升序累加；
// 分块模式下每块只处理影响该块的光源，不影响的光源贡献恰为 0，
// 因此分块拼接结果与逐像素处理逐位一致，与分块大小、并行度无关。
func LightingPass(scene *Scene, layout GBufferLayout, opts LightingOptions, output string) *graph.Pass {
	reads := append(append([]string{}, layout.TargetNames()...), layout.DepthName())
	// 光源按 ID 排序，固定累加顺序。
	lights := append([]Light(nil), scene.Lights...)
	sort.Slice(lights, func(i, j int) bool { return lights[i].ID < lights[j].ID })

	return &graph.Pass{
		Name:   "lighting",
		Reads:  reads,
		Writes: []string{output},
		ConfigHash: hashUint64(0x51ed27b9,
			uint64(layout.Width), uint64(layout.Height),
			uint64(opts.TileSize), uint64(opts.TileWorkers),
			uint64(opts.Ambient*1000), uint64(len(lights))),
		Execute: func(ctx *graph.PassContext) error {
			gb := make([]*graph.Buffer, len(layout.Targets))
			for i, n := range layout.TargetNames() {
				gb[i] = ctx.MustGet(n)
			}
			out := ctx.MustGet(output)
			depth := ctx.MustGet(layout.DepthName())
			shade(scene, lights, layout, gb, out, opts, depth)
			return nil
		},
	}
}

// shade 执行光照。分块与逐像素共用同一个像素着色函数，
// 且对每个像素只累加“影响该像素”的光源（其余贡献恰为 0），
// 因此两种路径的结果完全一致。
func shade(scene *Scene, lights []Light, layout GBufferLayout, gb []*graph.Buffer, out *graph.Buffer, opts LightingOptions, depth *graph.Buffer) {
	w, h := layout.Width, layout.Height

	// 预先为每个像素计算影响它的光源列表开销过大；
	// 改为：分块模式下按块求光源包围盒交集，块内像素共享候选列表，
	// 像素级仍逐个判断贡献是否为 0 并跳过。
	if opts.TileSize > 1 {
		shadeTiled(scene, lights, layout, gb, out, opts, depth)
		return
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			shadePixel(scene, lights, gb, out, x, y, w, opts.Ambient)
		}
	}
}

// tile 是一个屏幕分块。
type tile struct{ x0, y0, x1, y1 int }

func shadeTiled(scene *Scene, lights []Light, layout GBufferLayout, gb []*graph.Buffer, out *graph.Buffer, opts LightingOptions, depth *graph.Buffer) {
	w, h := layout.Width, layout.Height
	ts := opts.TileSize
	var tiles []tile
	for y := 0; y < h; y += ts {
		for x := 0; x < w; x += ts {
			tiles = append(tiles, tile{x, y, mini(x+ts, w), mini(y+ts, h)})
		}
	}

	workers := opts.TileWorkers
	if workers <= 1 {
		for _, t := range tiles {
			shadeTile(scene, lights, gb, out, t, w, opts.Ambient, depth)
		}
		return
	}
	// 并行处理分块；各块写入互不相交的像素区域，结果与顺序无关。
	jobs := make(chan tile, len(tiles))
	for _, t := range tiles {
		jobs <- t
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				shadeTile(scene, lights, gb, out, t, w, opts.Ambient, depth)
			}
		}()
	}
	wg.Wait()
}

// shadeTile 处理一个分块：先根据块内深度范围做保守的光源剔除，
// 再逐像素着色。被剔除的光源对块内任何像素的贡献都恰为 0，
// 因此分块拼接结果与逐像素处理完全一致。
func shadeTile(scene *Scene, lights []Light, gb []*graph.Buffer, out *graph.Buffer, t tile, width int, ambient float32, depth *graph.Buffer) {
	// 1. 计算块内有效像素的深度范围（忽略未覆盖像素）。
	h := depth.Desc.Height
	zmin, zmax := float32(1e30), float32(-1e30)
	for y := t.y0; y < t.y1; y++ {
		for x := t.x0; x < t.x1; x++ {
			z := depth.Data[y*width+x]
			if z >= 1e29 { // 未覆盖
				continue
			}
			if z < zmin {
				zmin = z
			}
			if z > zmax {
				zmax = z
			}
		}
	}
	var cand []Light
	if zmin <= zmax {
		// 2. 把块的四个角在两个极端深度上反投影到世界空间，
		//    得到保守包围盒；与光源球求交，不相交则剔除。
		box := tileWorldBounds(scene.Camera, t, width, h, zmin, zmax)
		for _, l := range lights {
			if l.Radius <= 0 || box.distanceTo(l.Position) <= l.Radius {
				cand = append(cand, l)
			}
		}
	}
	// 3. 块内逐像素着色（候选为空时循环体不执行，等价于全部贡献为 0）。
	for y := t.y0; y < t.y1; y++ {
		for x := t.x0; x < t.x1; x++ {
			shadePixel(scene, cand, gb, out, x, y, width, ambient)
		}
	}
}

// aabb 是轴对齐包围盒。
type aabb struct{ min, max Vec3 }

func (b aabb) distanceTo(p Vec3) float32 {
	var d float32
	axes := [3]struct{ p, lo, hi float32 }{
		{p.X, b.min.X, b.max.X},
		{p.Y, b.min.Y, b.max.Y},
		{p.Z, b.min.Z, b.max.Z},
	}
	var sq float32
	for _, a := range axes {
		d = 0
		if a.p < a.lo {
			d = a.lo - a.p
		} else if a.p > a.hi {
			d = a.p - a.hi
		}
		sq += d * d
	}
	return sqrtf(sq)
}

// tileWorldBounds 反投影分块角落到世界空间，返回保守包围盒。
func tileWorldBounds(cam Camera, t tile, width, height int, zmin, zmax float32) aabb {
	aspect := float32(width) / float32(height)
	tanHalf := tanf(cam.FovY / 2)
	a := cam.Far / (cam.Near - cam.Far)
	b := cam.Near * cam.Far / (cam.Near - cam.Far)
	view := LookAt(cam.Position, cam.Target, cam.Up)

	unproject := func(sx, sy, ndcZ float32) Vec3 {
		ndcX := (sx/float32(width))*2 - 1
		ndcY := 1 - (sy/float32(height))*2
		zv := -b / (ndcZ + a)
		xv := ndcX * (-zv) * aspect * tanHalf
		yv := ndcY * (-zv) * tanHalf
		// 视图空间 -> 世界空间（刚体变换求逆：R^T）。
		return Vec3{
			view[0]*xv + view[4]*yv + view[8]*zv + cam.Position.X,
			view[1]*xv + view[5]*yv + view[9]*zv + cam.Position.Y,
			view[2]*xv + view[6]*yv + view[10]*zv + cam.Position.Z,
		}
	}
	corners := [4][2]float32{
		{float32(t.x0), float32(t.y0)},
		{float32(t.x1), float32(t.y0)},
		{float32(t.x0), float32(t.y1)},
		{float32(t.x1), float32(t.y1)},
	}
	box := aabb{min: Vec3{1e30, 1e30, 1e30}, max: Vec3{-1e30, -1e30, -1e30}}
	for _, c := range corners {
		for _, z := range [2]float32{zmin, zmax} {
			p := unproject(c[0], c[1], z)
			box.min.X = minf(box.min.X, p.X)
			box.min.Y = minf(box.min.Y, p.Y)
			box.min.Z = minf(box.min.Z, p.Z)
			box.max.X = maxf(box.max.X, p.X)
			box.max.Y = maxf(box.max.Y, p.Y)
			box.max.Z = maxf(box.max.Z, p.Z)
		}
	}
	return box
}

func sqrtf(v float32) float32 { return float32(math.Sqrt(float64(v))) }
func tanf(v float32) float32  { return float32(math.Tan(float64(v))) }

// shadePixel 计算单个像素的着色。
// 对每个光源：若贡献恰为 0 则跳过（等价于加上 0.0），
// 因此候选列表的差异不会改变结果。
func shadePixel(scene *Scene, lights []Light, gb []*graph.Buffer, out *graph.Buffer, x, y, width int, ambient float32) {
	i := y*width + x
	pos := get3(gb[TargetPosition], i)
	n := get3(gb[TargetNormal], i)
	albedo := get3(gb[TargetAlbedo], i)
	specular := float32(0.5)
	if len(gb) > TargetMaterial {
		specular = gb[TargetMaterial].Data[i]
	}

	// 未覆盖像素（法线为零）输出环境色。
	if n.X == 0 && n.Y == 0 && n.Z == 0 {
		put3(out, i, Vec3{0, 0, 0})
		return
	}

	var r, g, b float32
	r = albedo.X * ambient
	g = albedo.Y * ambient
	b = albedo.Z * ambient

	view := scene.Camera.Position.Sub(pos).Norm()
	for _, l := range lights {
		lv := l.Position.Sub(pos)
		dist := lv.Len()
		if l.Radius > 0 && dist > l.Radius {
			continue // 贡献恰为 0，跳过与累加 0.0 等价
		}
		ldir := lv.Norm()
		diff := n.Dot(ldir)
		if diff < 0 {
			diff = 0
		}
		att := float32(1)
		if l.Radius > 0 {
			att = 1 - dist/l.Radius
			att *= att
		}
		// 半程向量高光。
		h := ldir.Add(view).Norm()
		sp := n.Dot(h)
		if sp < 0 {
			sp = 0
		}
		sp = powf(sp, 32) * specular
		r += l.Color.X * (albedo.X*diff + sp) * att
		g += l.Color.Y * (albedo.Y*diff + sp) * att
		b += l.Color.Z * (albedo.Z*diff + sp) * att
	}
	put3(out, i, Vec3{r, g, b})
}

func powf(v float32, e int) float32 {
	r := float32(1)
	for i := 0; i < e; i++ {
		r *= v
	}
	return r
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}
