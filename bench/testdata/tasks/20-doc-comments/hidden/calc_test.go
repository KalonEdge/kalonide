package calc

import "testing"

func TestCalc(t *testing.T) {
	if Add(2, 3) != 5 || Sub(2, 3) != -1 || Mul(2, 3) != 6 || Clamp(9, 0, 5) != 5 {
		t.Fatal("arithmetic changed")
	}
	if q, err := Div(7, 2); q != 3 || err != nil {
		t.Fatal("Div changed")
	}
	if _, err := Div(1, 0); err == nil {
		t.Fatal("Div by zero changed")
	}
}
