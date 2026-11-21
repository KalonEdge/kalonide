package sandbox

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitProtectedFollowsWorktreesAndRealPaths(t *testing.T) {
	main := t.TempDir()
	if err := exec.Command("git", "-C", main, "init", "-q").Run(); err != nil {
		t.Skip("git not available")
	}
	exec.Command("git", "-C", main, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x").Run()
	wt := filepath.Join(t.TempDir(), "wt")
	if err := exec.Command("git", "-C", main, "worktree", "add", "-q", wt).Run(); err != nil {
		t.Skip("git worktree not available")
	}
	lit, subs := gitProtected([]string{wt})
	realMain, _ := filepath.EvalSymlinks(main)
	joined := strings.Join(subs, "\n")
	if len(lit) != 1 || !strings.HasSuffix(lit[0], "/wt/.git") {
		t.Fatalf("literals: %v", lit)
	}
	if !strings.Contains(joined, filepath.Join(realMain, ".git", "hooks")) || !strings.Contains(joined, filepath.Join(realMain, ".git", "config")) {
		t.Fatalf("the shared git folder of a worktree is not protected:\n%s", joined)
	}
	for _, p := range append(lit, subs...) {
		if strings.HasPrefix(p, "/var/") {
			t.Fatalf("path not resolved to its real location: %s", p)
		}
	}
	if l, s := gitProtected([]string{t.TempDir()}); len(l)+len(s) != 0 {
		t.Fatal("a folder without git has protected paths")
	}
}
