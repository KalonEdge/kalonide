package ui

import (
	"fmt"
	"strings"
)

// glyphs are Kalonide's own 5-row block letters. '#' is a filled cell.
var glyphs = map[rune][5]string{
	'K': {"#   #", "#  # ", "###  ", "#  # ", "#   #"},
	'A': {" ### ", "#   #", "#####", "#   #", "#   #"},
	'L': {"#    ", "#    ", "#    ", "#    ", "#####"},
	'O': {" ### ", "#   #", "#   #", "#   #", " ### "},
	'N': {"#   #", "##  #", "# # #", "#  ##", "#   #"},
	'I': {"###", " # ", " # ", " # ", "###"},
	'D': {"#### ", "#   #", "#   #", "#   #", "#### "},
	'E': {"#####", "#    ", "#### ", "#    ", "#####"},
}

// Wordmark renders text in block letters that fit within cols columns.
// It uses double-width cells when there is room, single-width when not,
// and returns nil when even that does not fit (the caller then shows a
// one-line name instead). Lines are colored with a teal gradient.
func Wordmark(text string, cols int) []string {
	pixels := wordmarkPixels(text)
	if pixels == nil {
		return nil
	}
	width := len([]rune(pixels[0]))
	cell := 0
	switch {
	case width*2+4 <= cols:
		cell = 2
	case width+4 <= cols:
		cell = 1
	default:
		return nil
	}
	out := make([]string, len(pixels))
	for row, line := range pixels {
		var b strings.Builder
		for col, p := range []rune(line) {
			if p != '#' {
				b.WriteString(strings.Repeat(" ", cell))
				continue
			}
			block := strings.Repeat("█", cell)
			if colorOn {
				b.WriteString(gradient(col, width, row) + block + "\x1b[0m")
			} else {
				b.WriteString(block)
			}
		}
		out[row] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// wordmarkPixels lays out the letters with one empty column between them.
func wordmarkPixels(text string) []string {
	var rows [5]strings.Builder
	for i, r := range strings.ToUpper(text) {
		g, ok := glyphs[r]
		if !ok {
			return nil
		}
		for row := range rows {
			if i > 0 {
				rows[row].WriteByte(' ')
			}
			rows[row].WriteString(g[row])
		}
	}
	out := make([]string, 5)
	for i := range rows {
		out[i] = rows[i].String()
	}
	return out
}

// gradient runs from light teal (#22D3EE) through Kalon teal (#0891B2) to
// deep teal (#0E7490) across the word, slightly darker on lower rows.
func gradient(col, width, row int) string {
	stops := [][3]float64{{0x22, 0xD3, 0xEE}, {0x08, 0x91, 0xB2}, {0x0E, 0x74, 0x90}}
	t := float64(col) / float64(max(width-1, 1))
	seg := 0
	if t > 0.5 {
		seg, t = 1, (t-0.5)*2
	} else {
		t *= 2
	}
	a, b := stops[seg], stops[seg+1]
	shade := 1 - 0.06*float64(row)
	c := func(i int) int { return int((a[i] + (b[i]-a[i])*t) * shade) }
	if trueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c(0), c(1), c(2))
	}
	// 256-color fallback: light, mid and deep teal.
	switch {
	case col < width/3:
		return "\x1b[38;5;45m"
	case col < 2*width/3:
		return "\x1b[38;5;38m"
	}
	return "\x1b[38;5;31m"
}
