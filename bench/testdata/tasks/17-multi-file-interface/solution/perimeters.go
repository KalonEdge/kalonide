package shapes

import "math"

// Perimeter returns the circumference.
func (c Circle) Perimeter() float64 { return 2 * math.Pi * c.R }

// Perimeter returns the length of the edges.
func (r Rect) Perimeter() float64 { return 2 * (r.W + r.H) }

// Perimeter returns the sum of the sides.
func (t Triangle) Perimeter() float64 { return t.A + t.B + t.C }
