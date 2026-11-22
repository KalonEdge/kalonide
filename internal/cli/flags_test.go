package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/KalonEdge/kalonide/internal/tools"
)

func TestFlagsMixWithArguments(t *testing.T) {
	var model, out string
	var yolo, resume bool
	var resumeID string
	var dirs []string
	var n int
	f := newFlags("exec")
	f.String(&model, "", "", "m", "model")
	f.String(&out, "", "", "o", "output")
	f.Bool(&yolo, "", "yolo")
	f.Optional(&resume, &resumeID, "", "", "resume")
	f.List(&dirs, "", "", "add-dir")
	f.Int(&n, "", "", "max-turns")
	err := f.Parse([]string{"fix", "--model", "q", "the", "-o=json", "--yolo", "--resume", "bug", "--add-dir", "a", "--add-dir=b", "--max-turns", "5", "--", "--literal"})
	if err != nil {
		t.Fatal(err)
	}
	if model != "q" || out != "json" || !yolo || !resume || resumeID != "" || len(dirs) != 2 || n != 5 {
		t.Fatalf("model=%q out=%q yolo=%v resume=%v id=%q dirs=%v n=%d", model, out, yolo, resume, resumeID, dirs, n)
	}
	want := []string{"fix", "the", "bug", "--literal"}
	if len(f.args) != len(want) {
		t.Fatalf("args %v", f.args)
	}
	for i := range want {
		if f.args[i] != want[i] {
			t.Fatalf("args %v", f.args)
		}
	}
	if err := f.Parse([]string{"--resume=abc"}); err != nil || resumeID != "abc" {
		t.Fatal(err, resumeID)
	}
	if err := f.Parse([]string{"--nope"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := f.Parse([]string{"--model"}); err == nil {
		t.Fatal("missing value accepted")
	}
}

func TestTooLongToReviewIsRefused(t *testing.T) {
	g := &acpGate{srv: newACPServer(io.Discard), sess: &acpSession{id: "s"}}
	huge := strings.Repeat("echo x; ", 3000) // ~24 KB
	ok, err := g.Allow(context.Background(), tools.Bash{}, huge, "")
	if ok || err != nil {
		t.Fatalf("an unreviewably long command was allowed: %v %v", ok, err)
	}
}
