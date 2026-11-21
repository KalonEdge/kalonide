// Package agent runs the conversation loop: send the conversation to the
// model, run the tools it asks for, feed results back, repeat until it
// answers. It recovers from the common failures of local models and flaky
// servers instead of giving up.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
)

// Chatter sends chat requests. *llm.Client implements it.
type Chatter interface {
	Chat(ctx context.Context, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error)
}

// Gate decides whether a tool call may run. danger is non-empty for calls
// that are destructive and must be explicitly approved.
type Gate interface {
	Allow(ctx context.Context, t tools.Tool, summary, danger string) (bool, error)
}

// AllowAll approves everything except dangerous calls.
type AllowAll struct{}

func (AllowAll) Allow(_ context.Context, _ tools.Tool, _ string, danger string) (bool, error) {
	return danger == "", nil
}

// ToolHooks runs the user's commands around tool calls. Before returns a
// reason to block the call, or ""; After returns notes for the model.
type ToolHooks interface {
	Before(ctx context.Context, tool string, args json.RawMessage) string
	After(ctx context.Context, tool string, args json.RawMessage) string
}

// Sink receives live events for display.
type Sink interface {
	Text(s string)
	Thinking(s string)
	ToolStart(id, name, summary string)
	ToolEnd(id, name, result string, failed bool)
	Notice(s string)
}

// NopSink discards events.
type NopSink struct{}

func (NopSink) Text(string)                          {}
func (NopSink) Thinking(string)                      {}
func (NopSink) ToolStart(string, string, string)     {}
func (NopSink) ToolEnd(string, string, string, bool) {}
func (NopSink) Notice(string)                        {}

// Stop says why a run ended.
type Stop string

const (
	StopDone      Stop = "done"
	StopMaxTurns  Stop = "max_turns"
	StopCancelled Stop = "cancelled"
	StopError     Stop = "error"
)

// Result summarises a run.
type Result struct {
	Text  string // final answer text
	Turns int    // model calls made
	Stop  Stop
	Usage llm.Usage
}

// maxNudges bounds how often one run asks the model to carry on after it
// stopped mid-task (a dropped tool call or an announced but untaken step).
const maxNudges = 3

// ErrMaxTurns is returned when the turn limit is reached.
var ErrMaxTurns = errors.New("stopped: reached the turn limit")

// Agent holds one conversation.
type Agent struct {
	Client    Chatter
	Model     string
	Fallbacks []string // tried in order when the model is overloaded
	Tools     *tools.Registry
	Env       *tools.Env
	Gate      Gate
	Sink      Sink
	Hooks     ToolHooks // optional

	// SystemPrompt builds the system prompt; toolsOn says whether tools
	// will be offered.
	SystemPrompt func(model string, toolsOn bool) string

	MaxTurns      int // per Run (default 60)
	ContextWindow int // tokens (default 32768)
	// MaxContinuations bounds automatic "continue" after an answer is cut
	// off at the output limit (default 3; negative disables).
	MaxContinuations int

	Messages []llm.Message // conversation, without the system prompt
	Usage    llm.Usage     // accumulated
	// Compactions counts summaries that replaced earlier messages; message
	// positions from before one no longer line up.
	Compactions int

	noTools map[string]bool // models that rejected tool calling
	tried   map[string]bool // fallback models already used this run
}

func (a *Agent) sink() Sink {
	if a.Sink == nil {
		return NopSink{}
	}
	return a.Sink
}

func (a *Agent) maxTurns() int {
	if a.MaxTurns > 0 {
		return a.MaxTurns
	}
	return 60
}

func (a *Agent) continuationLimit() int {
	switch {
	case a.MaxContinuations < 0:
		return 0
	case a.MaxContinuations == 0:
		return 3
	}
	return a.MaxContinuations
}

func (a *Agent) window() int {
	if a.ContextWindow >= 2048 {
		return a.ContextWindow
	}
	return 32768
}

func (a *Agent) toolsOn() bool {
	return a.Tools != nil && len(a.Tools.Names()) > 0 && !a.noTools[a.Model]
}

func (a *Agent) system() string {
	if a.SystemPrompt == nil {
		return ""
	}
	return a.SystemPrompt(a.Model, a.toolsOn())
}

func (a *Agent) request() llm.Request {
	msgs := make([]llm.Message, 0, len(a.Messages)+1)
	if s := a.system(); s != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: s})
	}
	msgs = append(msgs, a.Messages...)
	req := llm.Request{Model: a.Model, Messages: msgs}
	if a.toolsOn() {
		req.Tools = a.Tools.Specs()
	}
	return req
}

// Run adds the user's message and works until the model gives a final
// answer, the turn limit is hit, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, input string) (*Result, error) {
	return a.RunMessage(ctx, llm.Message{Role: llm.RoleUser, Content: input})
}

