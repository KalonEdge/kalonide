package tools

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KalonEdge/kalonide/internal/sandbox"
)

func TestSandboxConfinesCommands(t *testing.T) {
	if ok, _ := sandbox.Available(); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS sandbox-exec")
	}
	env := newEnv(t)
	policy := &sandbox.Policy{Writable: []string{env.Root}}
	ctx := context.Background()

	out, _ := RunCommand(ctx, env.Root, "echo ok > inside.txt && cat inside.txt", time.Minute, policy)
	if !strings.Contains(out, "ok") || !strings.Contains(out, "exit code 0") {
		t.Fatalf("write inside the workspace failed: %s", out)
	}

	home, _ := os.UserHomeDir()
	outside := filepath.Join(home, fmt.Sprintf(".kalonide-sandbox-test-%d", os.Getpid()))
	defer os.Remove(outside)
	out, _ = RunCommand(ctx, env.Root, "echo leak > "+outside, time.Minute, policy)
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("sandboxed command wrote outside the workspace: %s", out)
	}
	if !strings.Contains(out, "Operation not permitted") {
		t.Fatalf("expected a permission error, got: %s", out)
	}

	// Network: other machines blocked, this machine allowed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	out, _ = RunCommand(ctx, env.Root, fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d && echo local-ok", port), time.Minute, policy)
	if !strings.Contains(out, "local-ok") {
		t.Fatalf("localhost blocked: %s", out)
	}
	out, _ = RunCommand(ctx, env.Root, "exec 3<>/dev/tcp/192.0.2.1/80 && echo remote-ok", 20*time.Second, policy)
	if strings.Contains(out, "remote-ok") || !strings.Contains(out, "not permitted") {
		t.Fatalf("remote network allowed: %s", out)
	}

	// Unconfined when no policy applies.
	out, _ = RunCommand(ctx, env.Root, "echo free > "+outside, time.Minute, nil)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("unsandboxed command was confined: %s", out)
	}

	// Background commands are confined too.
	env.Procs = &Procs{}
	defer env.Procs.StopAll()
	env.Sandbox = func() *sandbox.Policy { return policy }
	os.Remove(outside)
	run(t, Bash{}, env, map[string]any{"command": "echo leak > " + outside + "; echo done", "background": true})
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("background command escaped the sandbox")
	}
}

func TestSandboxBlocksEscapes(t *testing.T) {
	if ok, _ := sandbox.Available(); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS sandbox-exec")
	}
	env := newEnv(t)
	policy := &sandbox.Policy{Writable: []string{env.Root}}
	ctx := context.Background()
	git := func(args string) string {
		out, _ := RunCommand(ctx, env.Root, "git "+args, time.Minute, nil)
		return out
	}
	git("init -q .")
	out, _ := RunCommand(ctx, env.Root, "osascript -e 'return 1'; open -a TextEdit; echo after", time.Minute, policy)
	if strings.Count(out, "Operation not permitted") < 2 {
		t.Fatalf("osascript/open were not blocked: %s", out)
	}
	out, _ = RunCommand(ctx, env.Root, "echo 'echo pwned' > .git/hooks/pre-commit; git config core.hooksPath /tmp/x; echo hi > a.txt && git add a.txt && git -c user.email=a@b -c user.name=a commit -qm one && echo committed", time.Minute, policy)
	if _, err := os.Stat(filepath.Join(env.Root, ".git/hooks/pre-commit")); err == nil {
		t.Fatalf("git hook planted from the sandbox: %s", out)
	}
	if strings.Contains(git("config --get core.hooksPath"), "/tmp/x") {
		t.Fatal(".git/config changed from the sandbox")
	}
	if !strings.Contains(out, "committed") {
		t.Fatalf("git commit should still work in the sandbox: %s", out)
	}
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	out, _ = RunCommand(ctx, env.Root, "echo sock=$SSH_AUTH_SOCK", time.Minute, policy)
	if strings.Contains(out, "agent.sock") {
		t.Fatalf("SSH agent socket passed into the sandbox: %s", out)
	}
}

func TestSandboxGitDirCannotBeSwapped(t *testing.T) {
	if ok, _ := sandbox.Available(); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS sandbox-exec")
	}
	env := newEnv(t)
	RunCommand(context.Background(), env.Root, "git init -q .", time.Minute, nil)
	policy := &sandbox.Policy{Writable: []string{env.Root}}
	out, _ := RunCommand(context.Background(), env.Root, "mv .git .git-old; mkdir -p evil/hooks; echo 'echo pwned' > .git/hooks/post-checkout; ls -d .git", time.Minute, policy)
	if _, err := os.Stat(filepath.Join(env.Root, ".git-old")); err == nil {
		t.Fatalf(".git was moved from the sandbox: %s", out)
	}
	if _, err := os.Stat(filepath.Join(env.Root, ".git", "HEAD")); err != nil {
		t.Fatalf(".git damaged: %s", out)
	}
}

func TestSandboxCannotCreateGitRepo(t *testing.T) {
	if ok, _ := sandbox.Available(); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS sandbox-exec")
	}
	env := newEnv(t)
	policy := &sandbox.Policy{Writable: []string{env.Root}}
	out, _ := RunCommand(context.Background(), env.Root, "mkdir -p .git/hooks && echo 'echo pwned' > .git/hooks/post-checkout; git init -q . ; echo done", time.Minute, policy)
	if _, err := os.Stat(filepath.Join(env.Root, ".git")); err == nil {
		t.Fatalf("a .git folder was created from the sandbox: %s", out)
	}
}

func TestSandboxGitProtectionIgnoresCase(t *testing.T) {
	if ok, _ := sandbox.Available(); !ok || runtime.GOOS != "darwin" {
		t.Skip("needs macOS sandbox-exec")
	}
	env := newEnv(t)
	RunCommand(context.Background(), env.Root, "git init -q .", time.Minute, nil)
	policy := &sandbox.Policy{Writable: []string{env.Root}}
	out, _ := RunCommand(context.Background(), env.Root, "echo pwned > .GIT/hooks/pre-commit; echo x >> .Git/config; mkdir -p .gIT/HOOKS; echo done", time.Minute, policy)
	if _, err := os.Stat(filepath.Join(env.Root, ".git", "hooks", "pre-commit")); err == nil {
		t.Fatalf("hook planted through a differently-cased path: %s", out)
	}
	if strings.Count(out, "Operation not permitted") < 2 {
		t.Fatalf("expected the writes to be refused: %s", out)
	}
	// Ordinary files with similar names are fine.
	out, _ = RunCommand(context.Background(), env.Root, "echo ok > .gitignore && echo ok > notes.git && echo fine", time.Minute, policy)
	if !strings.Contains(out, "fine") {
		t.Fatalf("ordinary files refused: %s", out)
	}
}
