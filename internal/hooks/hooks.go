// Package hooks runs the user's own commands around the agent's work:
// before a tool call (and able to block it), after a tool call (its output
// goes back to the model, e.g. a formatter's or linter's report), and when
// a request is done. Hooks come only from the user's settings, never from
// a project folder, so a downloaded repository cannot add one.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Events.
const (
	BeforeTool = "before_tool"
	AfterTool  = "after_tool"
	TurnEnd    = "turn_end"
)

// Hook is one entry of "hooks" in settings.json.
type Hook struct {
	Event    string   `json:"event"`
	Tools    []string `json:"tools,omitempty"` // tool names; empty means all (tool events only)
	Command  string   `json:"command"`
	Timeout  int      `json:"timeout,omitempty"` // seconds, default 60
	Disabled bool     `json:"disabled,omitempty"`
}

// Valid reports a problem with the hook's settings, or "".
func (h Hook) Valid() string {
	switch h.Event {
	case BeforeTool, AfterTool, TurnEnd:
	default:
		return fmt.Sprintf("unknown event %q (use before_tool, after_tool or turn_end)", h.Event)
	}
	if strings.TrimSpace(h.Command) == "" {
		return "command is empty"
	}
	return ""
}

func (h Hook) matches(event, tool string) bool {
	if h.Disabled || h.Event != event {
		return false
	}
	if len(h.Tools) == 0 {
		return true
	}
	for _, t := range h.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// Runner runs hooks for one workspace.
type Runner struct {
	Hooks []Hook
	Root  string // the workspace; hooks run there
	// Resolve checks a tool's path the way the tools do (allowed roots,
	// symlinks). KALONIDE_FILE is only set for paths it accepts.
	Resolve func(path string) (string, error)
}

// Result is what one hook run produced.
type Result struct {
	Output string
	Failed bool
}

// run executes a hook with the event's details in the environment.
func (r *Runner) run(ctx context.Context, h Hook, env map[string]string) Result {
	timeout := 60 * time.Second
	if h.Timeout > 0 {
		timeout = time.Duration(h.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
	cmd.Dir = r.Root
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if len(text) > 8000 {
		text = text[:8000] + "\n[… hook output cut]"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{Output: fmt.Sprintf("hook timed out after %s", timeout), Failed: true}
	}
	return Result{Output: text, Failed: err != nil}
}

func (r *Runner) toolEnv(event, tool string, args json.RawMessage) map[string]string {
	env := map[string]string{
		"KALONIDE_EVENT":     event,
		"KALONIDE_TOOL":      tool,
		"KALONIDE_ARGS":      string(args),
		"KALONIDE_WORKSPACE": r.Root,
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) == nil && a.Path != "" && r.Resolve != nil {
		if p, err := r.Resolve(a.Path); err == nil {
			env["KALONIDE_FILE"] = p
		}
	}
	return env
}

// Before runs before_tool hooks. A hook that fails blocks the call; its
// output is returned as the reason.
func (r *Runner) Before(ctx context.Context, tool string, args json.RawMessage) string {
	if r == nil {
		return ""
	}
	for i, h := range r.Hooks {
		if !h.matches(BeforeTool, tool) {
			continue
		}
		if res := r.run(ctx, h, r.toolEnv(BeforeTool, tool, args)); res.Failed {
			reason := res.Output
			if reason == "" {
				reason = fmt.Sprintf("hook %d refused it", i+1)
			}
			return reason
		}
	}
	return ""
}

// After runs after_tool hooks and returns their combined output, for the
// model to see.
func (r *Runner) After(ctx context.Context, tool string, args json.RawMessage) string {
	if r == nil {
		return ""
	}
	// Hooks are named by number: a command line can contain secrets and
	// is not the model's business.
	var notes []string
	for i, h := range r.Hooks {
		if !h.matches(AfterTool, tool) {
			continue
		}
		res := r.run(ctx, h, r.toolEnv(AfterTool, tool, args))
		switch {
		case res.Failed && res.Output != "":
			notes = append(notes, fmt.Sprintf("Hook %d failed:\n%s", i+1, res.Output))
		case res.Failed:
			notes = append(notes, fmt.Sprintf("Hook %d failed.", i+1))
		case res.Output != "":
			notes = append(notes, fmt.Sprintf("Hook %d:\n%s", i+1, res.Output))
		}
	}
	return strings.Join(notes, "\n\n")
}

// End runs turn_end hooks and returns their output, for the user.
func (r *Runner) End(ctx context.Context, stop string) string {
	if r == nil {
		return ""
	}
	var notes []string
	for _, h := range r.Hooks {
		if !h.matches(TurnEnd, "") {
			continue
		}
		res := r.run(ctx, h, map[string]string{"KALONIDE_EVENT": TurnEnd, "KALONIDE_STOP": stop, "KALONIDE_WORKSPACE": r.Root})
		if res.Output != "" {
			notes = append(notes, res.Output)
		}
	}
	return strings.Join(notes, "\n")
}
