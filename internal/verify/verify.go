// Package verify finds and runs a project's own checks: build, lint and
// tests, from the files that describe the project (go.mod, package.json,
// pyproject.toml, Cargo.toml, Package.swift, Makefile).
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Check is one command that should succeed.
type Check struct {
	ID      string // e.g. go-test
	Label   string // e.g. "go test ./..."
	Command string // run with /bin/sh -c in the project root
}

// Result is the outcome of one check.
type Result struct {
	Check   Check
	Passed  bool
	Output  string // the end of the combined output
	Seconds float64
}

// Detect lists the checks a project defines, in a sensible order: build
// before lint before tests.
func Detect(root string) []Check {
	var out []Check
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	add := func(id, cmd string) { out = append(out, Check{ID: id, Label: cmd, Command: cmd}) }

	// A Makefile's own targets win: they encode the project's way.
	makeTargets := makefileTargets(filepath.Join(root, "Makefile"))
	if makeTargets["test"] || makeTargets["check"] || makeTargets["lint"] {
		for _, t := range []string{"build", "lint", "vet", "check", "test"} {
			if makeTargets[t] {
				add("make-"+t, "make "+t)
			}
		}
		return out
	}
	if has("go.mod") {
		add("go-build", "go build ./...")
		add("go-vet", "go vet ./...")
		add("go-test", "go test ./...")
	}
	if has("package.json") {
		scripts := packageScripts(filepath.Join(root, "package.json"))
		runner := "npm run"
		switch {
		case has("pnpm-lock.yaml"):
			runner = "pnpm run"
		case has("yarn.lock"):
			runner = "yarn"
		case has("bun.lockb"), has("bun.lock"):
			runner = "bun run"
		}
		for _, s := range []string{"build", "typecheck", "lint", "test"} {
			if body, ok := scripts[s]; ok && !strings.Contains(body, "no test specified") {
				add("node-"+s, runner+" "+s)
			}
		}
	}
	if has("Cargo.toml") {
		add("cargo-build", "cargo build")
		add("cargo-test", "cargo test")
	}
	if has("Package.swift") {
		add("swift-build", "swift build")
		if has("Tests") {
			add("swift-test", "swift test")
		}
	}
	if has("pyproject.toml") || has("setup.py") || has("pytest.ini") || has("requirements.txt") || pyTests(root) {
		if _, err := exec.LookPath("pytest"); err == nil && (has("pytest.ini") || has("conftest.py") || mentions(filepath.Join(root, "pyproject.toml"), "pytest")) {
			add("pytest", "pytest -q")
		} else if pyTests(root) {
			add("py-unittest", "python3 -m unittest discover -q")
		}
	}
	return out
}

var reMakeTarget = regexp.MustCompile(`(?m)^([a-zA-Z][\w-]*)\s*:`)

func makefileTargets(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, m := range reMakeTarget.FindAllStringSubmatch(string(data), -1) {
		out[m[1]] = true
	}
	return out
}

func packageScripts(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	_ = json.Unmarshal(data, &pkg)
	return pkg.Scripts
}

func mentions(path, word string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), word)
}

// pyTests reports whether Python test files exist at the top level or in
// a tests folder.
func pyTests(root string) bool {
	for _, pattern := range []string{"test_*.py", "*_test.py", "tests/test_*.py"} {
		if m, _ := filepath.Glob(filepath.Join(root, pattern)); len(m) > 0 {
			return true
		}
	}
	return false
}

// Run executes the checks in order. It stops at the first failure unless
// all is set, because later checks usually fail for the same reason.
func Run(ctx context.Context, root string, checks []Check, timeout time.Duration, all bool) []Result {
	var out []Result
	for _, c := range checks {
		r := runOne(ctx, root, c, timeout)
		out = append(out, r)
		if ctx.Err() != nil || (!r.Passed && !all) {
			break
		}
	}
	return out
}

func runOne(ctx context.Context, root string, c Check, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Command)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	data, err := cmd.CombinedOutput()
	r := Result{Check: c, Passed: err == nil, Seconds: time.Since(start).Seconds()}
	text := strings.TrimSpace(string(data))
	if len(text) > 6000 {
		text = "[…]\n" + text[len(text)-6000:]
	}
	if ctx.Err() == context.DeadlineExceeded {
		text += fmt.Sprintf("\n[timed out after %s]", timeout)
	}
	r.Output = text
	return r
}

// Summary renders results for a person or a model: one line per check,
// with the output of failures.
func Summary(results []Result, skipped int) string {
	var b strings.Builder
	for _, r := range results {
		mark := "PASS"
		if !r.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "%s  %s  (%.1fs)\n", mark, r.Check.Label, r.Seconds)
		if !r.Passed && r.Output != "" {
			b.WriteString(indent(r.Output) + "\n")
		}
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "(%d later check(s) not run after the failure)\n", skipped)
	}
	return strings.TrimRight(b.String(), "\n")
}

// AllPassed reports whether every result passed.
func AllPassed(results []Result) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return len(results) > 0
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