// RunMessage is Run for a prepared user message, for example one with
// images attached.
func (a *Agent) RunMessage(ctx context.Context, msg llm.Message) (*Result, error) {
	msg.Role = llm.RoleUser
	a.Messages = append(a.Messages, msg)
	a.tried = map[string]bool{a.Model: true}
	res := &Result{}
	var ladder compactLadder
	continuations := 0
	nudged := false // empty-reply nudge used
	nudges := 0     // "you stopped mid-task" nudges used
	failures := map[string]int{}

	for res.Turns < a.maxTurns() {
		if err := ctx.Err(); err != nil {
			res.Stop = StopCancelled
			return res, err
		}
		a.maybeCompact(ctx, &ladder)

		res.Turns++
		resp, err := a.call(ctx, &ladder)
		if resp != nil {
			a.addUsage(resp.Usage, res)
		}
		if err != nil {
			if ctx.Err() != nil {
				a.keepPartial(resp, "[interrupted]")
				res.Stop = StopCancelled
				return res, ctx.Err()
			}
			a.keepPartial(resp, "[reply cut off by an error]")
			res.Stop = StopError
			return res, err
		}

		calls := resp.ToolCalls
		content := resp.Content
		if len(calls) == 0 && a.toolsOn() {
			if tc, rest, ok := extractTextToolCalls(content, a.Tools); ok {
				calls, content = tc, rest
			}
		}
		a.Messages = append(a.Messages, llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: calls})

		if len(calls) == 0 {
			if resp.FinishReason == "length" && continuations < a.continuationLimit() {
				continuations++
				a.sink().Notice("reply hit the output limit; asking the model to continue")
				a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: "Your reply was cut off by the output limit. Continue exactly where you stopped. Do not repeat anything."})
				continue
			}
			if a.toolsOn() && resp.ToolCallLost && nudges < maxNudges {
				// The server dropped a tool call the model was making.
				nudges++
				a.sink().Notice("the model's tool call was lost by the server; asking it to send it again")
				a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: "Your tool call did not come through: the reply ended before it was sent. Make the call again now, using the tool-calling interface."})
				continue
			}
			if strings.TrimSpace(content) == "" && !nudged {
				nudged = true
				a.Messages = a.Messages[:len(a.Messages)-1]
				a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: "Your last reply was empty. Please answer or continue the task."})
				continue
			}
			if a.toolsOn() && nudges < maxNudges && announcesNextStep(content) {
				// Small models often say "now I will run the tests" and stop.
				nudges++
				a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: "You said what you will do next but did not do it. Do it now with the tools, then report the result."})
				continue
			}
			res.Text = content
			res.Stop = StopDone
			return res, nil
		}

		for i, call := range calls {
			if ctx.Err() != nil {
				// Keep the history valid: every call needs a result.
				for _, rest := range calls[i:] {
					a.addToolResult(rest, "Cancelled by the user before it ran.")
				}
				res.Stop = StopCancelled
				return res, ctx.Err()
			}
			out := a.runTool(ctx, call, failures)
			a.addToolResult(call, out)
		}
	}
	res.Stop = StopMaxTurns
	return res, ErrMaxTurns
}

func (a *Agent) addUsage(u llm.Usage, res *Result) {
	a.Usage.PromptTokens += u.PromptTokens
	a.Usage.CompletionTokens += u.CompletionTokens
	a.Usage.TotalTokens += u.TotalTokens
	res.Usage.PromptTokens += u.PromptTokens
	res.Usage.CompletionTokens += u.CompletionTokens
	res.Usage.TotalTokens += u.TotalTokens
}

// keepPartial records streamed text from a failed call so the
// conversation shows what the user already saw.
func (a *Agent) keepPartial(resp *llm.Response, note string) {
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return
	}
	a.Messages = append(a.Messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Content + "\n\n" + note})
}

func (a *Agent) addToolResult(call llm.ToolCall, out string) {
	a.Messages = append(a.Messages, llm.Message{
		Role: llm.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: out,
	})
}

// call sends one request, recovering from rejected tools, overload (by
// switching to a fallback model) and context overflow (by compacting).
func (a *Agent) call(ctx context.Context, ladder *compactLadder) (*llm.Response, error) {
	for {
		if w, ok := a.sink().(interface{ Waiting(string) }); ok {
			w.Waiting("working")
		}
		resp, err := a.Client.Chat(ctx, a.request(), func(d llm.Delta) {
			if d.Reasoning != "" {
				a.sink().Thinking(d.Reasoning)
			}
			if d.Content != "" {
				a.sink().Text(d.Content)
			}
		})
		if err == nil || ctx.Err() != nil {
			return resp, err
		}
		streamed := resp != nil && (resp.Content != "" || resp.Reasoning != "")
		if streamed {
			return resp, err
		}
		switch llm.KindOf(err) {
		case llm.KindToolsRejected:
			if a.toolsOn() {
				if a.noTools == nil {
					a.noTools = map[string]bool{}
				}
				a.noTools[a.Model] = true
				a.sink().Notice(fmt.Sprintf("%s does not support tool calling; continuing as plain chat", a.Model))
				continue
			}
		case llm.KindOverloaded, llm.KindServer, llm.KindStalled:
			if next := a.nextFallback(); next != "" {
				a.sink().Notice(fmt.Sprintf("%s is unavailable (%v); switching to %s", a.Model, err, next))
				a.Model = next
				continue
			}
		case llm.KindContextTooLong:
			if a.compactStep(ctx, ladder) {
				continue
			}
		}
		return resp, err
	}
}

