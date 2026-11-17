package ui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrInterrupt is returned when the user presses Ctrl-C on an empty line.
var ErrInterrupt = errors.New("interrupted")

// Editor reads lines with editing, history and completion.
type Editor struct {
	Term *Terminal
	// Complete returns candidate completions for the text before the cursor.
	Complete func(prefix string) []string
	// ShiftTab is called on Shift+Tab (used to cycle permission modes).
	ShiftTab func()

	history  []string
	histPath string

	// render state
	buf       []rune
	pos       int
	cursorRow int
}

// NewEditor returns an editor whose history persists at histPath.
func NewEditor(t *Terminal, histPath string) *Editor {
	e := &Editor{Term: t, histPath: histPath}
	e.loadHistory()
	return e
}

func (e *Editor) loadHistory() {
	f, err := os.Open(e.histPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var s string
		if json.Unmarshal(sc.Bytes(), &s) == nil && s != "" {
			e.history = append(e.history, s)
		}
	}
	if len(e.history) > 1000 {
		e.history = e.history[len(e.history)-1000:]
	}
}

func (e *Editor) addHistory(s string) {
	if strings.TrimSpace(s) == "" || (len(e.history) > 0 && e.history[len(e.history)-1] == s) {
		return
	}
	e.history = append(e.history, s)
	if e.histPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(e.histPath), 0o700)
	f, err := os.OpenFile(e.histPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(s)
	f.Write(append(data, '\n'))
}

// ReadLine shows the prompt returned by prompt() and reads one entry.
// Enter submits; a trailing backslash or Alt+Enter adds a new line.
// It returns ErrInterrupt for Ctrl-C on an empty line and io.EOF for
// Ctrl-D on an empty line or closed input.
func (e *Editor) ReadLine(prompt func() string) (string, error) {
	if err := e.Term.Raw(); err != nil {
		return "", err
	}
	defer e.Term.Restore()
	fmt.Print("\x1b[?2004h") // bracketed paste on
	defer fmt.Print("\x1b[?2004l")

	e.buf, e.pos, e.cursorRow = nil, 0, 0
	histIdx := len(e.history)
	draft := ""
	e.render(prompt())

	for {
		k := ReadKey()
		switch k.Kind {
		case KeyEOF:
			e.finish(prompt())
			return "", io.EOF
		case KeyEnter:
			if e.pos == len(e.buf) && e.pos > 0 && e.buf[e.pos-1] == '\\' {
				e.buf[e.pos-1] = '\n'
				break
			}
			e.finish(prompt())
			s := string(e.buf)
			e.addHistory(s)
			return s, nil
		case KeyNewline:
			e.insert([]rune{'\n'})
		case KeyCtrlC:
			if len(e.buf) == 0 {
				e.finish(prompt())
				return "", ErrInterrupt
			}
			e.buf, e.pos = nil, 0
		case KeyCtrlD:
			if len(e.buf) == 0 {
				e.finish(prompt())
				return "", io.EOF
			}
			if e.pos < len(e.buf) {
				e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
			}
		case KeyRune:
			if k.Rune != 0 {
				e.insert([]rune{k.Rune})
			}
		case KeyPaste:
			e.insert([]rune(k.Text))
		case KeyBackspace:
			if e.pos > 0 {
				e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
				e.pos--
			}
		case KeyDelete:
			if e.pos < len(e.buf) {
				e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
			}
		case KeyLeft:
			if e.pos > 0 {
				e.pos--
			}
		case KeyRight:
			if e.pos < len(e.buf) {
				e.pos++
			}
		case KeyWordLeft:
			e.pos = wordLeft(e.buf, e.pos)
		case KeyWordRight:
			e.pos = wordRight(e.buf, e.pos)
		case KeyHome:
			e.pos = lineStart(e.buf, e.pos)
		case KeyEnd:
			e.pos = lineEnd(e.buf, e.pos)
		case KeyCtrlU:
			s := lineStart(e.buf, e.pos)
			e.buf = append(e.buf[:s], e.buf[e.pos:]...)
			e.pos = s
		case KeyCtrlK:
			end := lineEnd(e.buf, e.pos)
			e.buf = append(e.buf[:e.pos], e.buf[end:]...)
		case KeyCtrlW:
			s := wordLeft(e.buf, e.pos)
			e.buf = append(e.buf[:s], e.buf[e.pos:]...)
			e.pos = s
		case KeyCtrlL:
			fmt.Print("\x1b[H\x1b[2J")
			e.cursorRow = 0
		case KeyUp:
			if lineStart(e.buf, e.pos) > 0 {
				e.pos = moveVertical(e.buf, e.pos, -1)
			} else if histIdx > 0 {
				if histIdx == len(e.history) {
					draft = string(e.buf)
				}
				histIdx--
				e.buf = []rune(e.history[histIdx])
				e.pos = len(e.buf)
			}
		case KeyDown:
			if lineEnd(e.buf, e.pos) < len(e.buf) {
				e.pos = moveVertical(e.buf, e.pos, 1)
			} else if histIdx < len(e.history) {
				histIdx++
				if histIdx == len(e.history) {
					e.buf = []rune(draft)
				} else {
					e.buf = []rune(e.history[histIdx])
				}
				e.pos = len(e.buf)
			}
		case KeyTab:
			e.complete(prompt())
		case KeyShiftTab:
			if e.ShiftTab != nil {
				e.ShiftTab()
			}
		}
		e.render(prompt())
	}
}

