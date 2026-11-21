package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ApplyPatch applies a unified diff (the format of `git diff`) across one
// or more files. Every hunk must fit before anything is written: either
// the whole patch applies or nothing changes.
type ApplyPatch struct{}

func (ApplyPatch) Name() string { return "apply_patch" }
func (ApplyPatch) Kind() Kind   { return KindWrite }
func (ApplyPatch) Description() string {
	return "Apply a unified diff (like `git diff` output) to one or more files: `--- a/path` and `+++ b/path` headers, then `@@` hunks with context lines (space), removed lines (-) and added lines (+). Use /dev/null for new or deleted files. All hunks must apply or nothing changes. For a single small change, edit_file is simpler."
}
func (ApplyPatch) Schema() json.RawMessage {
	return schema([]string{"patch"}, [3]string{"patch", "string", "The unified diff"})
}

type patchArgs struct {
	Patch string `json:"patch"`
}

func (ApplyPatch) Summary(args json.RawMessage) string {
	var a patchArgs
	_ = json.Unmarshal(args, &a)
	files, err := parsePatch(a.Patch)
	if err != nil {
		return "patch"
	}
	var names []string
	for _, f := range files {
		names = append(names, f.target())
	}
	return strings.Join(names, ", ")
}

// Preview shows the patch itself, coloured by the renderer.
func (ApplyPatch) Preview(_ *Env, args json.RawMessage) string {
	var a patchArgs
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	files, err := parsePatch(a.Patch)
	if err != nil {
		return ""
	}
	var b strings.Builder
	shown := 0
	for _, f := range files {
		fmt.Fprintf(&b, "     %s\n", f.target())
		for _, h := range f.hunks {
			for _, l := range h.lines {
				if shown == 60 {
					b.WriteString("     … more\n")
					return strings.TrimRight(b.String(), "\n")
				}
				fmt.Fprintf(&b, "%c      │ %s\n", l.op, l.text)
				shown++
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

type patchLine struct {
	op   byte // ' ', '-', '+'
	text string
}

type hunk struct {
	oldStart int
	lines    []patchLine
}

func (h hunk) old() []string {
	var out []string
	for _, l := range h.lines {
		if l.op != '+' {
			out = append(out, l.text)
		}
	}
	return out
}

func (h hunk) new() []string {
	var out []string
	for _, l := range h.lines {
		if l.op != '-' {
			out = append(out, l.text)
		}
	}
	return out
}

type filePatch struct {
	oldPath, newPath string // "" for /dev/null
	hunks            []hunk
}

func (f filePatch) target() string {
	if f.newPath != "" {
		return f.newPath
	}
	return f.oldPath
}

func stripPrefix(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i] // drop timestamps
	}
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// parsePatch reads a unified diff. It ignores git's extra header lines.
func parsePatch(text string) ([]filePatch, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var files []filePatch
	cur := -1 // index into files
	var h *hunk
	flushHunk := func() {
		if h != nil && cur >= 0 {
			files[cur].hunks = append(files[cur].hunks, *h)
		}
		h = nil
	}
	sectionStart := func(l string) bool {
		return strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "@@") || strings.HasPrefix(l, "index ")
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			flushHunk()
			files = append(files, filePatch{oldPath: stripPrefix(l[4:]), newPath: stripPrefix(lines[i+1][4:])})
			cur = len(files) - 1
			i++
		case strings.HasPrefix(l, "@@"):
			if cur < 0 {
				return nil, errors.New("hunk before any ---/+++ file header")
			}
			flushHunk()
			h = &hunk{oldStart: hunkStart(l)}
		case h != nil && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+")):
			h.lines = append(h.lines, patchLine{op: l[0], text: l[1:]})
		case h != nil && l == "":
			// Some tools drop the space on empty context lines; a blank
			// line before the next section (or at the end) is a separator.
			if i < len(lines)-1 && !sectionStart(lines[i+1]) {
				h.lines = append(h.lines, patchLine{op: ' ', text: ""})
			}
		case strings.HasPrefix(l, `\ No newline`):
			// ignored
		}
	}
	flushHunk()
	if len(files) == 0 {
		return nil, errors.New("no file headers (--- a/path, +++ b/path) found in the patch")
	}
	for _, f := range files {
		if f.target() == "" {
			return nil, errors.New("a file header has /dev/null on both sides")
		}
		if len(f.hunks) == 0 && f.newPath != "" && f.oldPath != "" {
			return nil, fmt.Errorf("%s: no hunks", f.target())
		}
	}
	return files, nil
}

func hunkStart(header string) int {
	// @@ -12,7 +12,8 @@
	f := strings.Fields(header)
	if len(f) < 2 || !strings.HasPrefix(f[1], "-") {
		return 0
	}
	n, _ := strconv.Atoi(strings.SplitN(f[1][1:], ",", 2)[0])
	return n
}

// applyHunks applies hunks to lines. Each hunk is found near its stated
// line first, then anywhere after the previous hunk, then ignoring
// differences in surrounding whitespace.
func applyHunks(lines []string, hunks []hunk) ([]string, error) {
	out := append([]string(nil), lines...)
	offset := 0 // how far earlier hunks shifted line numbers
	from := 0   // hunks apply in order, after the previous one
	for n, h := range hunks {
		old := h.old()
		at := -1
		for _, eq := range []func(a, b string) bool{
			func(a, b string) bool { return a == b },
			func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
		} {
			at = findBlock(out, old, from, h.oldStart-1+offset, eq)
			if at >= 0 {
				break
			}
		}
		if at < 0 {
			first := "(empty)"
			if len(old) > 0 {
				first = strings.TrimSpace(old[0])
			}
			return nil, fmt.Errorf("hunk %d (around line %d) does not match the file; its first line is %q. Re-read the file and send a patch against its current content", n+1, h.oldStart, first)
		}
		repl := h.new()
		out = append(out[:at], append(append([]string(nil), repl...), out[at+len(old):]...)...)
		offset += len(repl) - len(old)
		from = at + len(repl)
	}
	return out, nil
}

// findBlock returns where block occurs in lines at or after from, choosing
// the occurrence closest to hint.
func findBlock(lines, block []string, from, hint int, eq func(a, b string) bool) int {
	if len(block) == 0 {
		return min(max(hint, from), len(lines))
	}
	best, bestDist := -1, 1<<30
	for i := from; i+len(block) <= len(lines); i++ {
		ok := true
		for j := range block {
			if !eq(lines[i+j], block[j]) {
				ok = false
				break
			}
		}
		if ok {
			d := i - hint
			if d < 0 {
				d = -d
			}
			if d < bestDist {
				best, bestDist = i, d
			}
		}
	}
	return best
}

func (ApplyPatch) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a patchArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	files, err := parsePatch(a.Patch)
	if err != nil {
		return "", err
	}
	type change struct {
		path    string
		content []byte // nil means delete
		mode    os.FileMode
		verb    string
	}
	var changes []change
	// Work everything out first; write nothing until every file fits.
	for _, f := range files {
		p, err := env.Resolve(f.target())
		if err != nil {
			return "", err
		}
		switch {
		case f.oldPath == "": // new file
			if _, err := os.Stat(p); err == nil {
				return "", fmt.Errorf("%s already exists; the patch creates it from /dev/null", f.target())
			}
			var lines []string
			for _, h := range f.hunks {
				lines = append(lines, h.new()...)
			}
			changes = append(changes, change{p, []byte(strings.Join(lines, "\n") + "\n"), 0o644, "created"})
		case f.newPath == "": // deleted file
			if _, err := os.Stat(p); err != nil {
				return "", fmt.Errorf("%s does not exist, so it cannot be deleted", f.target())
			}
			changes = append(changes, change{p, nil, 0, "deleted"})
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return "", notFoundHint(env, p, err)
			}
			info, _ := os.Stat(p)
			text := string(data)
			crlf := strings.Contains(text, "\r\n")
			text = strings.ReplaceAll(text, "\r\n", "\n")
			trailing := strings.HasSuffix(text, "\n")
			lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
			patched, err := applyHunks(lines, f.hunks)
			if err != nil {
				return "", fmt.Errorf("%s: %w", f.target(), err)
			}
			out := strings.Join(patched, "\n")
			if trailing {
				out += "\n"
			}
			if crlf {
				out = strings.ReplaceAll(out, "\n", "\r\n")
			}
			changes = append(changes, change{p, []byte(out), info.Mode().Perm(), "patched"})
		}
	}
	var report []string
	for _, c := range changes {
		if err := env.beforeWrite(c.path); err != nil {
			return "", err
		}
		if c.content == nil {
			if err := os.Remove(c.path); err != nil {
				return "", err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
				return "", err
			}
			if err := writeAtomic(c.path, c.content, c.mode); err != nil {
				return "", err
			}
		}
		report = append(report, c.verb+" "+env.Rel(c.path))
	}
	return "Applied: " + strings.Join(report, ", ") + ".", nil
}
