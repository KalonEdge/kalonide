package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Previewer is implemented by tools that can show what a call will change
// before it runs, so the user sees the diff before approving it.
type Previewer interface {
	Preview(env *Env, args json.RawMessage) string
}

// DiffOp marks a diff line.
const (
	DiffSame = ' '
	DiffDel  = '-'
	DiffAdd  = '+'
)

// DiffLine is one line of a line diff, with its line number in the old
// (deleted, unchanged) or new (added) text.
type DiffLine struct {
	Op   byte
	Line int
	Text string
}

// maxDiffLines bounds the input to the quadratic diff; larger changes are
// summarised instead.
const maxDiffLines = 600

// LineDiff returns the shortest edit script from a to b, line by line
// (longest common subsequence). Line numbers start at startA and startB.
func LineDiff(a, b []string, startA, startB int) []DiffLine {
	n, m := len(a), len(b)
	// lcs[i][j] = length of the LCS of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, DiffLine{DiffSame, startA + i, a[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			// On a tie, deletions come first, as in a conventional diff.
			out = append(out, DiffLine{DiffDel, startA + i, a[i]})
			i++
		default:
			out = append(out, DiffLine{DiffAdd, startB + j, b[j]})
			j++
		}
	}
	return out
}

// FormatDiff renders diff lines as "-  12 │ text", keeping at most context
// unchanged lines around each change and at most limit lines in all.
func FormatDiff(lines []DiffLine, context, limit int) string {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == DiffSame {
			continue
		}
		for k := max(0, i-context); k <= min(len(lines)-1, i+context); k++ {
			keep[k] = true
		}
	}
	var b strings.Builder
	shown, gap := 0, false
	for i, l := range lines {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && shown > 0 {
			b.WriteString("     ⋮\n")
		}
		gap = false
		if shown == limit {
			rest := 0
			for _, x := range lines[i:] {
				if x.Op != DiffSame {
					rest++
				}
			}
			fmt.Fprintf(&b, "     … %d more changed lines\n", rest)
			break
		}
		fmt.Fprintf(&b, "%c %4d │ %s\n", l.Op, l.Line, strings.TrimRight(l.Text, "\r"))
		shown++
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Preview shows the lines an edit will change, with their line numbers.
func (EditFile) Preview(env *Env, args json.RawMessage) string {
	var a editArgs
	if json.Unmarshal(args, &a) != nil || a.OldString == "" {
		return ""
	}
	p, err := env.Resolve(a.Path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	idx := strings.Index(text, strings.ReplaceAll(a.OldString, "\r\n", "\n"))
	if idx < 0 {
		return "" // the edit will fail and say why
	}
	first := strings.Count(text[:idx], "\n") + 1
	oldL, newL := splitLines(a.OldString), splitLines(a.NewString)
	if len(oldL)+len(newL) > maxDiffLines {
		return fmt.Sprintf("  large edit: %d lines replaced by %d, from line %d", len(oldL), len(newL), first)
	}
	// Add a little of the surrounding file so the change has a location.
	const ctx = 2
	file := splitLines(text)
	from := max(first-1-ctx, 0)
	to := min(first-1+len(oldL)+ctx, len(file))
	before, after := file[from:first-1], file[min(first-1+len(oldL), len(file)):to]
	oldBlock := append(append(append([]string{}, before...), oldL...), after...)
	newBlock := append(append(append([]string{}, before...), newL...), after...)
	out := FormatDiff(LineDiff(oldBlock, newBlock, from+1, from+1), ctx, 40)
	if a.ReplaceAll {
		if n := strings.Count(text, a.OldString); n > 1 {
			out += fmt.Sprintf("\n     (and %d more places)", n-1)
		}
	}
	return out
}

// Preview shows a new file's first lines, or the diff when replacing one.
func (WriteFile) Preview(env *Env, args json.RawMessage) string {
	var a writeArgs
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	p, err := env.Resolve(a.Path)
	if err != nil {
		return ""
	}
	newL := splitLines(a.Content)
	old, err := os.ReadFile(p)
	if err != nil || !a.Overwrite {
		lines := make([]DiffLine, len(newL))
		for i, l := range newL {
			lines[i] = DiffLine{DiffAdd, i + 1, l}
		}
		return FormatDiff(lines, 0, 20)
	}
	oldL := splitLines(string(old))
	if len(oldL)+len(newL) > maxDiffLines {
		return fmt.Sprintf("  replaces %d lines with %d lines", len(oldL), len(newL))
	}
	return FormatDiff(LineDiff(oldL, newL, 1, 1), 2, 40)
}
