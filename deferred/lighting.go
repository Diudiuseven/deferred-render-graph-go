package deferred

import (
	"sort"
	"sync"

	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

// ColorResource 最终颜色资源名。
const ColorResource = rendergraph.ResourceID("color")

// sortedLights 返回按 ID 排序的光源副本，保证累加顺序确定，
// 与分块合并顺序、遍历方式无关。
func sortedLights(lights []Light) []Light {
	out := append([]Light(nil), lights...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// renderLighting 光照阶段入口：tileSize>0 时分块推进，否则整屏逐像素。
// 两种路径对每个像素执行完全相同的光照累加，输出逐位一致。
func renderLighting(scene *Scene, layout GBufferLayout, width, height, tileSize, workers int, ctx *rendergraph.ExecContext) {
	lights := sortedLights(scene.Lights)
	if tileSize <= 0 {
		shadeRect(scene, layout, width, height, 0, 0, width, height, lights, ctx)
		return
	}
	// 生成分块（顺序固定）
	type tile struct{ x0, y0, x1, y1 int }
	var tiles []tile
	for y0 := 0; y0 < height; y0 += tileSize {
		for x0 := 0; x0 < width; x0 += tileSize {
			tiles = append(tiles, tile{x0, y0, minInt(x0+tileSize, width), minInt(y0+tileSize, height)})
		}
	}
	if workers <= 0 {
		workers = 1
	}
	// 每块只取可能影响该块的光源（保守剔除），块内仍逐像素精确判断，
	// 因此拼接结果与逐像素处理完全一致；各块写不相交像素，合并顺序无关。
	jobs := make(chan tile)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				tl := lightsForTile(scene, width, height, t.x0, t.y0, t.x1, t.y1, lights)
				shadeRect(scene, layout, width, height, t.x0, t.y0, t.x1, t.y1, tl, ctx)
			}
		}()
	}
	for _, t := range tiles {
		jobs <- t
	}
	close(jobs)
	wg.Wait()
}

// lightsForTile 返回可能影响指定分块的光源（保守超集）。
// 光源影响球投影到屏幕为圆；无法投影（在相机近平面之后）时保守地保留。
func lightsForTile(scene *Scene, width, height, x0, y0, x1, y1 int, lights []Light) []Light {
	fov := scene.Camera.FovScale
	if fov == 0 {
		fov = 1
	}
	aspect := float32(width) / float32(height)
	var out []Light
	for _, l := range lights {
		v := sub(l.Position, scene.Camera.Origin)
		z := -v.Z // 相机前方距离
		if z-l.Radius <= 1e-6 {
			out = append(out, l) // 影响球跨越相机平面，无法安全投影，保守保留
			continue
		}
		// 用最近深度投影半径，得到保守（偏大）的屏幕圆
		zNear := z - l.Radius
		sx := (v.X/z)/(aspect*fov)*0.5*float32(width) + 0.5*float32(width)
		sy := 0.5*float32(height) - (v.Y/z)/fov*0.5*float32(height)
		sr := (l.Radius/zNear)/fov*0.5*float32(height) + 1 // +1 像素余量
		// 圆与分块矩形相交测试
		cx := max32(float32(x0), min32f(sx, float32(x1)))
		cy := max32(float32(y0), min32f(sy, float32(y1)))
		dx, dy := sx-cx, sy-cy
		if dx*dx+dy*dy <= sr*sr {
			out = append(out, l)
		}
	}
	return out
}

func min32f(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// shadeRect 对矩形区域逐像素着色。光源按 ID 顺序累加，
// 每个像素的结果只取决于该像素的 GBuffer 数据与光源序列。
func shadeRect(scene *Scene, layout GBufferLayout, width, height, x0, y0, x1, y1 int, lights []Light, ctx *rendergraph.ExecContext) {
	var albedo, normal, depth, material *rendergraph.Image
	if layout.Has(TargetAlbedo) {
		albedo = ctx.Get(ResourceName(TargetAlbedo))
	}
	if layout.Has(TargetNormal) {
		normal = ctx.Get(ResourceName(TargetNormal))
	}
	if layout.Has(TargetDepth) {
		depth = ctx.Get(ResourceName(TargetDepth))
	}
	if layout.Has(TargetMaterial) {
		material = ctx.Get(ResourceName(TargetMaterial))
	}
	color := ctx.Get(ColorResource)

	aspect := float32(width) / float32(height)
	fov := scene.Camera.FovScale
	if fov == 0 {
		fov = 1
	}
	ro := scene.Camera.Origin

	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			idx := (y*width + x) * 4
			d := farDepth
			if depth != nil {
				d = depth.Pixels[idx]
			}
			var out Vec3
			if d >= farDepth {
				out = Vec3{} // 背景
			} else {
				var alb, n Vec3
				var shin float32
				if albedo != nil {
					alb = Vec3{albedo.Pixels[idx], albedo.Pixels[idx+1], albedo.Pixels[idx+2]}
				}
				if normal != nil {
					n = Vec3{normal.Pixels[idx], normal.Pixels[idx+1], normal.Pixels[idx+2]}
				}
				if material != nil {
					shin = material.Pixels[idx]
				}
				// 由深度重建世界坐标（与几何阶段同一公式，确定）
				ndcX := (2*(float32(x)+0.5)/float32(width) - 1) * aspect * fov
				ndcY := (1 - 2*(float32(y)+0.5)/float32(height)) * fov
				rd := normalize(Vec3{ndcX, ndcY, -1})
				hit := add(ro, scale(rd, d))

				out = mul(scene.Ambient, alb)
				for _, l := range lights {
					out = add(out, lightContrib(scene, l, hit, n, alb, shin, ro))
				}
			}
			color.Pixels[idx] = out.X
			color.Pixels[idx+1] = out.Y
			color.Pixels[idx+2] = out.Z
			color.Pixels[idx+3] = 1
		}
	}
}

// lightContrib 单个光源对像素的贡献；超出影响半径贡献严格为零，
// 因此分块剔除不会漏掉任何非零贡献。
func lightContrib(scene *Scene, l Light, hit, n, alb Vec3, shin float32, ro Vec3) Vec3 {
	toL := sub(l.Position, hit)
	dist := length(toL)
	if dist >= l.Radius || dist == 0 {
		return Vec3{}
	}
	ldir := scale(toL, 1/dist)
	att := 1 - dist/l.Radius
	att = att * att
	diff := max32(dot(n, ldir), 0)
	// Blinn-Phong 高光
	view := normalize(sub(ro, hit))
	half := normalize(add(ldir, view))
	spec := pow32(max32(dot(n, half), 0), shin)
	base := add(scale(alb, diff), scale(Vec3{1, 1, 1}, spec))
	return scale(mul(l.Color, base), att)
}
