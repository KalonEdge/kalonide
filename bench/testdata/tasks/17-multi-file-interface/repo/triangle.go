package shapes

import "math"

// Triangle has sides A, B and C.
type Triangle struct{ A, B, C float64 }

// Area uses Heron's formula.
func (t Triangle) Area() float64 {
	s := (t.A + t.B + t.C) / 2
	return math.Sqrt(s * (s - t.A) * (s - t.B) * (s - t.C))
}
