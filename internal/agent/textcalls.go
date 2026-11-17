package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
)

// Small local models often write a tool call into their reply text instead
// of using the tool-calling interface. These patterns recover such calls.
var (
	reTagCall   = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*(?:</tool_call>|$)`)
	reFenceCall = regexp.MustCompile("(?s)```(?:json|tool_call|tool)?\\s*(\\{.*?\\}|\\[.*?\\])\\s*```")
)

// extractTextToolCalls finds tool calls written as text. It only accepts
// calls to registered tools, and returns the reply with the calls removed.
func extractTextToolCalls(content string, reg *tools.Registry) ([]llm.ToolCall, string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, content, false
	}
	var calls []llm.ToolCall
	rest := content
	try := func(blob string) bool {
		found := parseCallBlob(blob, reg)
		calls = append(calls, found...)
		return len(found) > 0
	}
	for _, m := range reTagCall.FindAllStringSubmatch(content, -1) {
		if try(m[1]) {
			rest = strings.Replace(rest, m[0], "", 1)
		}
	}
	if len(calls) == 0 {
		for _, m := range reFenceCall.FindAllStringSubmatch(content, -1) {
			if try(m[1]) {
				rest = strings.Replace(rest, m[0], "", 1)
			}
		}
	}
	if len(calls) == 0 && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		if try(trimmed) {
			rest = ""
		}
	}
	if len(calls) == 0 {
		return nil, content, false
	}
	for i := range calls {
		calls[i].ID = fmt.Sprintf("text_%d_%d", time.Now().UnixNano()%1e9, i)
		calls[i].Type = "function"
	}
	return calls, strings.TrimSpace(rest), true
}

// parseCallBlob accepts the shapes models use:
// {"name":…, "arguments":…}, {"name":…, "parameters":…},
// {"tool":…, "args":…}, {"function":{"name":…, "arguments":…}}, or a list.
func parseCallBlob(blob string, reg *tools.Registry) []llm.ToolCall {
	blob = strings.TrimSpace(blob)
	if strings.HasPrefix(blob, "[") {
		var list []json.RawMessage
		if json.Unmarshal([]byte(blob), &list) != nil {
			return nil
		}
		var out []llm.ToolCall
		for _, item := range list {
			out = append(out, parseCallBlob(string(item), reg)...)
		}
		return out
	}
	var v struct {
		Name       string          `json:"name"`
		Tool       string          `json:"tool"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
		Args       json.RawMessage `json:"args"`
		Input      json.RawMessage `json:"input"`
		Function   *struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(blob), &v) != nil {
		return nil
	}
	name, args := v.Name, v.Arguments
	if name == "" {
		name = v.Tool
	}
	if v.Function != nil && name == "" {
		name, args = v.Function.Name, v.Function.Arguments
	}
	for _, alt := range []json.RawMessage{v.Parameters, v.Args, v.Input} {
		if len(args) == 0 {
			args = alt
		}
	}
	if _, ok := reg.Get(name); !ok {
		return nil
	}
	argStr := "{}"
	if len(args) > 0 && string(args) != "null" {
		var s string
		if json.Unmarshal(args, &s) == nil {
			argStr = s
		} else {
			argStr = string(args)
		}
	}
	return []llm.ToolCall{{Function: llm.FunctionCall{Name: name, Arguments: argStr}}}
}
