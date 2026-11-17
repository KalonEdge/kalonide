package ui

import (
	"strings"
	"unicode/utf8"
)

// shadowGlyphs are the letters of KALONIDE in the "ANSI Shadow" style:
// solid blocks with a box-drawing shadow, six rows tall.
var shadowGlyphs = map[rune][6]string{
	'K': {"██╗  ██╗", "██║ ██╔╝", "█████╔╝ ", "██╔═██╗ ", "██║  ██╗", "╚═╝  ╚═╝"},
	'A': {" █████╗ ", "██╔══██╗", "███████║", "██╔══██║", "██║  ██║", "╚═╝  ╚═╝"},
	'L': {"██╗     ", "██║     ", "██║     ", "██║     ", "███████╗", "╚══════╝"},
	'O': {" ██████╗ ", "██╔═══██╗", "██║   ██║", "██║   ██║", "╚██████╔╝", " ╚═════╝ "},
	'N': {"███╗   ██╗", "████╗  ██║", "██╔██╗ ██║", "██║╚██╗██║", "██║ ╚████║", "╚═╝  ╚═══╝"},
	'I': {"██╗", "██║", "██║", "██║", "██║", "╚═╝"},
	'D': {"██████╗ ", "██╔══██╗", "██║  ██║", "██║  ██║", "██████╔╝", "╚═════╝ "},
	'E': {"███████╗", "██╔════╝", "█████╗  ", "██╔══╝  ", "███████╗", "╚══════╝"},
}

// kalonLogo is the Kalon mark (a K in a ring of linked dots) drawn in
// braille, generated from logos/kalon.svg at 24×24 dots, with the K redrawn bolder
// so it stays readable at this size. It is six rows
// tall to sit beside the wordmark.
var kalonLogo = [6]string{
	"⠀⢀⡀⠀⣃⠸⠟⡄⢴⠄⠀⠀",
	"⢀⠘⠋⠶⠩⠄⠋⣶⡔⢛⠠⠄",
	"⢈⠘⣓⠓⣿⣧⣾⠋⠓⣙⢡⣤",
	"⠛⢃⣍⢤⣿⡏⢻⣧⣤⢬⡄⡁",
	"⠐⠂⣥⠘⠿⣠⠐⣂⠶⣠⡄⠁",
	"⠀⠀⠐⠗⠘⣴⡆⢩⠀⠈⠁⠀",
}

// The K sits in columns 4-7 of rows 2-3; it is drawn bright, the dots teal.
func logoIsK(row, col int) bool { return (row == 2 || row == 3) && col >= 4 && col <= 7 }

const logoWidth = 12

// Hero renders the startup mark for a terminal cols wide: the Kalon logo
// beside the KALONIDE wordmark when there is room, the wordmark alone when
// a little narrower, and the logo with the name in plain text on small
// terminals. The wordmark is drawn in the terminal's own bold text color
// (readable on dark and light themes) with the final E in Kalon teal.
func Hero(cols int) []string {
	mark := wordmarkLines("KALONIDE")
	markW := utf8.RuneCountInString(strings.Replace(mark[0], "\x00", "", 1))
	const indent, gap = 2, 3
	switch {
	case indent+logoWidth+gap+markW <= cols:
		out := make([]string, 6)
		for i := range out {
			out[i] = logoLine(i) + strings.Repeat(" ", gap) + colorMark(mark[i])
		}
		return out
	case indent+markW <= cols:
		out := make([]string, 6)
		for i := range out {
			out[i] = colorMark(mark[i])
		}
		return out
	case indent+logoWidth+gap+8 <= cols:
		out := make([]string, 6)
		for i := range out {
			out[i] = logoLine(i)
		}
		out[2] += strings.Repeat(" ", gap) + Bold("kalonide")
		return out
	}
	return nil
}

// wordmarkLines lays the letters side by side without colour. The last
// letter is separated by a NUL so colorMark can tint it.
func wordmarkLines(text string) []string {
	letters := []rune(strings.ToUpper(text))
	out := make([]string, 6)
	for row := range out {
		var b strings.Builder
		for i, r := range letters {
			if i == len(letters)-1 {
				b.WriteByte(0)
			}
			b.WriteString(shadowGlyphs[r][row])
		}
		out[row] = b.String()
	}
	return out
}

func colorMark(line string) string {
	head, tail, _ := strings.Cut(line, "\x00")
	if !colorOn {
		return strings.TrimRight(head+tail, " ")
	}
	return "\x1b[1m" + head + "\x1b[0m" + paint(accentCode+";1", strings.TrimRight(tail, " "))
}

func logoLine(row int) string {
	if !colorOn {
		return kalonLogo[row]
	}
	// Colour runs, not single characters, to keep the output small.
	var b, run strings.Builder
	runK := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runK {
			b.WriteString(Bold(run.String()))
		} else {
			b.WriteString(Accent(run.String()))
		}
		run.Reset()
	}
	for col, r := range []rune(kalonLogo[row]) {
		if k := logoIsK(row, col); k != runK {
			flush()
			runK = k
		}
		run.WriteRune(r)
	}
	flush()
	return b.String()
}
