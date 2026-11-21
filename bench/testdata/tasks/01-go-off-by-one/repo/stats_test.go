package stats

import "testing"

func TestAverage(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{{[]float64{2, 4, 6}, 4}, {[]float64{5}, 5}, {nil, 0}, {[]float64{-1, 1}, 0}}
	for _, c := range cases {
		if got := Average(c.in); got != c.want {
			t.Errorf("Average(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
