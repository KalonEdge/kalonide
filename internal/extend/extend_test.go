package extend

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommandsAndSkills(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	write(t, filepath.Join(user, "commands", "review.md"), "Review the diff.")
	write(t, filepath.Join(user, "commands", "standup.md"), "---\ndescription: Daily notes\n---\nSummarize $ARGUMENTS.")
	write(t, filepath.Join(project, "commands", "review.md"), "---\ndescription: Project review\n---\nReview against CONTRIBUTING.md: $ARGUMENTS")
	write(t, filepath.Join(project, "commands", "Bad Name!.md"), "ignored")
	write(t, filepath.Join(project, "skills", "go-style", "SKILL.md"), "---\nname: go-style\ndescription: How we write Go\n---\nUse table tests.")
	write(t, filepath.Join(project, "skills", "release.md"), "# Cutting a release\nSteps…")

	cmds := Commands(project, user)
	if len(cmds) != 2 {
		t.Fatalf("commands: %+v", cmds)
	}
	review, _ := Find(cmds, "review")
	if review.Scope != "project" || review.Description != "Project review" {
		t.Fatalf("project command must win: %+v", review)
	}
	if got := Expand(review, "  the auth change "); got != "Review against CONTRIBUTING.md: the auth change" {
		t.Fatalf("expand: %q", got)
	}
	standup, _ := Find(cmds, "standup")
	if Expand(standup, "") != "Summarize ." || Expand(Item{Body: "Do it."}, "now") != "Do it.\n\nnow" {
		t.Fatal("expand without placeholder")
	}

	skills := Skills(project, user)
	if len(skills) != 2 || skills[0].Name != "go-style" || skills[0].Body != "Use table tests." || skills[1].Description != "Cutting a release" {
		t.Fatalf("skills: %+v", skills)
	}
}

func TestSymlinksAndControlCharacters(t *testing.T) {
	project := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	write(t, secret, "TOKEN=abc")
	os.MkdirAll(filepath.Join(project, "commands"), 0o755)
	os.Symlink(secret, filepath.Join(project, "commands", "leak.md"))
	write(t, filepath.Join(project, "commands", "ok.md"), "---\ndescription: fine\x1b[2J\x1b[31m red\u202e\n---\nbody\twith tab\x07")
	cmds := Commands(project, "")
	if _, ok := Find(cmds, "leak"); ok {
		t.Fatal("symlink to a file outside the folder was loaded")
	}
	ok, found := Find(cmds, "ok")
	if !found || ok.Description != "fine[2J[31m red" || ok.Body != "body\twith tab" {
		t.Fatalf("control characters not removed: %q %q", ok.Description, ok.Body)
	}
}
