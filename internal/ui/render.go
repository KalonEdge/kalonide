package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KalonEdge/kalonide/internal/tools"
)

// Renderer prints agent activity live: streamed text, dimmed thinking,
// one line per tool call with a short result, and plan updates.
type Renderer struct {
	Out          io.Writer
	ShowThinking bool

	mu       sync.Mutex
	inText   bool // last output was answer text
	inThink  bool // last output was thinking text
	lineOpen bool // cursor is not at the start of a line
	started  map[string]time.Time
	spinner  *spinner

	// Answer text streams raw; each finished line is redrawn formatted.
	md      mdState
	partial string // the unfinished line, as printed so far
}

// NewRenderer writes to stdout.
func NewRenderer() *Renderer {
	return &Renderer{Out: os.Stdout, ShowThinking: true, started: map[string]time.Time{}}
}

func (r *Renderer) write(s string) {
	if s == "" {
		return
	}
	fmt.Fprint(r.Out, s)
	r.lineOpen = !strings.HasSuffix(s, "\n")
}

func (r *Renderer) newline() {
	if r.lineOpen {
		r.write("\n")
	}
}

// Waiting shows a spinner until the first output arrives.
func (r *Renderer) Waiting(label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinner()
	if !colorOn {
		return
	}
	r.newline()
	r.spinner = startSpinner(r.Out, label)
}

func (r *Renderer) stopSpinner() {
	if r.spinner != nil {
		r.spinner.stop()
		r.spinner = nil
	}
}

func (r *Renderer) Text(s string) {
	s = Sanitize(s)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinner()
	if r.inThink {
		r.write("\n\n")
		r.inThink = false
	}
	r.inText = true
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			r.partial += s
			r.write(s)
			return
		}
		r.partial += s[:i]
		r.write(s[:i])
		r.finishLine()
		s = s[i+1:]
	}
}

// finishLine redraws the current answer line with markdown formatting and
// ends it. The raw line may have wrapped, so move up over every row it took.
func (r *Renderer) finishLine() {
	styled := styleLine(r.partial, &r.md)
	if styled != r.partial {
		cols := max(Width(), 1)
		rows := 0
		if w := VisibleLen(r.partial); w > 0 {
			rows = (w - 1) / cols
		}
		r.write("\r")
		if rows > 0 {
			r.write(fmt.Sprintf("\x1b[%dA", rows))
		}
		r.write("\x1b[J" + styled)
	}
	r.write("\n")
	r.partial = ""
}

// flushPartial completes a half-finished answer line before other output.
func (r *Renderer) flushPartial() {
	if r.partial != "" {
		r.finishLine()
	}
}

func (r *Renderer) Thinking(s string) {
	s = Sanitize(s)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	if !r.ShowThinking {
		return
	}
	r.stopSpinner()
	if !r.inThink {
		r.newline()
		r.write(Dim(Italic("thinking… ")))
		r.inThink = true
		r.inText = false
	}
	r.write(Dim(s))
}

func (r *Renderer) ToolStart(id, name, summary string) {
	name, summary = Sanitize(name), Sanitize(summary)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	r.stopSpinner()
	r.newline()
	if r.inText || r.inThink {
		r.write("\n")
	}
	r.inText, r.inThink = false, false
	r.started[id] = time.Now()
	r.write(Accent("● ") + Bold(name) + " " + Dim(clip(oneLine(summary), max(Width()-len(name)-4, 20))) + "\n")
}

func (r *Renderer) ToolEnd(id, name, result string, failed bool) {
	name, result = Sanitize(name), Sanitize(result)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinner()
	el := time.Since(r.started[id])
	delete(r.started, id)
	line := resultLine(name, result)
	if el > 2*time.Second {
		line += fmt.Sprintf(" · %.0fs", el.Seconds())
	}
	if failed {
		r.write("  " + Red("⎿ "+clip(line, Width()-6)) + "\n")
	} else {
		r.write("  " + Dim("⎿ "+clip(line, Width()-6)) + "\n")
	}
}

func (r *Renderer) Notice(s string) {
	s = Sanitize(s)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	r.stopSpinner()
	r.newline()
	r.write(Yellow("! ") + Dim(s) + "\n")
}

// Plan prints the current plan.
func (r *Renderer) Plan(items []tools.PlanItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	r.stopSpinner()
	r.newline()
	for _, it := range items {
		it.Content = Sanitize(it.Content)
		switch it.Status {
		case "completed":
			r.write("  " + Green("✔ ") + Dim(it.Content) + "\n")
		case "in_progress":
			r.write("  " + Accent("◐ ") + Bold(it.Content) + "\n")
		default:
			r.write("  " + Dim("○ "+it.Content) + "\n")
		}
	}
}

// End finishes a turn's output on a fresh line.
func (r *Renderer) End() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	r.stopSpinner()
	r.newline()
	r.inText, r.inThink = false, false
	r.md = mdState{}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// resultLine condenses a tool result to one informative line.
func resultLine(name, result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return "done"
	}
	lines := strings.Split(result, "\n")
	switch name {
	case "read_file":
		if strings.HasSuffix(lines[len(lines)-1], "to read more)") {
			return strings.Trim(lines[len(lines)-1], "()")
		}
		return fmt.Sprintf("%d lines", len(lines))
	case "glob", "list_directory":
		if len(lines) == 1 {
			return lines[0]
		}
		return fmt.Sprintf("%d entries", countNonEmpty(lines))
	case "grep":
		last := lines[len(lines)-1]
		if strings.HasPrefix(last, "(") {
			return strings.Trim(last, "()")
		}
		return lines[0]
	case "bash":
		last := lines[len(lines)-1]
		if len(lines) > 1 {
			return fmt.Sprintf("%s · %d lines of output", strings.Trim(last, "[]"), len(lines)-1)
		}
		return strings.Trim(last, "[]")
	case "web_fetch":
		return fmt.Sprintf("%d characters", len(result))
	}
	return oneLine(lines[0])
}

func countNonEmpty(ls []string) int {
	n := 0
	for _, l := range ls {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "(") {
			n++
		}
	}
	return n
}

type spinner struct {
	done chan struct{}
	wg   sync.WaitGroup
}

func startSpinner(out io.Writer, label string) *spinner {
	s := &spinner{done: make(chan struct{})}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	start := time.Now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			el := ""
			if d := time.Since(start); d > 3*time.Second {
				el = fmt.Sprintf(" %ds", int(d.Seconds()))
			}
			fmt.Fprintf(out, "\r%s %s\x1b[K", Accent(frames[i%len(frames)]), Dim(label+el))
			select {
			case <-s.done:
				fmt.Fprint(out, "\r\x1b[K")
				return
			case <-t.C:
			}
		}
	}()
	return s
}

func (s *spinner) stop() {
	close(s.done)
	s.wg.Wait()
}

// ToolPreview shows the diff a file change will make, before it runs.
func (r *Renderer) ToolPreview(id, name, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPartial()
	r.stopSpinner()
	for _, line := range strings.Split(Sanitize(text), "\n") {
		line = clip(line, max(Width()-6, 20))
		switch {
		case strings.HasPrefix(line, "-"):
			line = Red(line)
		case strings.HasPrefix(line, "+"):
			line = Green(line)
		default:
			line = Dim(line)
		}
		r.write("    " + line + "\n")
	}
}
