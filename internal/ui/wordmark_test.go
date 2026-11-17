package ui

import (
	"strings"
	"testing"
)

func TestWordmarkFitsAndFallsBack(t *testing.T) {
	SetColor(false)
	defer SetColor(colorOn)
	for _, tc := range []struct {
		cols, wantCell int
	}{{120, 2}, {80, 1}, {40, 0}} {
		lines := Wordmark("kalonide", tc.cols)
		if tc.wantCell == 0 {
			if lines != nil {
				t.Fatalf("cols %d: expected fallback", tc.cols)
			}
			continue
		}
		if len(lines) != 5 {
			t.Fatalf("cols %d: %d rows", tc.cols, len(lines))
		}
		for _, l := range lines {
			if w := VisibleLen(l); w+2 > tc.cols {
				t.Fatalf("cols %d: line is %d wide", tc.cols, w)
			}
		}
		if !strings.Contains(lines[0], strings.Repeat("█", tc.wantCell)) {
			t.Fatalf("cols %d: wrong cell width\n%s", tc.cols, strings.Join(lines, "\n"))
		}
	}
	if Wordmark("kalon?", 200) != nil {
		t.Fatal("unknown letters must fall back")
	}
}
