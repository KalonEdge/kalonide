package labels

import "testing"

func TestLabels(t *testing.T) {
	got := Labels(3)
	if len(got) != 3 || got[0] != "item-1" || got[2] != "item-3" {
		t.Fatalf("Labels(3) = %v", got)
	}
	if Label(42) != "item-42" {
		t.Fatalf("Label(42) = %q", Label(42))
	}
}
