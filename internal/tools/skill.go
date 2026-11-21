package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KalonEdge/kalonide/internal/extend"
)

// Skill loads one of the user's or project's skills: an instruction pack
// the model reads only when it is relevant.
type Skill struct {
	Skills []extend.Item
}

func (Skill) Name() string { return "skill" }
func (Skill) Kind() Kind   { return KindRead }
func (Skill) Description() string {
	return "Load the full instructions of a skill listed in the system prompt. Use it when a skill matches the task, before starting the work."
}
func (Skill) Schema() json.RawMessage {
	return schema([]string{"name"}, [3]string{"name", "string", "Skill name"})
}

type skillArgs struct {
	Name string `json:"name"`
}

func (Skill) Summary(args json.RawMessage) string {
	var a skillArgs
	_ = json.Unmarshal(args, &a)
	return a.Name
}

func (s Skill) Run(_ context.Context, _ *Env, args json.RawMessage) (string, error) {
	var a skillArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if it, ok := extend.Find(s.Skills, strings.ToLower(strings.TrimSpace(a.Name))); ok {
		return fmt.Sprintf("Skill %s (%s):\n\n%s", it.Name, it.Scope, it.Body), nil
	}
	names := make([]string, len(s.Skills))
	for i, it := range s.Skills {
		names[i] = it.Name
	}
	return "", fmt.Errorf("no skill named %q; available: %s", a.Name, strings.Join(names, ", "))
}
