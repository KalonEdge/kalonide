package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/KalonEdge/kalonide/internal/verify"
)

// Verify runs the project's own build, lint and test commands.
type Verify struct{}

func (Verify) Name() string { return "verify" }
func (Verify) Kind() Kind   { return KindShell }
func (Verify) Description() string {
	return "Run the project's own checks (build, lint, tests), detected from go.mod, package.json, Cargo.toml, pyproject.toml, Package.swift or the Makefile. Use it after changing code. Stops at the first failing check unless all is true."
}
func (Verify) Schema() json.RawMessage {
	return schema(nil, [3]string{"all", "boolean", "Run every check even after a failure (default false)"})
}

func (Verify) Summary(json.RawMessage) string { return "project checks" }

func (Verify) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a struct {
		All bool `json:"all"`
	}
	_ = json.Unmarshal(args, &a)
	checks := verify.Detect(env.Root)
	if len(checks) == 0 {
		return "No checks found (no go.mod, package.json, Cargo.toml, pyproject.toml, Package.swift or Makefile test target). Run the project's tests with bash instead.", nil
	}
	results := verify.Run(ctx, env.Root, checks, 10*time.Minute, a.All)
	return verify.Summary(results, len(checks)-len(results)), nil
}
