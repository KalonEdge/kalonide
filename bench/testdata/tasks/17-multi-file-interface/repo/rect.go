package shapes

// Rect is a W by H rectangle.
type Rect struct{ W, H float64 }

// Area returns the area.
func (r Rect) Area() float64 { return r.W * r.H }
