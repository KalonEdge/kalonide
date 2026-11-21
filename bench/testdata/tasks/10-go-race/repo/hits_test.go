package hits

import "testing"

func TestCount(t *testing.T) {
	for i := 0; i < 20; i++ {
		if got := Count(500); got != 500 {
			t.Fatalf("Count(500) = %d", got)
		}
	}
}
