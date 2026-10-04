package deferred

import (
	"github.com/Diudiuseven/deferred-render-graph-go/rendergraph"
)

// farDepth 表示背景像素的深度。
const farDepth = float32(1e30)

// renderGeometry 几何阶段：逐像素求交，把最近可见面的几何与材质写入 GBuffer。
// 球体按切片顺序遍历、严格取更近者，结果与遍历实现无关、完全确定。
func renderGeometry(scene *Scene, layout GBufferLayout, width, height int, ctx *rendergraph.ExecContext) {
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

	aspect := float32(width) / float32(height)
	fov := scene.Camera.FovScale
	if fov == 0 {
		fov = 1
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			ndcX := (2*(float32(x)+0.5)/float32(width) - 1) * aspect * fov
			ndcY := (1 - 2*(float32(y)+0.5)/float32(height)) * fov
			rd := normalize(Vec3{ndcX, ndcY, -1})
			ro := scene.Camera.Origin

			bestT := farDepth
			bestIdx := -1
			for i, s := range scene.Spheres {
				t := intersectSphere(ro, rd, s)
				if t > 0 && t < bestT {
					bestT = t
					bestIdx = i
				}
			}

			idx := (y*width + x) * 4
			if bestIdx < 0 {
				if albedo != nil {
					setPx(albedo.Pixels, idx, Vec3{})
				}
				if normal != nil {
					setPx(normal.Pixels, idx, Vec3{})
				}
				if depth != nil {
					depth.Pixels[idx] = farDepth
				}
				if material != nil {
					setPx(material.Pixels, idx, Vec3{})
				}
				continue
			}
			s := scene.Spheres[bestIdx]
			hit := add(ro, scale(rd, bestT))
			n := normalize(sub(hit, s.Center))
			if albedo != nil {
				setPx(albedo.Pixels, idx, s.Albedo)
			}
			if normal != nil {
				setPx(normal.Pixels, idx, n)
			}
			if depth != nil {
				depth.Pixels[idx] = bestT
			}
			if material != nil {
				material.Pixels[idx] = s.Shininess
			}
		}
	}
}

func setPx(pix []float32, idx int, v Vec3) {
	pix[idx] = v.X
	pix[idx+1] = v.Y
	pix[idx+2] = v.Z
}
