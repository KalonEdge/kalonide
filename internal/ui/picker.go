package ui

import (
	"fmt"
	"strings"
	"unicode"
)

// PickItem is one picker row.
type PickItem struct {
	Label  string
	Detail string
}

// Pick shows an arrow-key list with type-to-filter. It returns the index
// of the chosen item in items, or -1 when cancelled with Esc or Ctrl-C.
func Pick(t *Terminal, title string, items []PickItem, selected int) (int, error) {
	if len(items) == 0 {
		return -1, nil
	}
	if err := t.Raw(); err != nil {
		return -1, err
	}
	defer t.Restore()
	fmt.Print("\x1b[?25l") // hide cursor
	defer fmt.Print("\x1b[?25h")

	filter := ""
	visible := func() []int {
		var idx []int
		f := strings.ToLower(filter)
		for i, it := range items {
			if f == "" || strings.Contains(strings.ToLower(it.Label+" "+it.Detail), f) {
				idx = append(idx, i)
			}
		}
		return idx
	}
	cur := 0
	vis := visible()
	for i, v := range vis {
		if v == selected {
			cur = i
		}
	}
	const pageSize = 12
	top := 0
	drawn := 0

	draw := func() {
		var b strings.Builder
		if drawn > 0 {
			fmt.Fprintf(&b, "\x1b[%dA", drawn)
		}
		b.WriteString("\r\x1b[J")
		cols := max(Width(), 20)
		lines := 0
		line := func(s string) {
			b.WriteString(clip(s, cols-1))
			b.WriteString("\r\n")
			lines++
		}
		hint := "↑↓ move · enter select · esc cancel · type to filter"
		if filter != "" {
			line(Bold(title) + "  " + Accent("filter: "+filter) + Dim("▏"))
		} else {
			line(Bold(title) + "  " + Dim(hint))
		}
		if len(vis) == 0 {
			line(Dim("  no matches"))
		}
		if cur < top {
			top = cur
		}
		if cur >= top+pageSize {
			top = cur - pageSize + 1
		}
		for i := top; i < len(vis) && i < top+pageSize; i++ {
			it := items[vis[i]]
			mark := "  "
			label := it.Label
			if vis[i] == selected {
				label += Dim(" (current)")
			}
			if i == cur {
				mark = Bright("❯ ")
				label = Bright(it.Label) + strings.TrimPrefix(label, it.Label)
			}
			row := mark + label
			if it.Detail != "" {
				row += "  " + Dim(it.Detail)
			}
			line(row)
		}
		if len(vis) > pageSize {
			line(Dim(fmt.Sprintf("  %d of %d", cur+1, len(vis))))
		}
		drawn = lines
		fmt.Print(b.String())
	}
	clear := func() {
		if drawn > 0 {
			fmt.Printf("\x1b[%dA\r\x1b[J", drawn)
		}
	}

	draw()
	for {
		k := ReadKey()
		switch k.Kind {
		case KeyEOF, KeyCtrlC, KeyCtrlD:
			clear()
			return -1, nil
		case KeyEsc:
			if filter != "" {
				filter = ""
				vis = visible()
				cur = 0
				break
			}
			clear()
			return -1, nil
		case KeyEnter:
			if len(vis) > 0 {
				clear()
				return vis[cur], nil
			}
		case KeyUp:
			if cur > 0 {
				cur--
			} else if len(vis) > 0 {
				cur = len(vis) - 1
			}
		case KeyDown, KeyTab:
			if cur < len(vis)-1 {
				cur++
			} else {
				cur = 0
			}
		case KeyPageUp:
			cur = max(cur-pageSize, 0)
		case KeyPageDown:
			cur = max(min(cur+pageSize, len(vis)-1), 0)
		case KeyHome:
			cur = 0
		case KeyEnd:
			cur = max(len(vis)-1, 0)
		case KeyBackspace:
			if filter != "" {
				r := []rune(filter)
				filter = string(r[:len(r)-1])
				vis = visible()
				cur = 0
			}
		case KeyRune, KeyPaste:
			txt := string(k.Rune)
			if k.Kind == KeyPaste {
				txt = strings.TrimSpace(k.Text)
			}
			if k.Kind == KeyRune && !unicode.IsPrint(k.Rune) {
				break
			}
			filter += txt
			vis = visible()
			cur = 0
		}
		draw()
	}
}

// Choice is one single-key answer for Confirm.
type Choice struct {
	Key   rune
	Label string
}

// Confirm asks a question answered with one key press. It returns the key
// chosen, or 0 when cancelled (Esc, Ctrl-C). Enter picks the first choice.
func Confirm(t *Terminal, question string, choices []Choice) (rune, error) {
	if err := t.Raw(); err != nil {
		return 0, err
	}
	defer t.Restore()
	var opts []string
	for _, c := range choices {
		opts = append(opts, Accent("["+string(c.Key)+"]")+" "+c.Label)
	}
	fmt.Print(question + "  " + strings.Join(opts, "  ") + " ")
	for {
		k := ReadKey()
		switch k.Kind {
		case KeyEOF, KeyCtrlC, KeyEsc, KeyCtrlD:
			fmt.Print(Dim("cancelled") + "\r\n")
			return 0, nil
		case KeyEnter:
			fmt.Print(choices[0].Label + "\r\n")
			return choices[0].Key, nil
		case KeyRune:
			r := unicode.ToLower(k.Rune)
			for _, c := range choices {
				if c.Key == r {
					fmt.Print(c.Label + "\r\n")
					return c.Key, nil
				}
			}
		}
	}
}

// clip cuts s to fit width display columns, keeping ANSI codes intact.
func clip(s string, width int) string {
	if VisibleLen(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		rw := runeWidth(r)
		if w+rw > width-1 {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		w += rw
	}
	if colorOn {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}
