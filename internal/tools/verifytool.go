package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/sandbox"
	"github.com/KalonEdge/kalonide/internal/verify"
)

// Verify runs the project's own build, lint and test commands.
type Verify struct {
	Root string // when set, Summary names the commands for the approval prompt
}

func (Verify) Name() string { return "verify" }
func (Verify) Kind() Kind   { return KindShell }
func (Verify) Description() string {
	return "Run the project's own checks (build, lint, tests), detected from go.mod, package.json, Cargo.toml, pyproject.toml, Package.swift or the Makefile. Use it after changing code. Stops at the first failing check unless all is true."
}
func (Verify) Schema() json.RawMessage {
	return schema(nil, [3]string{"all", "boolean", "Run every check even after a failure (default false)"})
}

// Summary names the exact commands, so an approval prompt shows what will
// run (the agent may have edited the Makefile or package.json).
func (v Verify) Summary(json.RawMessage) string {
	if v.Root == "" {
		return "project checks"
	}
	return CheckSummary(v.Root)
}

// CheckSummary lists the commands verify would run in root.
func CheckSummary(root string) string {
	checks := verify.Detect(root)
	if len(checks) == 0 {
		return "project checks (none found)"
	}
	labels := make([]string, len(checks))
	for i, c := range checks {
		labels[i] = c.Command
	}
	return strings.Join(labels, " && ")
}

func (Verify) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a struct {
		All bool `json:"all"`
	}
	_ = json.Unmarshal(args, &a)
	checks := verify.Detect(env.Root)
	if len(checks) == 0 {
		return "No checks found (no go.mod, package.json, Cargo.toml, pyproject.toml, Package.swift or Makefile test target). Run the project's tests with bash instead.", nil
	}
	results := verify.Run(ctx, env.Root, checks, 10*time.Minute, a.All, env.SandboxWrapper())
	return verify.Summary(results, len(checks)-len(results)), nil
}

// SandboxWrapper returns a verify.Wrapper for the current sandbox policy,
// or nil when commands run unconfined.
func (e *Env) SandboxWrapper() verify.Wrapper {
	p := e.sandboxPolicy()
	if p == nil {
		return nil
	}
	policy := *p
	return func(argv []string) []string { return sandbox.Wrap(argv, policy) }
}
