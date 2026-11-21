// Package sandbox confines shell commands the agent runs: writes only to
// the workspace, temp folders and build caches, and no network beyond
// this machine unless allowed. It uses macOS's sandbox-exec, or bwrap
// (bubblewrap) on Linux when installed.
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Policy says what a sandboxed command may do.
type Policy struct {
	Writable []string // folders it may write (besides temp folders and caches)
	Network  bool     // reach other machines; localhost is always allowed on macOS
}

const macTool = "/usr/bin/sandbox-exec"

// Available reports whether this machine can sandbox commands, and how.
func Available() (bool, string) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(macTool); err == nil {
			return true, "sandbox-exec"
		}
	case "linux":
		if p, err := exec.LookPath("bwrap"); err == nil {
			return true, p
		}
	}
	return false, ""
}

// Wrap returns argv that runs argv inside the sandbox, or argv unchanged
// when no sandbox is available.
func Wrap(argv []string, p Policy) []string {
	ok, tool := Available()
	if !ok {
		return argv
	}
	if runtime.GOOS == "darwin" {
		return append([]string{macTool, "-p", macProfile(p)}, argv...)
	}
	return append(bwrapArgs(tool, p), argv...)
}

// writableDirs is the policy's folders plus the places builds and package
// managers need: temp folders and the usual caches.
func writableDirs(p Policy) []string {
	dirs := append([]string{}, p.Writable...)
	dirs = append(dirs, os.TempDir(), "/tmp", "/private/tmp", "/private/var/folders", "/dev")
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{
			"Library/Caches", ".cache", ".npm", ".pnpm-store", ".yarn", ".bun",
			"go/pkg/mod", ".cargo/registry", ".cargo/git", ".gradle/caches", ".m2/repository",
		} {
			dirs = append(dirs, filepath.Join(home, rel))
		}
	}
	for _, v := range []string{"GOCACHE", "GOMODCACHE"} {
		if d := goEnv(v); d != "" {
			dirs = append(dirs, d)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func goEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sbString quotes a path for a sandbox profile.
func sbString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func macProfile(p Policy) string {
	dirs := writableDirs(p)
	var b strings.Builder
	b.WriteString("(version 1)(allow default)\n")
	// Writes: only the allowed folders.
	b.WriteString("(deny file-write*)\n(allow file-write*")
	for _, d := range dirs {
		b.WriteString(" (subpath " + sbString(d) + ")")
	}
	b.WriteString(")\n")
	// Files that run code later, outside the sandbox, when the user uses
	// git: keep them read-only (commits still work), and keep .git itself
	// from being replaced. gitProtected returns real paths, which is what
	// the sandbox matches against.
	if lit, subs := gitProtected(p.Writable); len(lit)+len(subs) > 0 {
		b.WriteString("(deny file-write*")
		for _, l := range lit {
			b.WriteString(" (literal " + sbString(l) + ")")
		}
		for _, d := range subs {
			b.WriteString(" (subpath " + sbString(d) + ")")
		}
		b.WriteString(")\n")
	}
	// No escaping through other apps: Apple Events (Terminal, System
	// Events), opening apps, or the tools that do those things.
	b.WriteString("(deny appleevent-send)(deny lsopen)\n")
	b.WriteString(`(deny process-exec (literal "/usr/bin/osascript") (literal "/usr/bin/open") (literal "/usr/bin/automator"))` + "\n")
	// Network: this machine only (unless allowed), and Unix sockets only
	// inside the allowed folders (not the Docker socket or the SSH agent).
	b.WriteString("(deny network-outbound)\n(allow network-outbound")
	if p.Network {
		b.WriteString(` (remote ip "*:*")`)
	} else {
		b.WriteString(` (remote ip "localhost:*")`)
	}
	// Unix sockets only inside the workspace: shared temp folders hold
	// other programs' sockets.
	for _, d := range p.Writable {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		b.WriteString(" (remote unix-socket (subpath " + sbString(d) + "))")
	}
	b.WriteString(")\n")
	return b.String()
}

func bwrapArgs(tool string, p Policy) []string {
	args := []string{tool, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
		"--die-with-parent", "--new-session", "--unshare-ipc",
		// Hide service sockets (Docker, D-Bus) that would hand out more power.
		"--tmpfs", "/run", "--tmpfs", "/var/run"}
	for _, d := range writableDirs(p) {
		if d == "/dev" {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--bind", d, d)
		}
	}
	_, subs := gitProtected(p.Writable)
	for _, d := range subs {
		if strings.HasSuffix(d, "/hooks") {
			_ = os.MkdirAll(d, 0o755) // so it can be bound read-only
		}
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--ro-bind", d, d)
		}
	}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	return args
}

// Env removes variables that point sandboxed commands at more powerful
// services: the SSH and GPG agents, D-Bus, and a Docker daemon.
func Env(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "SSH_AUTH_SOCK", "GPG_AGENT_INFO", "DBUS_SESSION_BUS_ADDRESS", "DOCKER_HOST", "DOCKER_CONTEXT":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// gitProtected returns, for each root that is a git checkout, the paths a
// sandboxed command must not write: the .git entry itself (so it cannot be
// swapped for another), and the parts of the real git folder that run or
// configure code (hooks, config, info, modules, worktrees). It follows a
// .git file (worktrees, submodules) to the real and the shared git folder.
func gitProtected(roots []string) (literals, subpaths []string) {
	protect := func(gitDir string) {
		for _, rel := range []string{"hooks", "config", "info", "modules", "worktrees", "commondir"} {
			subpaths = append(subpaths, filepath.Join(gitDir, rel))
		}
	}
	for _, root := range roots {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
		dotGit := filepath.Join(root, ".git")
		info, err := os.Lstat(dotGit)
		if err != nil {
			continue
		}
		literals = append(literals, dotGit)
		if info.IsDir() {
			protect(dotGit)
			continue
		}
		data, err := os.ReadFile(dotGit)
		if err != nil {
			continue
		}
		gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
		if gitDir == "" {
			continue
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
		if real, err := filepath.EvalSymlinks(gitDir); err == nil {
			gitDir = real
		}
		protect(gitDir)
		if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
			common := strings.TrimSpace(string(c))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			protect(filepath.Clean(common))
		}
	}
	return literals, subpaths
}
