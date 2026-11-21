package calc

import "errors"

func Add(a, b int) int { return a + b }

func Sub(a, b int) int { return a - b }

func Mul(a, b int) int { return a * b }

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

func Clamp(v, lo, hi int) int { return clamp(v, lo, hi) }
