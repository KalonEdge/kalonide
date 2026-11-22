// Package sandbox confines shell commands the agent runs: writes only to
// the workspace, temp folders and build caches, and no network beyond
// this machine unless allowed. It uses macOS's sandbox-exec; other systems
// have no sandbox yet, and say so.
package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// Only macOS is supported: a Linux sandbox (bubblewrap) is planned but
// will ship only once it is verified on Linux.
func Available() (bool, string) {
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(macTool); err == nil {
			return true, "sandbox-exec"
		}
	}
	return false, ""
}

// Wrap returns argv that runs argv inside the sandbox, or argv unchanged
// when no sandbox is available.
func Wrap(argv []string, p Policy) []string {
	if ok, _ := Available(); !ok {
		return argv
	}
	return append([]string{macTool, "-p", macProfile(p)}, argv...)
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
	// macOS disks usually ignore case (and fold two Unicode letters), so
	// .GIT/hooks is .git/hooks: lock all of .git in any spelling, then
	// allow back exactly what git needs to commit.
	b.WriteString(gitRules(p.Writable))
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

// gitProtected returns, for each root, the paths a sandboxed command must
// not write, whether or not they exist yet: the .git entry itself (so it
// cannot be created, removed or swapped) and the parts that run or
// configure code (hooks, config, info, modules, worktrees, commondir). It
// follows a .git file (worktrees, submodules) to the real and the shared
// git folder. All paths are real paths, which is what macOS matches.
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
		literals = append(literals, dotGit)
		protect(dotGit)
		info, err := os.Lstat(dotGit)
		if err != nil || info.IsDir() {
			continue
		}
		// A .git file points at the real git folder.
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
			if real, err := filepath.EvalSymlinks(common); err == nil {
				common = real
			}
			protect(common)
		}
	}
	return literals, subpaths
}

// anyCase matches s in any letter case, including the two Unicode letters
// that macOS disks fold to ASCII: the Kelvin sign (k) and the long s (s).
// It uses alternation, since bracket expressions in sandbox regexes work
// on bytes, not characters.
func anyCase(s string) string {
	var b strings.Builder
	for _, r := range s {
		lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r))
		if lo == up {
			b.WriteString(regexp.QuoteMeta(string(r)))
			continue
		}
		alts := []string{up, lo}
		switch lo {
		case "k":
			alts = append(alts, "\u212a")
		case "s":
			alts = append(alts, "\u017f")
		}
		b.WriteString("(" + strings.Join(alts, "|") + ")")
	}
	return b.String()
}

// gitWritable are the parts of a .git folder git needs to commit, fetch
// and keep history: everything else (hooks, config, info, rebase todo
// lists that can hold commands, …) stays read-only.
const gitWritable = `(objects|refs|logs|lfs)(/.*)?|(index|HEAD|ORIG_HEAD|FETCH_HEAD|MERGE_HEAD|MERGE_MSG|MERGE_MODE|CHERRY_PICK_HEAD|REVERT_HEAD|COMMIT_EDITMSG|packed-refs|description|gc\.log|shallow)(\.lock)?`

// gitRules returns sandbox rules that make every .git folder in roots
// read-only in any spelling (letter case, Unicode folding), then allow the
// exact parts git needs back.
func gitRules(roots []string) string {
	var deny, allow []string
	for _, root := range roots {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
		if strings.ContainsAny(root, "\"\\") {
			continue // not expressible safely; the exact-path rules still apply
		}
		deny = append(deny, `(regex #"^`+anyCase(root)+"/"+anyCase(".git")+`(/.*)?$")`)
		if !isASCII(root) {
			// The same accented letter can be stored in two Unicode forms,
			// so a pattern may miss another spelling of this folder: lock
			// all of .git here (by its real path too) and allow nothing
			// back. Committing from inside the sandbox fails in such
			// folders; the user can still commit.
			deny = append(deny, "(subpath "+sbString(filepath.Join(root, ".git"))+")")
			continue
		}
		allow = append(allow, `(regex #"^`+regexp.QuoteMeta(root)+`/\.git/(`+gitWritable+`)$")`)
	}
	if len(deny) == 0 {
		return ""
	}
	out := "(deny file-write* " + strings.Join(deny, " ") + ")\n"
	if len(allow) > 0 {
		out += "(allow file-write* " + strings.Join(allow, " ") + ")\n"
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
