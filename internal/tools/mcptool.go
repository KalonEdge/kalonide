package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/KalonEdge/kalonide/internal/mcp"
)

// MCPTool exposes one tool of an MCP server to the model.
type MCPTool struct {
	Client *mcp.Client
	Server string
	Tool   mcp.Tool
}

var reToolChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// MCPToolName builds the model-visible name: mcp__<server>__<tool>, with
// only the characters model APIs accept, at most 64 long.
func MCPToolName(server, tool string) string {
	n := "mcp__" + reToolChars.ReplaceAllString(server, "_") + "__" + reToolChars.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func (t MCPTool) Name() string { return MCPToolName(t.Server, t.Tool.Name) }

func (t MCPTool) Description() string {
	d := strings.TrimSpace(t.Tool.Description)
	if len(d) > 1000 {
		d = d[:1000] + "…"
	}
	return fmt.Sprintf("[from the %s MCP server] %s", t.Server, d)
}

func (t MCPTool) Schema() json.RawMessage {
	if len(t.Tool.InputSchema) == 0 || string(t.Tool.InputSchema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.Tool.InputSchema
}

// Kind trusts the server's read-only hint; anything else needs approval
// outside yolo mode, since an MCP tool could do anything.
func (t MCPTool) Kind() Kind {
	if t.Tool.Annotations.ReadOnlyHint && !t.Tool.Annotations.DestructiveHint {
		return KindRead
	}
	return KindExternal
}

func (t MCPTool) Summary(args json.RawMessage) string {
	s := strings.Join(strings.Fields(string(args)), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return t.Server + "/" + t.Tool.Name + " " + s
}

// Danger makes tools the server marks destructive always ask.
func (t MCPTool) Danger(json.RawMessage) string {
	if t.Tool.Annotations.DestructiveHint {
		return "the " + t.Server + " server marks " + t.Tool.Name + " as destructive"
	}
	return ""
}

func (t MCPTool) Run(ctx context.Context, _ *Env, args json.RawMessage) (string, error) {
	out, isErr, err := t.Client.Call(ctx, t.Tool.Name, args)
	if err != nil {
		return "", fmt.Errorf("%s server: %w", t.Server, err)
	}
	if isErr {
		if strings.TrimSpace(out) == "" {
			out = "the tool reported an error"
		}
		return "", errors.New(out)
	}
	if strings.TrimSpace(out) == "" {
		return "(no output)", nil
	}
	return out, nil
}
