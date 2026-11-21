package shapes

// Shape is a closed 2D figure.
type Shape interface {
	Area() float64
}

// TotalArea sums the areas of shapes.
func TotalArea(ss []Shape) float64 {
	t := 0.0
	for _, s := range ss {
		t += s.Area()
	}
	return t
}
