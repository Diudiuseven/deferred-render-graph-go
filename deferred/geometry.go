package deferred

import (
	"github.com/Diudiuseven/deferred-render-graph-go/graph"
)

// GeometryPass 把可见面的几何与材质信息写入 G-Buffer 多目标中间结果。
// 光栅化在通道内部串行进行，保证结果确定（z-buffer 无竞争）。
func GeometryPass(scene *Scene, layout GBufferLayout) *graph.Pass {
	names := layout.TargetNames()
	writes := append(append([]string{}, names...), layout.DepthName())
	return &graph.Pass{
		Name:       "geometry",
		Writes:     writes,
		ConfigHash: hashUint64(0x9e3779b9, uint64(layout.Width), uint64(layout.Height), uint64(len(layout.Targets))),
		Execute: func(ctx *graph.PassContext) error {
			targets := make([]*graph.Buffer, len(names))
			for i, n := range names {
				targets[i] = ctx.MustGet(n)
			}
			depth := ctx.MustGet(layout.DepthName())
			rasterize(scene, layout, targets, depth)
			return nil
		},
	}
}

// 逐三角形、逐像素光栅化；z-buffer 取最近者。
// 初始化：深度为远平面（1.0 之后由 Clear 置 0，这里在函数内显式置 far）。
func rasterize(scene *Scene, layout GBufferLayout, targets []*graph.Buffer, depth *graph.Buffer) {
	w, h := layout.Width, layout.Height
	// 深度初始化为远平面。
	for i := range depth.Data {
		depth.Data[i] = 1e30
	}
	vp := scene.Camera.ViewProj(float32(w) / float32(h))

	for _, tri := range scene.Triangles {
		// 视空间变换。
		var sx, sy, sz [3]float32
		vs := [3]Vec3{tri.V0, tri.V1, tri.V2}
		visible := false
		for i, v := range vs {
			cx, cy, cz, cw := vp.MulV(v)
			if cw <= 0 {
				cw = 1e-6
			}
			sx[i] = (cx/cw + 1) * 0.5 * float32(w)
			sy[i] = (1 - cy/cw) * 0.5 * float32(h)
			sz[i] = cz / cw
			if sz[i] >= -1 && sz[i] <= 1 {
				visible = true
			}
		}
		if !visible {
			continue
		}

		// 包围盒。
		minX, maxX := sx[0], sx[0]
		minY, maxY := sy[0], sy[0]
		for i := 1; i < 3; i++ {
			minX = minf(minX, sx[i])
			maxX = maxf(maxX, sx[i])
			minY = minf(minY, sy[i])
			maxY = maxf(maxY, sy[i])
		}
		x0 := clampi(int(minX), 0, w-1)
		x1 := clampi(int(maxX)+1, 0, w-1)
		y0 := clampi(int(minY), 0, h-1)
		y1 := clampi(int(maxY)+1, 0, h-1)

		// 重心坐标分母。
		d := (sy[1]-sy[2])*(sx[0]-sx[2]) + (sx[2]-sx[1])*(sy[0]-sy[2])
		if d == 0 {
			continue
		}

		// 面法线（世界空间）。
		n := tri.V1.Sub(tri.V0).Cross(tri.V2.Sub(tri.V0)).Norm()
		mat := Material{Albedo: Vec3{1, 1, 1}}
		if tri.Material >= 0 && tri.Material < len(scene.Materials) {
			mat = scene.Materials[tri.Material]
		}

		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				px := float32(x) + 0.5
				py := float32(y) + 0.5
				w0 := ((sy[1]-sy[2])*(px-sx[2]) + (sx[2]-sx[1])*(py-sy[2])) / d
				w1 := ((sy[2]-sy[0])*(px-sx[2]) + (sx[0]-sx[2])*(py-sy[2])) / d
				w2 := 1 - w0 - w1
				if w0 < 0 || w1 < 0 || w2 < 0 {
					continue
				}
				z := w0*sz[0] + w1*sz[1] + w2*sz[2]
				di := y*w + x
				if z >= depth.Data[di] {
					continue
				}
				depth.Data[di] = z
				// 世界空间位置（透视校正简化为重心插值）。
				pos := tri.V0.Mul(w0).Add(tri.V1.Mul(w1)).Add(tri.V2.Mul(w2))
				for ti, buf := range targets {
					switch ti {
					case TargetPosition:
						put3(buf, di, pos)
					case TargetNormal:
						put3(buf, di, n)
					case TargetAlbedo:
						put3(buf, di, mat.Albedo)
					case TargetMaterial:
						buf.Data[di] = mat.Specular
					}
				}
			}
		}
	}
}

func put3(b *graph.Buffer, i int, v Vec3) {
	c := 3
	if len(b.Data)/maxi(1, b.Desc.Pixels()) >= 4 {
		c = 4
	}
	b.Data[i*c] = v.X
	b.Data[i*c+1] = v.Y
	b.Data[i*c+2] = v.Z
}

func get3(b *graph.Buffer, i int) Vec3 {
	c := 3
	if len(b.Data)/maxi(1, b.Desc.Pixels()) >= 4 {
		c = 4
	}
	return Vec3{b.Data[i*c], b.Data[i*c+1], b.Data[i*c+2]}
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hashUint64(seed uint64, vals ...uint64) uint64 {
	h := seed
	for _, v := range vals {
		h ^= v
		h *= 0x100000001b3
	}
	return h
}
