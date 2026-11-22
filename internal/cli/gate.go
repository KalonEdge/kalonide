package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/ui"
)

// gate is the permission policy. Reads never ask. Writes, shell and
// network depend on the mode; destructive commands always need a
// one-time yes from a person.
type gate struct {
	mu          sync.Mutex
	mode        string
	always      map[string]bool // tools approved for the rest of the session
	interactive bool
	term        *ui.Terminal
	render      *ui.Renderer
	denied      int // calls refused, reported after headless runs
}

func newGate(mode string, interactive bool, term *ui.Terminal, r *ui.Renderer) *gate {
	return &gate{mode: mode, always: map[string]bool{}, interactive: interactive, term: term, render: r}
}

func (g *gate) Mode() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mode
}

func (g *gate) SetMode(m string) {
	g.mu.Lock()
	g.mode = m
	g.mu.Unlock()
}

// Cycle moves to the next mode: ask → auto-edit → yolo → ask.
func (g *gate) Cycle() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.mode {
	case config.ModeAsk:
		g.mode = config.ModeAutoEdit
	case config.ModeAutoEdit:
		g.mode = config.ModeYolo
	default:
		g.mode = config.ModeAsk
	}
	return g.mode
}

func (g *gate) Allow(ctx context.Context, t tools.Tool, summary, danger string) (bool, error) {
	kind := t.Kind()
	if kind == tools.KindRead || kind == tools.KindInteract || kind == tools.KindMemory {
		return true, nil
	}
	g.mu.Lock()
	mode := g.mode
	always := g.always[t.Name()]
	g.mu.Unlock()

	if danger != "" {
		if !g.interactive {
			g.countDenied()
			return false, nil
		}
		return g.ask(t, summary, danger, false)
	}
	switch {
	case always:
		return true, nil
	case mode == config.ModeYolo:
		return true, nil
	case mode == config.ModeAutoEdit && kind == tools.KindWrite:
		return true, nil
	case !g.interactive:
		g.countDenied()
		return false, nil
	}
	return g.ask(t, summary, "", true)
}

func (g *gate) countDenied() {
	g.mu.Lock()
	g.denied++
	g.mu.Unlock()
}

func (g *gate) ask(t tools.Tool, summary, danger string, offerAlways bool) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.render != nil {
		g.render.End()
	}
	// Show exactly what will run, sanitized so it cannot disguise itself.
	// Show exactly what will run, in full: never approve hidden text.
	shown := ui.Sanitize(summary)
	if len(shown) > maxReviewable {
		if g.render != nil {
			g.render.Notice(fmt.Sprintf("refused %s: the command is %d KB, too long to review", t.Name(), len(shown)>>10))
		}
		return false, nil
	}
	q := fmt.Sprintf("  %s %s: %s\n ", ui.Yellow("Allow"), ui.Bold(t.Name()), shown)
	if danger != "" {
		q = fmt.Sprintf("  %s %s\n  %s\n ", ui.Red("Destructive ("+danger+"), cannot be auto-approved:"), ui.Bold(shown), ui.Bold("Run it?"))
	}
	choices := []ui.Choice{{Key: 'y', Label: "yes"}}
	if offerAlways {
		choices = append(choices, ui.Choice{Key: 'a', Label: "always (" + t.Name() + ", this session)"})
	}
	choices = append(choices, ui.Choice{Key: 'n', Label: "no"})
	if danger != "" {
		// Make "no" the Enter default for destructive commands.
		choices = []ui.Choice{{Key: 'n', Label: "no"}, {Key: 'y', Label: "yes"}}
	}
	key, err := ui.Confirm(g.term, q, choices)
	_ = g.term.Quiet() // back to the working mode
	if err != nil {
		return false, err
	}
	switch key {
	case 'y':
		return true, nil
	case 'a':
		g.always[t.Name()] = true
		return true, nil
	}
	return false, nil
}

// maxReviewable is the longest command a person is asked to approve;
// anything longer is refused rather than shown in part.
const maxReviewable = 16 << 10
