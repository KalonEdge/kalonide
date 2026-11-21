package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Task is one benchmark task.
type Task struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // fix, feature, refactor, test, answer, shell, docs
	Prompt string `json:"prompt"`
	Dir    string `json:"-"` // absolute task folder
	Num    string `json:"-"` // folder number prefix, e.g. "07"
}

func loadTasks(dir string, only []string) ([]Task, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		td := filepath.Join(abs, e.Name())
		data, err := os.ReadFile(filepath.Join(td, "task.json"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("%s/task.json: %w", e.Name(), err)
		}
		if t.ID == "" || t.Prompt == "" {
			return nil, fmt.Errorf("%s/task.json: id and prompt are required", e.Name())
		}
		for _, need := range []string{"repo", "check.sh"} {
			if _, err := os.Stat(filepath.Join(td, need)); err != nil {
				return nil, fmt.Errorf("%s: missing %s", e.Name(), need)
			}
		}
		t.Dir = td
		t.Num, _, _ = strings.Cut(e.Name(), "-")
		if selected(t, only) {
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Num < tasks[j].Num })
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks matched")
	}
	return tasks, nil
}

func selected(t Task, only []string) bool {
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		if o == t.ID || o == t.Num {
			return true
		}
	}
	return false
}

// workspace is a scratch copy of a task's repo plus side folders.
type workspace struct {
	root   string // removed by cleanup
	repo   string // where the agent works
	home   string // fresh KALONIDE_HOME, so runs never share sessions or memory
	tmp    string // TMPDIR for the check
	answer string // the agent's final reply
}

func newWorkspace(t Task) (*workspace, error) {
	root, err := os.MkdirTemp("", "kbench-"+t.ID+"-")
	if err != nil {
		return nil, err
	}
	w := &workspace{
		root:   root,
		repo:   filepath.Join(root, "repo"),
		home:   filepath.Join(root, "home"),
		tmp:    filepath.Join(root, "tmp"),
		answer: filepath.Join(root, "answer.txt"),
	}
	for _, d := range []string{w.home, w.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := copyTree(filepath.Join(t.Dir, "repo"), w.repo); err != nil {
		return nil, err
	}
	// Resolve symlinks (macOS /var -> /private/var) so paths match what
	// the agent sees.
	if real, err := filepath.EvalSymlinks(w.repo); err == nil {
		w.repo = real
	}
	return w, os.WriteFile(w.answer, nil, 0o644)
}

func (w *workspace) cleanup() { os.RemoveAll(w.root) }

// check restores the hidden tests and runs check.sh.
func (w *workspace) check(t Task) (bool, string) {
	if err := copyTree(filepath.Join(t.Dir, "hidden"), w.repo); err != nil && !os.IsNotExist(err) {
		return false, "restoring hidden tests: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(t.Dir, "check.sh"))
	cmd.Dir = w.repo
	cmd.Env = append(w.env(), "TASK_DIR="+t.Dir, "ANSWER_FILE="+w.answer, "GOFLAGS=-count=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, err := cmd.CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

// copyTree copies the files under src into dst, creating folders and
// overwriting files. A missing src is reported as os.ErrNotExist.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// validateTasks proves each check is sound: it must fail on the untouched
// repo and pass once the reference solution is applied.
func validateTasks(tasks []Task) bool {
	ok := true
	for _, t := range tasks {
		before, beforeOut := validateOnce(t, false)
		after, afterOut := validateOnce(t, true)
		switch {
		case before:
			ok = false
			fmt.Printf("✘ %-24s passes WITHOUT a fix: the check is too weak\n", t.ID)
		case !after:
			ok = false
			fmt.Printf("✘ %-24s fails WITH the reference solution:\n%s\n", t.ID, indent(afterOut))
		default:
			fmt.Printf("✔ %-24s fails before (%s), passes after\n", t.ID, firstLine(beforeOut))
		}
	}
	return ok
}

func validateOnce(t Task, solved bool) (bool, string) {
	w, err := newWorkspace(t)
	if err != nil {
		return false, err.Error()
	}
	defer w.cleanup()
	if solved {
		sol := filepath.Join(t.Dir, "solution")
		if data, err := os.ReadFile(filepath.Join(sol, "ANSWER.txt")); err == nil {
			os.WriteFile(w.answer, data, 0o644)
		} else if err := copyTree(sol, w.repo); err != nil {
			return false, "no solution: " + err.Error()
		}
	}
	return w.check(t)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "ok") {
			if len(l) > 70 {
				l = l[:70] + "…"
			}
			return l
		}
	}
	return "no output"
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}

// env is the environment for the agent and the check: an explicit
// allowlist, never the caller's full environment, so API keys and tokens
// in the shell that started the benchmark cannot reach the agent. HOME is
// the throwaway workspace home; Go keeps the shared build and module
// caches so runs stay fast, and may not download anything.
func (w *workspace) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.home,
		"TMPDIR=" + w.tmp,
		"LANG=en_US.UTF-8",
		"TERM=dumb",
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"GOTELEMETRY=off",
	}
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOROOT"} {
		if v := goEnv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

var goEnvCache = map[string]string{}

// goEnv reads a Go setting once, from the real environment.
func goEnv(key string) string {
	if v, ok := goEnvCache[key]; ok {
		return v
	}
	out, _ := exec.Command("go", "env", key).Output()
	v := strings.TrimSpace(string(out))
	goEnvCache[key] = v
	return v
}
