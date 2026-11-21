package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---- update_plan ----

// UpdatePlan records the agent's step-by-step plan.
type UpdatePlan struct{}

func (UpdatePlan) Name() string { return "update_plan" }
func (UpdatePlan) Kind() Kind   { return KindInteract }
func (UpdatePlan) Description() string {
	return "Set the plan for a multi-step task. Send the whole list each time; status is pending, in_progress or completed. Keep one step in_progress."
}
func (UpdatePlan) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content"]}}},"required":["items"]}`)
}

type planArgs struct {
	Items []PlanItem `json:"items"`
}

func (UpdatePlan) Summary(args json.RawMessage) string {
	var a planArgs
	_ = json.Unmarshal(args, &a)
	done := 0
	for _, it := range a.Items {
		if it.Status == "completed" {
			done++
		}
	}
	return fmt.Sprintf("%d/%d done", done, len(a.Items))
}

func (UpdatePlan) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a planArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if len(a.Items) == 0 {
		return "", errors.New("items is empty")
	}
	inProgress := false
	for i := len(a.Items) - 1; i >= 0; i-- {
		it := &a.Items[i]
		it.Content = strings.TrimSpace(it.Content)
		switch strings.ToLower(strings.ReplaceAll(it.Status, "-", "_")) {
		case "completed", "complete", "done":
			it.Status = "completed"
		case "in_progress", "active", "doing", "started":
			// Only the latest in-progress step stays in progress.
			if inProgress {
				it.Status = "completed"
			} else {
				it.Status = "in_progress"
				inProgress = true
			}
		default:
			it.Status = "pending"
		}
	}
	env.Plan = a.Items
	if env.OnPlan != nil {
		env.OnPlan(a.Items)
	}
	return "Plan updated.", nil
}

// ---- ask_user ----

// AskUser asks the user a clarifying question.
type AskUser struct{}

func (AskUser) Name() string { return "ask_user" }
func (AskUser) Kind() Kind   { return KindInteract }
func (AskUser) Description() string {
	return "Ask the user a question and wait for the answer. Only for real blocking ambiguity you cannot settle from the code or a sensible default. Optional options gives 2-4 choices."
}
func (AskUser) Schema() json.RawMessage {
	return schema([]string{"question"},
		[3]string{"question", "string", "The question"},
		[3]string{"options", "array", "Optional short answer choices"},
	)
}

type askArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func (AskUser) Summary(args json.RawMessage) string {
	var a askArgs
	_ = json.Unmarshal(args, &a)
	return a.Question
}

func (AskUser) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a askArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Question) == "" {
		return "", errors.New("question is required")
	}
	if env.Ask == nil {
		return "No user is available to answer. Proceed with your best assumption and state it in your reply.", nil
	}
	ans, err := env.Ask(ctx, a.Question, a.Options)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ans) == "" {
		return "The user gave no answer. Proceed with your best assumption and state it.", nil
	}
	return "User answered: " + ans, nil
}

// ---- remember ----

// Remember saves a durable memory loaded into future sessions.
type Remember struct{}

func (Remember) Name() string { return "remember" }

// Kind is KindWrite: a memory steers every later session, so in ask mode
// the user approves each one.
func (Remember) Kind() Kind { return KindWrite }
func (Remember) Description() string {
	return "Save a lasting fact for future sessions: a project convention, a user preference, a hard-won gotcha. Not for temporary task state. Reusing a name updates that memory."
}
func (Remember) Schema() json.RawMessage {
	return schema([]string{"name", "content"},
		[3]string{"name", "string", "Short kebab-case name"},
		[3]string{"content", "string", "The fact, in one or a few sentences"},
		[3]string{"scope", "string", "project (default, stored in .kalonide/memory) or user (all projects)"},
	)
}

type rememberArgs struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Scope   string `json:"scope"`
}

func (Remember) Summary(args json.RawMessage) string {
	var a rememberArgs
	_ = json.Unmarshal(args, &a)
	scope := a.Scope
	if scope == "" {
		scope = "project"
	}
	return a.Name + " (" + scope + ")"
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a safe file name: lowercase letters, digits and dashes only.
func Slug(s string) string {
	s = strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

func (Remember) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a rememberArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	slug := Slug(a.Name)
	if slug == "" {
		return "", errors.New("name must contain letters or digits")
	}
	content := strings.TrimSpace(a.Content)
	if content == "" {
		return "", errors.New("content is required")
	}
	dir := env.ProjectMemoryDir
	if strings.EqualFold(a.Scope, "user") {
		dir = env.UserMemoryDir
	}
	if dir == "" {
		return "", errors.New("memory is not available here")
	}
	body := fmt.Sprintf("---\nname: %s\nupdated: %s\n---\n\n%s\n", slug, time.Now().Format("2006-01-02"), content)
	p := filepath.Join(dir, slug+".md")
	if dir == env.ProjectMemoryDir {
		// Project memories live in the workspace: confine them like any
		// other write, so a symlinked .kalonide cannot redirect them.
		resolved, err := env.Resolve(p)
		if err != nil {
			return "", err
		}
		p = resolved
	}
	if err := env.beforeWrite(p); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := writeAtomic(p, []byte(body), 0o644); err != nil {
		return "", err
	}
	return "Saved memory " + slug + ".", nil
}
