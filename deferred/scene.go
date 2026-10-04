package deferred

import "math"

// Vec3 是三维向量。
type Vec3 struct{ X, Y, Z float32 }

func (a Vec3) Add(b Vec3) Vec3    { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3    { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Mul(s float32) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Dot(b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func (a Vec3) Len() float32 { return float32(math.Sqrt(float64(a.Dot(a)))) }
func (a Vec3) Norm() Vec3 {
	l := a.Len()
	if l == 0 {
		return Vec3{}
	}
	return a.Mul(1 / l)
}

// Mat4 是行主序 4x4 矩阵。
type Mat4 [16]float32

// MulV 用矩阵变换点（w=1），返回裁剪空间坐标与 w。
func (m Mat4) MulV(v Vec3) (x, y, z, w float32) {
	x = m[0]*v.X + m[1]*v.Y + m[2]*v.Z + m[3]
	y = m[4]*v.X + m[5]*v.Y + m[6]*v.Z + m[7]
	z = m[8]*v.X + m[9]*v.Y + m[10]*v.Z + m[11]
	w = m[12]*v.X + m[13]*v.Y + m[14]*v.Z + m[15]
	return
}

// LookAt 构造视图矩阵。
func LookAt(eye, target, up Vec3) Mat4 {
	f := target.Sub(eye).Norm()
	s := f.Cross(up.Norm()).Norm()
	u := s.Cross(f)
	return Mat4{
		s.X, s.Y, s.Z, -s.Dot(eye),
		u.X, u.Y, u.Z, -u.Dot(eye),
		-f.X, -f.Y, -f.Z, f.Dot(eye),
		0, 0, 0, 1,
	}
}

// Perspective 构造透视投影矩阵（右手系，深度映射到 [0,1]）。
func Perspective(fovY, aspect, near, far float32) Mat4 {
	t := float32(1 / math.Tan(float64(fovY)/2))
	return Mat4{
		t / aspect, 0, 0, 0,
		0, t, 0, 0,
		0, 0, far / (near - far), near * far / (near - far),
		0, 0, -1, 0,
	}
}

// Mul 矩阵相乘：m*n。
func (m Mat4) Mul(n Mat4) Mat4 {
	var r Mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			var s float32
			for k := 0; k < 4; k++ {
				s += m[i*4+k] * n[k*4+j]
			}
			r[i*4+j] = s
		}
	}
	return r
}

// Triangle 是一个世界空间三角形，带材质索引。
type Triangle struct {
	V0, V1, V2 Vec3
	Material   int
}

// Material 描述表面材质。
type Material struct {
	Albedo   Vec3
	Specular float32
}

// Light 是点光源。
type Light struct {
	ID       int
	Position Vec3
	Color    Vec3
	Radius   float32
}

// Camera 是简单针孔相机。
type Camera struct {
	Position, Target, Up Vec3
	FovY                 float32
	Near, Far            float32
}

// ViewProj 返回相机的视图投影矩阵。
func (c Camera) ViewProj(aspect float32) Mat4 {
	return Perspective(c.FovY, aspect, c.Near, c.Far).Mul(LookAt(c.Position, c.Target, c.Up))
}

// Scene 包含几何体、材质与光源。
type Scene struct {
	Triangles []Triangle
	Materials []Material
	Lights    []Light
	Camera    Camera
}
