package calc

import "errors"

// Add returns a plus b.
func Add(a, b int) int { return a + b }

// Sub returns a minus b.
func Sub(a, b int) int { return a - b }

// Mul returns a times b.
func Mul(a, b int) int { return a * b }

// Div returns a divided by b, or an error when b is zero.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("division by zero")
	}
	return a / b, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Clamp limits v to the range lo to hi.
func Clamp(v, lo, hi int) int { return clamp(v, lo, hi) }
