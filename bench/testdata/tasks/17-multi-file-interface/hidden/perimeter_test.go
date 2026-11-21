package shapes

import (
	"math"
	"testing"
)

func TestPerimeter(t *testing.T) {
	var shapes []Shape = []Shape{Circle{1}, Rect{2, 3}, Triangle{3, 4, 5}}
	want := []float64{2 * math.Pi, 10, 12}
	for i, s := range shapes {
		if got := s.Perimeter(); math.Abs(got-want[i]) > 1e-9 {
			t.Errorf("%T perimeter = %v, want %v", s, got, want[i])
		}
	}
}
