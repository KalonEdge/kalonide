package ui

import (
	"fmt"
	"os"
	"strings"
)

// Color output is on when stdout is a terminal and NO_COLOR is unset.
var colorOn = IsTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"

// SetColor forces color on or off.
func SetColor(on bool) { colorOn = on }

// ColorOn reports whether color output is enabled.
func ColorOn() bool { return colorOn }

var trueColor = strings.Contains(os.Getenv("COLORTERM"), "truecolor") || strings.Contains(os.Getenv("COLORTERM"), "24bit")

func paint(code, s string) string {
	if !colorOn || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func rgb(r, g, b int, fallback int) string {
	if trueColor {
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}
	return fmt.Sprintf("38;5;%d", fallback)
}

// Kalon teal #0891B2 is the brand accent.
var (
	accentCode = rgb(0x08, 0x91, 0xB2, 31)
	brightCode = rgb(0x22, 0xD3, 0xEE, 45) // lighter teal for highlights
)

func Accent(s string) string { return paint(accentCode, s) }
func Bright(s string) string { return paint(brightCode+";1", s) }
func Bold(s string) string   { return paint("1", s) }
func Dim(s string) string    { return paint("2", s) }
func Italic(s string) string { return paint("3", s) }
func Red(s string) string    { return paint("31", s) }
func Green(s string) string  { return paint("32", s) }
func Yellow(s string) string { return paint("33", s) }

// VisibleLen is the display width of s, ignoring ANSI escape codes. Wide
// characters (CJK, most emoji) count as two columns.
func VisibleLen(s string) int {
	n := 0
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
			n += runeWidth(r)
		}
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x300 && r <= 0x36f, r == 0x200b, r == 0x200d, r >= 0xfe00 && r <= 0xfe0f:
		return 0 // combining marks and joiners
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// Sanitize removes terminal control characters from text that came from a
// model or a tool, so it cannot move the cursor, recolor, overwrite lines
// or fake a prompt. Newlines and tabs are kept.
func Sanitize(s string) string {
	clean := true
	for _, r := range s {
		if isControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r == 0xfffd: // invalid UTF-8, e.g. a raw 8-bit CSI byte
		return true
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls, including CSI (0x9b)
		return true
	case r == 0x2028 || r == 0x2029 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
		return true // line separators and bidirectional overrides
	}
	return false
}
