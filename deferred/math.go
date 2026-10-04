package deferred

import "math"

func mathSqrt(x float64) float64 { return math.Sqrt(x) }

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// pow32 确定性幂运算（用于高光项）。
func pow32(base, exp float32) float32 {
	if base <= 0 {
		return 0
	}
	return float32(math.Pow(float64(base), float64(exp)))
}
