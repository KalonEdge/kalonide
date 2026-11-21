package shop

import "testing"

func TestTotal(t *testing.T) {
	items := []Item{{"a", 2, 3}, {"b", 1.5, 2}}
	if got := Total(items); got != 9 {
		t.Fatalf("Total = %v", got)
	}
	if got := discounted(items, 50); got != 4.5 {
		t.Fatalf("discounted = %v", got)
	}
}