func (e *Editor) insert(rs []rune) {
	nb := make([]rune, 0, len(e.buf)+len(rs))
	nb = append(nb, e.buf[:e.pos]...)
	nb = append(nb, rs...)
	nb = append(nb, e.buf[e.pos:]...)
	e.buf = nb
	e.pos += len(rs)
}

func (e *Editor) complete(prompt string) {
	if e.Complete == nil {
		return
	}
	prefix := string(e.buf[:e.pos])
	cands := e.Complete(prefix)
	switch len(cands) {
	case 0:
		return
	case 1:
		e.buf = append([]rune(cands[0]), e.buf[e.pos:]...)
		e.pos = len([]rune(cands[0]))
		return
	}
	common := cands[0]
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c, common) {
			common = common[:len(common)-1]
		}
	}
	if len(common) > len(prefix) {
		e.buf = append([]rune(common), e.buf[e.pos:]...)
		e.pos = len([]rune(common))
		return
	}
	// Show the choices below the input, then redraw it.
	e.finish(prompt)
	fmt.Print(Dim(strings.Join(cands, "   ")) + "\r\n")
	e.cursorRow = 0
}

// render redraws the prompt and buffer, handling wrapped and multi-line
// input, and leaves the cursor at the edit position.
func (e *Editor) render(prompt string) {
	cols := max(Width(), 10)
	var b strings.Builder
	if e.cursorRow > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", e.cursorRow)
	}
	b.WriteString("\r\x1b[J")

	lines := strings.Split(string(e.buf), "\n")
	cont := Dim("· ")
	promptW := VisibleLen(prompt)
	contW := VisibleLen(cont)
	row := 0
	curRow, curCol := 0, 0
	consumed := 0 // runes before the current line
	for i, line := range lines {
		lead, leadW := prompt, promptW
		if i > 0 {
			lead, leadW = cont, contW
			b.WriteString("\r\n")
		}
		b.WriteString(lead)
		b.WriteString(line)
		lr := []rune(line)
		w := leadW + VisibleLen(line)
		if w > 0 && w%cols == 0 {
			b.WriteString(" \b") // leave the pending-wrap state
		}
		if e.pos >= consumed && e.pos <= consumed+len(lr) {
			pw := leadW + VisibleLen(string(lr[:e.pos-consumed]))
			curRow, curCol = row+pw/cols, pw%cols
		}
		row += w/cols + 1
		consumed += len(lr) + 1
	}
	endRow := row - 1
	if up := endRow - curRow; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	b.WriteString("\r")
	if curCol > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", curCol)
	}
	e.cursorRow = curRow
	fmt.Print(b.String())
}

// finish moves the cursor below the input so output can follow.
func (e *Editor) finish(prompt string) {
	e.pos = len(e.buf)
	e.render(prompt)
	fmt.Print("\r\n")
	e.cursorRow = 0
}

func lineStart(buf []rune, pos int) int {
	for pos > 0 && buf[pos-1] != '\n' {
		pos--
	}
	return pos
}

func lineEnd(buf []rune, pos int) int {
	for pos < len(buf) && buf[pos] != '\n' {
		pos++
	}
	return pos
}

func moveVertical(buf []rune, pos, dir int) int {
	col := pos - lineStart(buf, pos)
	var start int
	if dir < 0 {
		start = lineStart(buf, lineStart(buf, pos)-1)
	} else {
		start = lineEnd(buf, pos) + 1
	}
	return min(start+col, lineEnd(buf, start))
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func wordLeft(buf []rune, pos int) int {
	for pos > 0 && !isWord(buf[pos-1]) {
		pos--
	}
	for pos > 0 && isWord(buf[pos-1]) {
		pos--
	}
	return pos
}

func wordRight(buf []rune, pos int) int {
	for pos < len(buf) && !isWord(buf[pos]) {
		pos++
	}
	for pos < len(buf) && isWord(buf[pos]) {
		pos++
	}
	return pos
}
