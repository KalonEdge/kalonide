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
	var b strings.Builder
	b.WriteString("(version 1)(allow default)(deny file-write*)\n(allow file-write*")
	for _, d := range writableDirs(p) {
		b.WriteString(" (subpath " + sbString(d) + ")")
	}
	b.WriteString(")\n")
	if !p.Network {
		b.WriteString(`(deny network-outbound)(allow network-outbound (remote ip "localhost:*") (remote unix-socket))` + "\n")
	}
	return b.String()
}

func bwrapArgs(tool string, p Policy) []string {
	args := []string{tool, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--die-with-parent"}
	for _, d := range writableDirs(p) {
		if d == "/dev" {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--bind", d, d)
		}
	}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	return args
}
