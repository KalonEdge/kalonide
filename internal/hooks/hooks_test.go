package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHooks(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Root: root, Hooks: []Hook{
		{Event: BeforeTool, Tools: []string{"bash"}, Command: `case "$KALONIDE_ARGS" in *push*) echo "no pushing from the agent"; exit 1;; esac`},
		{Event: AfterTool, Tools: []string{"write_file"}, Command: `echo "formatted $KALONIDE_FILE"`},
		{Event: AfterTool, Tools: []string{"write_file"}, Command: `exit 3`},
		{Event: AfterTool, Command: `echo all-tools`, Disabled: true},
		{Event: TurnEnd, Command: `echo "done: $KALONIDE_STOP"`},
	}}
	ctx := context.Background()
	if why := r.Before(ctx, "bash", json.RawMessage(`{"command":"git push"}`)); why != "no pushing from the agent" {
		t.Fatalf("before: %q", why)
	}
	if why := r.Before(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); why != "" {
		t.Fatalf("allowed call blocked: %q", why)
	}
	if why := r.Before(ctx, "read_file", nil); why != "" {
		t.Fatal("hook ran for the wrong tool")
	}
	note := r.After(ctx, "write_file", json.RawMessage(`{"path":"a/b.go"}`))
	if !strings.Contains(note, "formatted "+root+"/a/b.go") || !strings.Contains(note, "`exit 3` failed") || strings.Contains(note, "all-tools") {
		t.Fatalf("after: %q", note)
	}
	if got := r.End(ctx, "done"); got != "done: done" {
		t.Fatalf("end: %q", got)
	}
	var none *Runner
	if none.Before(ctx, "bash", nil) != "" || none.After(ctx, "x", nil) != "" || none.End(ctx, "") != "" {
		t.Fatal("nil runner must do nothing")
	}
}

func TestHookTimeoutAndValidation(t *testing.T) {
	r := &Runner{Root: t.TempDir(), Hooks: []Hook{{Event: BeforeTool, Command: "sleep 5", Timeout: 1}}}
	if why := r.Before(context.Background(), "bash", nil); !strings.Contains(why, "timed out") {
		t.Fatalf("got %q", why)
	}
	if (Hook{Event: "on_save", Command: "x"}).Valid() == "" || (Hook{Event: TurnEnd}).Valid() == "" || (Hook{Event: TurnEnd, Command: "x"}).Valid() != "" {
		t.Fatal("validation")
	}
}
