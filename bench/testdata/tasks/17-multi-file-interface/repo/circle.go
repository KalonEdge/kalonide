package shapes

import "math"

// Circle is a circle with radius R.
type Circle struct{ R float64 }

// Area returns the area.
func (c Circle) Area() float64 { return math.Pi * c.R * c.R }
