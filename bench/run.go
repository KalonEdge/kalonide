package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// Result is the outcome of one agent run on one task.
type Result struct {
	Agent    string  `json:"agent"`
	Model    string  `json:"model"`
	Task     string  `json:"task"`
	Kind     string  `json:"kind"`
	Passed   bool    `json:"passed"`
	Seconds  float64 `json:"seconds"`
	TimedOut bool    `json:"timed_out,omitempty"`
	Stop     string  `json:"stop,omitempty"` // the agent's own stop reason
	ExitCode int     `json:"exit_code"`
	Turns    int     `json:"turns,omitempty"`
	Tools    int     `json:"tools,omitempty"`
	Tokens   int     `json:"tokens,omitempty"`
	Error    string  `json:"error,omitempty"` // agent error, if any
	Check    string  `json:"check,omitempty"` // check output when it failed
}

// execOutput is the subset of `kalonide exec -o json` the bench reads.
type execOutput struct {
	Model string `json:"model"`
	Stop  string `json:"stop"`
	Text  string `json:"text"`
	Turns int    `json:"turns"`
	Error string `json:"error"`
	Tools []any  `json:"tools"`
	Usage struct {
		Total int `json:"total_tokens"`
	} `json:"usage"`
}

func runTask(ag agentSpec, model string, t Task, timeout time.Duration, maxTurns int) Result {
	r := Result{Agent: ag.Label, Model: model, Task: t.ID, Kind: t.Kind}
	w, err := newWorkspace(t)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer w.cleanup()

	args := []string{"exec", "--yolo", "-o", "json", "--max-turns", strconv.Itoa(maxTurns), "-C", w.repo}
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, t.Prompt)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ag.Bin, args...)
	cmd.Dir = w.repo
	cmd.Env = append(os.Environ(), "KALONIDE_HOME="+w.home, "NO_COLOR=1", "TMPDIR="+w.tmp)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	err = cmd.Run()
	r.Seconds = time.Since(start).Seconds()
	r.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.ExitCode = ee.ExitCode()
	} else if err != nil && !r.TimedOut {
		r.Error = err.Error()
	}

	var out execOutput
	if json.Unmarshal(stdout.Bytes(), &out) == nil {
		r.Stop, r.Turns, r.Tools, r.Tokens = out.Stop, out.Turns, len(out.Tools), out.Usage.Total
		if out.Model != "" {
			r.Model = out.Model
		}
		if out.Error != "" {
			r.Error = out.Error
		}
		os.WriteFile(w.answer, []byte(out.Text), 0o644)
	} else if r.Error == "" && !r.TimedOut {
		r.Error = "unreadable output: " + firstLine(stdout.String()+stderr.String())
	}

	passed, checkOut := w.check(t)
	r.Passed = passed
	if !passed {
		r.Check = clip(checkOut, 1200)
	}
	return r
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
