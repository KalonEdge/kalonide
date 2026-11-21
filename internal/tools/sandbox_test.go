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
