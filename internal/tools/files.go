package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxReadBytes = 8 << 20 // refuse to load files larger than this

func looksBinary(data []byte) bool {
	n := min(len(data), 8000)
	return bytes.IndexByte(data[:n], 0) >= 0 || !utf8.Valid(trimPartialRune(data[:n]))
}

// trimPartialRune drops a UTF-8 sequence cut off at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// ---- read_file ----

// ReadFile reads a text file with line numbers.
type ReadFile struct{}

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Kind() Kind   { return KindRead }
func (ReadFile) Description() string {
	return "Read a text file. Returns numbered lines. Use offset and limit for large files."
}
func (ReadFile) Schema() json.RawMessage {
	return schema([]string{"path"},
		[3]string{"path", "string", "File path, relative to the workspace or absolute"},
		[3]string{"offset", "integer", "First line to read, 1-based (default 1)"},
		[3]string{"limit", "integer", "Maximum lines to read (default 2000)"},
	)
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (ReadFile) Summary(args json.RawMessage) string {
	var a readArgs
	_ = json.Unmarshal(args, &a)
	if a.Offset > 1 || a.Limit > 0 {
		return fmt.Sprintf("%s (from line %d)", a.Path, max(a.Offset, 1))
	}
	return a.Path
}

func (ReadFile) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a readArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	p, err := env.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", notFoundHint(env, p, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_directory", a.Path)
	}
	if info.Size() > maxReadBytes {
		return "", fmt.Errorf("%s is %d bytes, too large to read whole; use grep to find what you need", a.Path, info.Size())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if looksBinary(data) {
		return fmt.Sprintf("%s is a binary file (%d bytes); not shown.", a.Path, len(data)), nil
	}
	if len(data) == 0 {
		return fmt.Sprintf("%s is empty.", a.Path), nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	start := max(a.Offset, 1)
	limit := a.Limit
	if limit <= 0 {
		limit = 2000
	}
	if start > len(lines) {
		return "", fmt.Errorf("offset %d is past the end of %s (%d lines)", start, a.Path, len(lines))
	}
	end := min(start-1+limit, len(lines))
	var b strings.Builder
	width := len(fmt.Sprint(end))
	for i := start - 1; i < end; i++ {
		line := lines[i]
		if len(line) > 2000 {
			line = line[:2000] + " […line truncated]"
		}
		fmt.Fprintf(&b, "%*d\t%s\n", width, i+1, line)
	}
	if start > 1 || end < len(lines) {
		fmt.Fprintf(&b, "\n(lines %d-%d of %d; use offset to read more)\n", start, end, len(lines))
	}
	return b.String(), nil
}

// notFoundHint adds similarly named files to a not-found error.
func notFoundHint(env *Env, p string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir, base := filepath.Dir(p), strings.ToLower(filepath.Base(p))
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		return fmt.Errorf("%s does not exist", env.Rel(p))
	}
	var close []string
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if stem != "" && (strings.Contains(n, stem) || strings.Contains(stem, strings.TrimSuffix(n, filepath.Ext(n)))) {
			close = append(close, env.Rel(filepath.Join(dir, e.Name())))
		}
		if len(close) == 5 {
			break
		}
	}
	if len(close) > 0 {
		return fmt.Errorf("%s does not exist. Did you mean: %s", env.Rel(p), strings.Join(close, ", "))
	}
	return fmt.Errorf("%s does not exist", env.Rel(p))
}

// ---- list_directory ----

// ListDirectory lists a directory, optionally recursively.
type ListDirectory struct{}

func (ListDirectory) Name() string { return "list_directory" }
func (ListDirectory) Kind() Kind   { return KindRead }
func (ListDirectory) Description() string {
	return "List files and folders. Folders end with /. Skips .git, node_modules and build output."
}
func (ListDirectory) Schema() json.RawMessage {
	return schema(nil,
		[3]string{"path", "string", "Directory (default: workspace root)"},
		[3]string{"recursive", "boolean", "Include subfolders, up to 4 levels (default false)"},
	)
}

type listArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func (ListDirectory) Summary(args json.RawMessage) string {
	var a listArgs
	_ = json.Unmarshal(args, &a)
	if a.Path == "" {
		a.Path = "."
	}
	if a.Recursive {
		return a.Path + " (recursive)"
	}
	return a.Path
}

func (ListDirectory) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a listArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		a.Path = "."
	}
	root, err := env.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", notFoundHint(env, root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a directory", a.Path)
	}
	const maxEntries = 500
	maxDepth := 1
	if a.Recursive {
		maxDepth = 4
	}
	var out []string
	truncated := false
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if d.IsDir() {
			if skipDirs[d.Name()] {
				out = append(out, filepath.ToSlash(rel)+"/ (skipped)")
				return filepath.SkipDir
			}
			out = append(out, filepath.ToSlash(rel)+"/")
			if depth >= maxDepth {
				return filepath.SkipDir
			}
		} else {
			out = append(out, filepath.ToSlash(rel))
		}
		if len(out) >= maxEntries {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return a.Path + " is empty.", nil
	}
	sort.Strings(out)
	res := strings.Join(out, "\n")
	if truncated {
		res += fmt.Sprintf("\n\n(stopped at %d entries; list a subfolder for more)", maxEntries)
	}
	return res, nil
}

// ---- write_file ----

// WriteFile creates a file, or replaces one when overwrite is set.
type WriteFile struct{}

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Kind() Kind   { return KindWrite }
func (WriteFile) Description() string {
	return "Create a file with the given content. Refuses to replace an existing file unless overwrite is true; prefer edit_file for changes."
}
func (WriteFile) Schema() json.RawMessage {
	return schema([]string{"path", "content"},
		[3]string{"path", "string", "File path"},
		[3]string{"content", "string", "Full file content"},
		[3]string{"overwrite", "boolean", "Replace the file if it exists (default false)"},
	)
}

type writeArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

func (WriteFile) Summary(args json.RawMessage) string {
	var a writeArgs
	_ = json.Unmarshal(args, &a)
	return fmt.Sprintf("%s (%d lines)", a.Path, countLines(a.Content))
}

func (WriteFile) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a writeArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	p, err := env.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	perm := os.FileMode(0o644)
	existed := false
	if info, err := os.Stat(p); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", a.Path)
		}
		if !a.Overwrite {
			return "", fmt.Errorf("%s already exists; use edit_file to change it, or set overwrite to true to replace it", a.Path)
		}
		perm = info.Mode().Perm()
		existed = true
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := writeAtomic(p, []byte(a.Content), perm); err != nil {
		return "", err
	}
	verb := "Created"
	if existed {
		verb = "Replaced"
	}
	return fmt.Sprintf("%s %s (%d lines, %d bytes).", verb, env.Rel(p), countLines(a.Content), len(a.Content)), nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func writeAtomic(p string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".kalonide-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// ---- edit_file ----

// EditFile replaces an exact string in a file.
type EditFile struct{}

func (EditFile) Name() string { return "edit_file" }
func (EditFile) Kind() Kind   { return KindWrite }
func (EditFile) Description() string {
	return "Replace old_string with new_string in a file. old_string must match exactly, including whitespace, and be unique unless replace_all is true. Read the file first."
}
func (EditFile) Schema() json.RawMessage {
	return schema([]string{"path", "old_string", "new_string"},
		[3]string{"path", "string", "File path"},
		[3]string{"old_string", "string", "Exact text to replace"},
		[3]string{"new_string", "string", "Replacement text"},
		[3]string{"replace_all", "boolean", "Replace every occurrence (default false)"},
	)
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (EditFile) Summary(args json.RawMessage) string {
	var a editArgs
	_ = json.Unmarshal(args, &a)
	return fmt.Sprintf("%s (-%d +%d lines)", a.Path, countLines(a.OldString), countLines(a.NewString))
}

func (EditFile) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a editArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.OldString == "" {
		return "", errors.New("old_string is empty; use write_file to create a file")
	}
	if a.OldString == a.NewString {
		return "", errors.New("old_string and new_string are identical; nothing to change")
	}
	p, err := env.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", notFoundHint(env, p, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	text := string(data)
	oldS, newS := a.OldString, a.NewString
	// Match files with Windows line endings when the model sent \n.
	if strings.Contains(text, "\r\n") && !strings.Contains(oldS, "\r\n") {
		oldS = strings.ReplaceAll(oldS, "\n", "\r\n")
		newS = strings.ReplaceAll(newS, "\n", "\r\n")
	}
	count := strings.Count(text, oldS)
	switch {
	case count == 0:
		return "", errors.New("old_string was not found in " + a.Path + closestHint(text, a.OldString))
	case count > 1 && !a.ReplaceAll:
		return "", fmt.Errorf("old_string appears %d times in %s (lines %s); add surrounding lines to make it unique, or set replace_all", count, a.Path, occurrenceLines(text, oldS))
	}
	firstLine := strings.Count(text[:strings.Index(text, oldS)], "\n") + 1
	if a.ReplaceAll {
		text = strings.ReplaceAll(text, oldS, newS)
	} else {
		text = strings.Replace(text, oldS, newS, 1)
	}
	if err := writeAtomic(p, []byte(text), info.Mode().Perm()); err != nil {
		return "", err
	}
	if count > 1 {
		return fmt.Sprintf("Replaced %d occurrences in %s.", count, env.Rel(p)), nil
	}
	return fmt.Sprintf("Edited %s at line %d (-%d +%d lines).", env.Rel(p), firstLine, countLines(a.OldString), countLines(a.NewString)), nil
}

func occurrenceLines(text, s string) string {
	var lines []string
	off := 0
	for len(lines) < 10 {
		i := strings.Index(text[off:], s)
		if i < 0 {
			break
		}
		lines = append(lines, fmt.Sprint(strings.Count(text[:off+i], "\n")+1))
		off += i + len(s)
	}
	return strings.Join(lines, ", ")
}

// closestHint finds the file line most similar to the first non-blank line
// of old, so the model can see what it got wrong (usually whitespace).
func closestHint(text, old string) string {
	var first string
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	if first == "" {
		return ""
	}
	best, bestLine, bestScore := "", 0, 0.0
	for i, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if t == first {
			return fmt.Sprintf(". Line %d matches the first line once whitespace is ignored; check indentation and the following lines, and re-read the file", i+1)
		}
		if s := similarity(first, t); s > bestScore {
			best, bestLine, bestScore = l, i+1, s
		}
	}
	if bestScore >= 0.6 {
		if len(best) > 200 {
			best = best[:200]
		}
		return fmt.Sprintf(". Closest line is %d: %q. Re-read the file and copy the text exactly", bestLine, best)
	}
	return ". Re-read the file and copy the text exactly"
}

// similarity is a cheap bigram overlap score in [0,1].
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	grams := map[string]int{}
	for i := 0; i+1 < len(a); i++ {
		grams[a[i:i+2]]++
	}
	hits := 0
	for i := 0; i+1 < len(b); i++ {
		if grams[b[i:i+2]] > 0 {
			grams[b[i:i+2]]--
			hits++
		}
	}
	return 2 * float64(hits) / float64(len(a)+len(b)-2)
}
