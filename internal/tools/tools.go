// Package tools implements the tools the model can call: reading, writing
// and editing files, searching, running shell commands, fetching web pages,
// tracking a plan, asking the user and saving memories.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/KalonEdge/kalonide/internal/llm"
)

// Kind says what a tool can affect. The permission gate decides by Kind.
type Kind int

const (
	KindRead     Kind = iota // reads inside allowed roots; never asks
	KindInteract             // talks to the user or updates the plan; never asks
	KindMemory               // writes Kalonide memory files; never asks
	KindWrite                // creates or changes files
	KindShell                // runs commands
	KindNetwork              // reaches the internet
	KindExternal             // a tool from an MCP server: could do anything
)

func (k Kind) String() string {
	switch k {
	case KindRead:
		return "read"
	case KindInteract:
		return "interact"
	case KindMemory:
		return "memory"
	case KindWrite:
		return "write"
	case KindShell:
		return "shell"
	case KindNetwork:
		return "network"
	case KindExternal:
		return "external"
	}
	return "unknown"
}

// Tool is one model-callable tool.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Kind() Kind
	// Summary is a one-line description of a call, shown to the user and
	// in permission prompts. args has already been validated as JSON.
	Summary(args json.RawMessage) string
	Run(ctx context.Context, env *Env, args json.RawMessage) (string, error)
}

// Dangerous is implemented by tools whose individual calls may be
// destructive (for example `rm -rf`). Such calls always need explicit
// approval.
type Dangerous interface {
	Danger(args json.RawMessage) string // reason, or "" when safe
}

// PlanItem is one step of the agent's plan.
type PlanItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed
}

// Env is what tools need from the outside world.
type Env struct {
	Root       string   // workspace root (absolute, symlinks resolved)
	ExtraRoots []string // extra directories granted with --add-dir

	// Ask poses a question to the user. nil means no user is present.
	Ask func(ctx context.Context, question string, options []string) (string, error)
	// OnPlan is told whenever the plan changes.
	OnPlan func([]PlanItem)

	ProjectMemoryDir string
	UserMemoryDir    string

	// Plan is the current plan.
	Plan []PlanItem

	// Procs tracks background commands; nil means bash cannot start any.
	Procs *Procs

	// BeforeWrite, if set, is called with a file's path just before a tool
	// changes it, so the old content can be kept for undo.
	BeforeWrite func(path string) error
}

// Registry is an ordered set of tools.
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry returns a registry with the given tools.
func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range ts {
		r.Add(t)
	}
	return r
}

// Add registers t, replacing any tool with the same name.
func (r *Registry) Add(t Tool) {
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Get returns the named tool.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns tool names in registration order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// List returns tools in registration order.
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

// Filter keeps only tools allowed by the enable and disable lists. An empty
// enable list allows everything. Unknown names are reported as an error.
func (r *Registry) Filter(enable, disable []string) error {
	var unknown []string
	check := func(names []string) {
		for _, n := range names {
			if _, ok := r.tools[n]; !ok {
				unknown = append(unknown, n)
			}
		}
	}
	check(enable)
	check(disable)
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown tool(s): %s (available: %s)", strings.Join(unknown, ", "), strings.Join(r.order, ", "))
	}
	keep := map[string]bool{}
	for _, n := range r.order {
		keep[n] = len(enable) == 0
	}
	for _, n := range enable {
		keep[n] = true
	}
	for _, n := range disable {
		keep[n] = false
	}
	var order []string
	for _, n := range r.order {
		if keep[n] {
			order = append(order, n)
		} else {
			delete(r.tools, n)
		}
	}
	r.order = order
	return nil
}

// Specs returns the tool definitions sent to the model.
func (r *Registry) Specs() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.order))
	for _, t := range r.List() {
		out = append(out, llm.Tool{Type: "function", Function: llm.FunctionSpec{
			Name: t.Name(), Description: t.Description(), Parameters: t.Schema(),
		}})
	}
	return out
}

// Default returns the standard tool set.
func Default() *Registry {
	return NewRegistry(
		ReadFile{}, ListDirectory{}, Glob{}, Grep{},
		WriteFile{}, EditFile{}, ApplyPatch{},
		Bash{}, BashOutput{}, BashStop{}, Verify{},
		WebFetch{},
		UpdatePlan{}, AskUser{}, Remember{},
	)
}

// SplitList splits "a,b c" into ["a","b","c"].
func SplitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

// decode unmarshals args into v with a helpful error.
func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// schema builds a compact JSON Schema object. props maps a name to
// "type: description"; names in required must be present.
func schema(required []string, props ...[3]string) json.RawMessage {
	type prop struct {
		Type        string         `json:"type"`
		Description string         `json:"description,omitempty"`
		Items       map[string]any `json:"items,omitempty"`
	}
	p := map[string]prop{}
	for _, x := range props {
		pr := prop{Type: x[1], Description: x[2]}
		if x[1] == "array" {
			pr.Items = map[string]any{"type": "string"}
		}
		p[x[0]] = pr
	}
	if required == nil {
		required = []string{}
	}
	out, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required})
	return out
}

// Truncate shortens s to at most max bytes by keeping the head and tail,
// noting how much was cut. It never splits a UTF-8 sequence.
func Truncate(s string, max int) string {
	if len(s) <= max || max < 200 {
		return s
	}
	head := max * 2 / 3
	tail := max - head - 80
	for head > 0 && !isRuneStart(s[head]) {
		head--
	}
	start := len(s) - tail
	for start < len(s) && !isRuneStart(s[start]) {
		start++
	}
	cut := start - head
	return s[:head] + fmt.Sprintf("\n\n[… %d bytes omitted …]\n\n", cut) + s[start:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// MaxOutput is the most a single tool result may send back to the model.
const MaxOutput = 30000

// beforeWrite runs the BeforeWrite hook, if any.
func (e *Env) beforeWrite(path string) error {
	if e.BeforeWrite == nil {
		return nil
	}
	if err := e.BeforeWrite(path); err != nil {
		return fmt.Errorf("could not save %s for undo, so it was not changed: %w", e.Rel(path), err)
	}
	return nil
}
