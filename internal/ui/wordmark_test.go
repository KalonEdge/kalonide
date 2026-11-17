package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShadowGlyphsAreRectangular(t *testing.T) {
	for r, g := range shadowGlyphs {
		w := utf8.RuneCountInString(g[0])
		for i, row := range g {
			if n := utf8.RuneCountInString(row); n != w {
				t.Errorf("%c row %d is %d wide, want %d", r, i, n, w)
			}
		}
	}
	for i, row := range kalonLogo {
		if n := utf8.RuneCountInString(row); n != logoWidth {
			t.Errorf("logo row %d is %d wide", i, n)
		}
	}
}

func TestHeroFitsEveryWidth(t *testing.T) {
	saved := colorOn
	defer SetColor(saved)
	for _, color := range []bool{false, true} {
		SetColor(color)
		for _, cols := range []int{200, 79, 78, 64, 63, 40, 25, 10} {
			lines := Hero(cols)
			plain := stripANSI(strings.Join(lines, "\n"))
			for _, l := range lines {
				if w := VisibleLen(l); w+2 > cols {
					t.Errorf("color=%v cols=%d: line %d wide: %q", color, cols, w, l)
				}
			}
			switch {
			case cols >= 79:
				if len(lines) != 6 || !strings.Contains(plain, "⢀⡀") || !strings.Contains(plain, "██╗  ██╗") {
					t.Errorf("cols=%d: want logo and wordmark", cols)
				}
			case cols >= 64:
				if strings.Contains(plain, "⢀⡀") || !strings.Contains(plain, "██╗  ██╗") {
					t.Errorf("cols=%d: want wordmark only", cols)
				}
			case cols >= 25:
				if !strings.Contains(plain, "kalonide") {
					t.Errorf("cols=%d: want logo and name", cols)
				}
			default:
				if lines != nil {
					t.Errorf("cols=%d: want nothing", cols)
				}
			}
		}
	}
}

func TestHeroColors(t *testing.T) {
	saved := colorOn
	defer SetColor(saved)
	SetColor(true)
	line := Hero(120)[0]
	if !strings.Contains(line, "\x1b[1m██╗  ██╗") || !strings.Contains(line, accentCode+";1m███████╗") {
		t.Fatalf("wordmark colors wrong: %q", line)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
