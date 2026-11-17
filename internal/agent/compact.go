package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
)

// compactLadder records which shrinking steps were already tried in a run,
// so a context overflow can never loop forever.
type compactLadder struct {
	pruned     bool
	summarized bool
	truncated  bool
}

// EstimateTokens roughly counts tokens in the conversation (about 3.5
// characters per token, rounded up, plus per-message overhead).
func EstimateTokens(msgs []llm.Message) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content) + 16
		for _, c := range m.ToolCalls {
			chars += len(c.Function.Name) + len(c.Function.Arguments) + 16
		}
	}
	return chars*2/7 + 1
}

// ContextTokens estimates the size of the next request.
func (a *Agent) ContextTokens() int {
	req := a.request()
	n := EstimateTokens(req.Messages)
	for _, t := range req.Tools {
		n += (len(t.Function.Description) + len(t.Function.Parameters) + len(t.Function.Name)) * 2 / 7
	}
	return n
}

// maybeCompact shrinks the conversation before it overflows the window.
func (a *Agent) maybeCompact(ctx context.Context, ladder *compactLadder) {
	limit := a.window() * 80 / 100
	for a.ContextTokens() > limit {
		if !a.compactStep(ctx, ladder) {
			return
		}
	}
}

// compactStep runs the next untried step of the ladder: drop old tool
// output (free), summarize older turns (one model call), then truncate the
// largest remaining tool result (lossy last resort). It reports whether
// anything shrank.
func (a *Agent) compactStep(ctx context.Context, ladder *compactLadder) bool {
	if !ladder.pruned {
		ladder.pruned = true
		if n := a.pruneOldToolOutput(); n > 0 {
			a.sink().Notice(fmt.Sprintf("context nearly full: cleared %d old tool outputs", n))
			return true
		}
	}
	if !ladder.summarized {
		ladder.summarized = true
		if err := a.Compact(ctx); err == nil {
			return true
		} else if ctx.Err() == nil {
			a.sink().Notice("could not summarize the conversation: " + err.Error())
		}
	}
	if !ladder.truncated {
		ladder.truncated = true
		if a.truncateLargestToolResult() {
			a.sink().Notice("context still too large: shortened the biggest tool output")
			return true
		}
	}
	return false
}

// pruneOldToolOutput replaces large tool results outside the recent tail
// with a short placeholder. It returns how many were cleared.
func (a *Agent) pruneOldToolOutput() int {
	keep := 8
	n := 0
	for i := 0; i < len(a.Messages)-keep; i++ {
		m := &a.Messages[i]
		if m.Role == llm.RoleTool && len(m.Content) > 400 {
			m.Content = fmt.Sprintf("[old output of %s cleared to save context; %d characters. Run the tool again if you need it.]", m.Name, len(m.Content))
			n++
		}
	}
	return n
}

func (a *Agent) truncateLargestToolResult() bool {
	best, size := -1, 4000
	for i, m := range a.Messages {
		if m.Role == llm.RoleTool && len(m.Content) > size {
			best, size = i, len(m.Content)
		}
	}
	if best < 0 {
		return false
	}
	a.Messages[best].Content = tools.Truncate(a.Messages[best].Content, 3000)
	return true
}

// splitPoint returns the index where the recent tail begins: the start of
// the last user turn, or of the one before it when that still leaves
// something to summarize. Cutting at a user message never separates a tool
// call from its result.
func (a *Agent) splitPoint() int {
	var users []int
	for i, m := range a.Messages {
		if m.Role == llm.RoleUser {
			users = append(users, i)
		}
	}
	if len(users) == 0 {
		return 0
	}
	if len(users) >= 2 && users[len(users)-2] > 0 {
		return users[len(users)-2]
	}
	return users[len(users)-1]
}

const summarizePrompt = `Summarize the conversation below so a coding agent can continue the work without it.
Keep: the user's goals and requests, decisions made, files read or changed (with paths), commands run and their results, errors and how they were fixed, what is done and what remains.
Drop: pleasantries and full file contents. Be specific and complete but compact. Write in plain prose and lists.`

// Compact replaces older turns with a model-written summary.
func (a *Agent) Compact(ctx context.Context) error {
	cut := a.splitPoint()
	if cut <= 0 {
		return fmt.Errorf("nothing old enough to summarize")
	}
	head, tail := a.Messages[:cut], a.Messages[cut:]
	budget := a.window() * 3 // characters: about 85% of the window in tokens
	transcript := renderTranscript(head, budget)
	resp, err := a.Client.Chat(ctx, llm.Request{
		Model: a.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: summarizePrompt},
			{Role: llm.RoleUser, Content: transcript},
		},
	}, nil)
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return fmt.Errorf("the model returned an empty summary")
	}
	before := EstimateTokens(a.Messages)
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "[Summary of the earlier conversation]\n\n" + summary},
		{Role: llm.RoleAssistant, Content: "Understood. I will continue from this summary."},
	}
	a.Messages = append(msgs, tail...)
	a.sink().Notice(fmt.Sprintf("summarized earlier conversation (~%dk → ~%dk tokens)", before/1000, EstimateTokens(a.Messages)/1000))
	return nil
}

// renderTranscript flattens messages to text, keeping the newest content
// when it must cut.
func renderTranscript(msgs []llm.Message, budget int) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var b strings.Builder
		switch m.Role {
		case llm.RoleTool:
			c := m.Content
			if len(c) > 1500 {
				c = c[:1000] + " […] " + c[len(c)-400:]
			}
			fmt.Fprintf(&b, "TOOL RESULT (%s): %s", m.Name, c)
		default:
			fmt.Fprintf(&b, "%s: %s", strings.ToUpper(m.Role), m.Content)
			for _, c := range m.ToolCalls {
				args := c.Function.Arguments
				if len(args) > 600 {
					args = args[:600] + "…"
				}
				fmt.Fprintf(&b, "\n  CALLED %s %s", c.Function.Name, args)
			}
		}
		parts = append(parts, b.String())
	}
	out := strings.Join(parts, "\n\n")
	if len(out) > budget {
		out = "[…earliest part omitted…]\n" + out[len(out)-budget:]
	}
	return out
}
