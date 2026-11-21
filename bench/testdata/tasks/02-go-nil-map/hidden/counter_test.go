package wc

import "testing"

func TestCounter(t *testing.T) {
	c := New()
	c.Add("a")
	c.Add("a")
	c.Add("b")
	if c.Count("a") != 2 || c.Count("b") != 1 || c.Count("z") != 0 {
		t.Fatalf("counts wrong: a=%d b=%d z=%d", c.Count("a"), c.Count("b"), c.Count("z"))
	}
}