func (a *Agent) nextFallback() string {
	for _, m := range a.Fallbacks {
		if m != "" && !a.tried[m] {
			a.tried[m] = true
			return m
		}
	}
	return ""
}

// runTool validates, gates and runs one tool call, always returning text
// for the model (errors included), never failing the run.
func (a *Agent) runTool(ctx context.Context, call llm.ToolCall, failures map[string]int) string {
	name := call.Function.Name
	t, ok := a.Tools.Get(name)
	if !ok || !a.toolsOn() {
		msg := fmt.Sprintf("Error: there is no tool named %q. Available tools: %s.", name, strings.Join(a.Tools.Names(), ", "))
		a.sink().ToolStart(call.ID, name, "")
		a.sink().ToolEnd(call.ID, name, msg, true)
		return msg
	}
	args := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		if fixed, ok := repairJSON(string(args)); ok {
			args = json.RawMessage(fixed)
		} else {
			msg := fmt.Sprintf("Error: the arguments for %s were not valid JSON, so it did not run. Send the call again with valid JSON arguments. You sent: %s", name, tools.Truncate(string(args), 500))
			a.sink().ToolStart(call.ID, name, "")
			a.sink().ToolEnd(call.ID, name, msg, true)
			return msg
		}
	}
	summary := t.Summary(args)
	danger := ""
	if d, ok := t.(tools.Dangerous); ok {
		danger = d.Danger(args)
	}
	a.sink().ToolStart(call.ID, name, summary)
	if pv, ok := t.(tools.Previewer); ok {
		if ps, ok := a.sink().(interface{ ToolPreview(id, name, text string) }); ok {
			if p := pv.Preview(a.Env, args); p != "" {
				ps.ToolPreview(call.ID, name, p)
			}
		}
	}
	if a.Gate != nil {
		allowed, err := a.Gate.Allow(ctx, t, summary, danger)
		if err != nil {
			msg := "Error: could not get permission: " + err.Error()
			a.sink().ToolEnd(call.ID, name, msg, true)
			return msg
		}
		if !allowed {
			msg := "The user did not allow this action, so it did not run. Do not retry the same action. Continue another way, or explain what you need and why."
			if danger != "" {
				msg = fmt.Sprintf("Blocked: this command needs explicit approval (%s) and was not approved. Do not retry it. Find a safer way or ask the user to run it.", danger)
			}
			a.sink().ToolEnd(call.ID, name, msg, true)
			return msg
		}
	}
	if a.Hooks != nil {
		if why := a.Hooks.Before(ctx, name, args); why != "" {
			msg := "Blocked by one of the user's hooks, so it did not run: " + tools.Truncate(why, 2000)
			a.sink().ToolEnd(call.ID, name, msg, true)
			return msg
		}
	}
	out, err := t.Run(ctx, a.Env, args)
	failed := err != nil
	if err != nil {
		out = "Error: " + err.Error()
	}
	out = tools.Truncate(tools.Redact(out), tools.MaxOutput)
	if a.Hooks != nil {
		if note := a.Hooks.After(ctx, name, args); note != "" {
			out += "\n\n" + tools.Truncate(tools.Redact(note), 8000)
		}
	}
	key := name + "\x00" + string(args)
	if failed {
		failures[key]++
		if failures[key] >= 3 {
			out += "\n\nThis exact call has now failed 3 times. Stop repeating it and change approach."
		}
	} else {
		delete(failures, key)
	}
	a.sink().ToolEnd(call.ID, name, out, failed)
	return out
}

// repairJSON fixes the most common ways models break JSON arguments:
// surrounding code fences or prose, and missing closing brackets from a
// cut-off reply.
func repairJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		if json.Valid([]byte(s[:j+1])) {
			return s[:j+1], true
		}
	}
	if json.Valid([]byte(s)) {
		return s, true
	}
	// Close unbalanced strings and brackets.
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	fixed := s
	if inStr {
		fixed += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			fixed += "}"
		} else {
			fixed += "]"
		}
	}
	if json.Valid([]byte(fixed)) {
		return fixed, true
	}
	return "", false
}

// DisableTools stops offering tools to model (it cannot call them).
func (a *Agent) DisableTools(model string) {
	if a.noTools == nil {
		a.noTools = map[string]bool{}
	}
	a.noTools[model] = true
}

// ToolsOn reports whether tools will be offered to the current model.
func (a *Agent) ToolsOn() bool { return a.toolsOn() }

// Reset clears the conversation.
func (a *Agent) Reset() {
	a.Messages = nil
	a.Usage = llm.Usage{}
}
