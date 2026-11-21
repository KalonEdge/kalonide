package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/tools"
)

// TaskTool lets the model hand a focused, read-only job to a helper agent
// with a fresh context: searching the code, reading many files to answer
// a question, reviewing a file. Only the helper's final answer comes
// back, which keeps the main conversation small.
type TaskTool struct {
	Parent   *Agent // set once the main agent exists
	MaxTurns int    // per task (default 15)
}

func (*TaskTool) Name() string     { return "task" }
func (*TaskTool) Kind() tools.Kind { return tools.KindRead }
func (*TaskTool) Description() string {
	return "Hand a focused read-only job to a helper agent with a fresh context, e.g. find where something is implemented, read many files to answer a question, or review a file. It returns only its final answer. It cannot edit files or run commands. Give it complete instructions: it does not see this conversation."
}
func (*TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"description":{"type":"string","description":"A few words naming the job"},"prompt":{"type":"string","description":"Full instructions, including what to report back"}},"required":["description","prompt"]}`)
}

type taskArgs struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

func (*TaskTool) Summary(args json.RawMessage) string {
	var a taskArgs
	_ = json.Unmarshal(args, &a)
	if a.Description != "" {
		return a.Description
	}
	return a.Prompt
}

const helperPrompt = `You are a helper working for another coding agent, in the user's project folder.
Do the job you are given using your read-only tools (repo_map, read_file, list_directory, glob, grep), then reply with a concise, complete answer: what you found, with file paths and line numbers. Search before reading many files. You cannot edit files or run commands, and the other agent sees only your final reply.

Workspace: %s
Date: %s
`

func (t *TaskTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) (string, error) {
	var a taskArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", errors.New("prompt is required")
	}
	p := t.Parent
	if p == nil {
		return "", errors.New("task is not available here")
	}
	turns := t.MaxTurns
	if turns <= 0 {
		turns = 15
	}
	helper := &Agent{
		Client:        p.Client,
		Model:         p.Model,
		Tools:         tools.NewRegistry(tools.ReadFile{}, tools.ListDirectory{}, tools.Glob{}, tools.Grep{}, tools.RepoMap{}),
		Env:           &tools.Env{Root: env.Root, ExtraRoots: env.ExtraRoots},
		Gate:          AllowAll{},
		Hooks:         p.Hooks, // the user's hooks apply to the helper's calls too
		Sink:          &helperSink{parent: p.sink()},
		MaxTurns:      turns,
		ContextWindow: p.ContextWindow,
		SystemPrompt: func(string, bool) string {
			return fmt.Sprintf(helperPrompt, env.Root, time.Now().Format("2 January 2006"))
		},
	}
	res, err := helper.Run(ctx, a.Prompt)
	if res == nil {
		return "", err
	}
	p.Usage.PromptTokens += res.Usage.PromptTokens
	p.Usage.CompletionTokens += res.Usage.CompletionTokens
	p.Usage.TotalTokens += res.Usage.TotalTokens
	answer := strings.TrimSpace(res.Text)
	switch {
	case errors.Is(err, ErrMaxTurns):
		answer = "The helper ran out of turns before finishing. What it had so far:\n\n" + lastAssistant(helper)
	case err != nil:
		return "", fmt.Errorf("helper stopped: %w", err)
	case answer == "":
		answer = "The helper finished without an answer."
	}
	return fmt.Sprintf("%s\n\n(helper: %d model calls)", answer, res.Turns), nil
}

func lastAssistant(a *Agent) string {
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if m := a.Messages[i]; m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return "(nothing)"
}

// helperSink shows the helper's tool calls under the main agent's output,
// marked as the helper's; its text and thinking stay hidden.
type helperSink struct{ parent Sink }

func (s *helperSink) Text(string)     {}
func (s *helperSink) Thinking(string) {}
func (s *helperSink) ToolStart(id, name, summary string) {
	s.parent.ToolStart("task-"+id, "task › "+name, summary)
}
func (s *helperSink) ToolEnd(id, name, result string, failed bool) {
	s.parent.ToolEnd("task-"+id, "task › "+name, result, failed)
}
func (s *helperSink) Notice(n string) { s.parent.Notice("helper: " + n) }

// Waiting keeps the spinner going while the helper thinks.
func (s *helperSink) Waiting(label string) {
	if w, ok := s.parent.(interface{ Waiting(string) }); ok {
		w.Waiting("helper working")
	}
}
