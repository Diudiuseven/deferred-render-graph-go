// Package deferred 在 rendergraph 之上实现延迟着色管线。
package deferred

// Vec3 三维向量。
type Vec3 struct{ X, Y, Z float32 }

// Sphere 场景中的球体（几何体）。
type Sphere struct {
	Center Vec3
	Radius float32
	Albedo Vec3
	Shininess float32
}

// Light 点光源。
type Light struct {
	ID       int
	Position Vec3
	Color    Vec3
	Radius   float32 // 影响半径，之外贡献为零
}

// Camera 简单针孔相机。
type Camera struct {
	Origin Vec3
	// 视平面：z = -1，宽高由渲染目标决定
	FovScale float32
}

// Scene 场景。
type Scene struct {
	Spheres []Sphere
	Lights  []Light
	Camera  Camera
	Ambient Vec3
}

// ---- 向量运算（纯函数，保证确定性）----

func add(a, b Vec3) Vec3    { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func sub(a, b Vec3) Vec3    { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func scale(a Vec3, s float32) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func mul(a, b Vec3) Vec3    { return Vec3{a.X * b.X, a.Y * b.Y, a.Z * b.Z} }
func dot(a, b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func length(a Vec3) float32 { return sqrt32(dot(a, a)) }

func normalize(a Vec3) Vec3 {
	l := length(a)
	if l == 0 {
		return Vec3{}
	}
	return scale(a, 1/l)
}

// sqrt32 确定性平方根（基于 float64 实现，跨平台一致）。
func sqrt32(x float32) float32 { return float32(sqrt64(float64(x))) }

func sqrt64(x float64) float64 {
	// 使用标准库之外的确定性实现：牛顿迭代初值来自位运算。
	if x <= 0 {
		return 0
	}
	// 直接调用 math.Sqrt；Go 的 math.Sqrt 在支持硬件指令的平台上结果确定。
	return mathSqrt(x)
}

// intersectSphere 返回射线与球的最近正交点距离，未命中返回 -1。
func intersectSphere(ro, rd Vec3, s Sphere) float32 {
	oc := sub(ro, s.Center)
	b := dot(oc, rd)
	c := dot(oc, oc) - s.Radius*s.Radius
	h := b*b - c
	if h < 0 {
		return -1
	}
	t := -b - sqrt32(h)
	if t < 0 {
		return -1
	}
	return t
}
