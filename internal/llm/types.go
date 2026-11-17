// Package llm is a client for OpenAI-compatible chat-completion servers:
// Kalon, Ollama, LM Studio, OpenRouter, OpenAI and anything else that
// speaks /v1/chat/completions.
package llm

import "encoding/json"

// Roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names the tool and carries its JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a tool the model may call.
type Tool struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

// FunctionSpec is a tool's name, purpose and JSON Schema parameters.
type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is a chat-completion request.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// Usage is token accounting reported by the server (zero when it reports none).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is a completed (possibly streamed) model reply.
type Response struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

// Delta is one streamed fragment, for live display.
type Delta struct {
	Content   string // answer text
	Reasoning string // thinking text, shown dimmed and never sent back
}

// ModelInfo is one model an endpoint serves.
type ModelInfo struct {
	ID     string
	Detail string   // size, family or capabilities, when the server says
	Caps   []string // capabilities such as "tools", "vision", "thinking"; nil if unknown
}

// Supports reports whether the model advertises a capability. Unknown
// capabilities count as supported.
func (m ModelInfo) Supports(c string) bool {
	if m.Caps == nil {
		return true
	}
	for _, x := range m.Caps {
		if x == c {
			return true
		}
	}
	return false
}
