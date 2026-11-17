package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---- glob ----

// Glob finds files by pattern. ** matches any number of folders.
type Glob struct{}

func (Glob) Name() string { return "glob" }
func (Glob) Kind() Kind   { return KindRead }
func (Glob) Description() string {
	return "Find files by glob pattern, e.g. **/*.go or src/**/test_*.py. ** matches any number of folders."
}
func (Glob) Schema() json.RawMessage {
	return schema([]string{"pattern"},
		[3]string{"pattern", "string", "Glob pattern relative to path"},
		[3]string{"path", "string", "Folder to search (default: workspace root)"},
	)
}

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (Glob) Summary(args json.RawMessage) string {
	var a globArgs
	_ = json.Unmarshal(args, &a)
	if a.Path != "" && a.Path != "." {
		return a.Pattern + " in " + a.Path
	}
	return a.Pattern
}

func (Glob) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a globArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	pat := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(a.Pattern)), "./")
	if pat == "" {
		return "", errors.New("pattern is required")
	}
	if _, err := path.Match(strings.ReplaceAll(pat, "**", "x"), ""); err != nil {
		return "", fmt.Errorf("bad pattern %q: %v", a.Pattern, err)
	}
	if a.Path == "" {
		a.Path = "."
	}
	root, err := env.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	const maxResults = 300
	var matches []string
	truncated := false
	err = walkFiles(ctx, root, func(rel string, _ fs.DirEntry) bool {
		if MatchGlob(pat, rel) {
			matches = append(matches, rel)
			if len(matches) >= maxResults {
				truncated = true
				return false
			}
		}
		return true
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "No files match " + a.Pattern, nil
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n(stopped at %d matches; narrow the pattern)", maxResults)
	}
	return out, nil
}

// MatchGlob matches a slash-separated path against a pattern in which **
// matches zero or more whole path segments. A pattern without a slash also
// matches the file's base name anywhere (so "*.go" finds nested files).
func MatchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, path.Base(name))
		return ok
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// walkFiles visits regular files under root, skipping noise directories.
// visit returns false to stop.
func walkFiles(ctx context.Context, root string, visit func(rel string, d fs.DirEntry) bool) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if !visit(filepath.ToSlash(rel), d) {
			return filepath.SkipAll
		}
		return nil
	})
	return err
}

// ---- grep ----

// Grep searches file contents with a regular expression.
type Grep struct{}

func (Grep) Name() string { return "grep" }
func (Grep) Kind() Kind   { return KindRead }
func (Grep) Description() string {
	return "Search file contents with a regular expression (Go RE2 syntax). Returns path:line: text. Skips binary files and build folders."
}
func (Grep) Schema() json.RawMessage {
	return schema([]string{"pattern"},
		[3]string{"pattern", "string", "Regular expression"},
		[3]string{"path", "string", "File or folder to search (default: workspace root)"},
		[3]string{"include", "string", "Only search files matching this glob, e.g. *.ts"},
		[3]string{"ignore_case", "boolean", "Case-insensitive match (default false)"},
	)
}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Include    string `json:"include"`
	IgnoreCase bool   `json:"ignore_case"`
}

func (Grep) Summary(args json.RawMessage) string {
	var a grepArgs
	_ = json.Unmarshal(args, &a)
	s := fmt.Sprintf("%q", a.Pattern)
	if a.Path != "" && a.Path != "." {
		s += " in " + a.Path
	}
	if a.Include != "" {
		s += " (" + a.Include + ")"
	}
	return s
}

func (Grep) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a grepArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	expr := a.Pattern
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		// Models often send literal text with regex metacharacters.
		re, err = regexp.Compile(regexp.QuoteMeta(a.Pattern))
		if err != nil {
			return "", fmt.Errorf("bad pattern: %v", err)
		}
		if a.IgnoreCase {
			re = regexp.MustCompile("(?i)" + re.String())
		}
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
	const maxHits = 200
	var hits []string
	files := 0
	truncated := false
	search := func(abs, display string) bool {
		if st, err := os.Stat(abs); err != nil || st.Size() > 4<<20 {
			return true
		}
		f, err := os.Open(abs)
		if err != nil {
			return true
		}
		defer f.Close()
		head := make([]byte, 8000)
		n, _ := f.Read(head)
		if looksBinary(head[:n]) {
			return true
		}
		if _, err := f.Seek(0, 0); err != nil {
			return true
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		matched := false
		for ln := 1; sc.Scan(); ln++ {
			line := sc.Text()
			if re.MatchString(line) {
				matched = true
				if len(line) > 300 {
					line = line[:300] + "…"
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", display, ln, strings.TrimRight(line, "\r")))
				if len(hits) >= maxHits {
					truncated = true
					return false
				}
			}
		}
		if matched {
			files++
		}
		return true
	}
	if !info.IsDir() {
		search(root, env.Rel(root))
	} else {
		err = walkFiles(ctx, root, func(rel string, d fs.DirEntry) bool {
			if a.Include != "" && !MatchGlob(a.Include, rel) {
				return true
			}
			display := rel
			if a.Path != "." {
				display = env.Rel(filepath.Join(root, rel))
			}
			return search(filepath.Join(root, rel), display)
		})
		if err != nil {
			return "", err
		}
	}
	if len(hits) == 0 {
		return "No matches for " + a.Pattern, nil
	}
	out := strings.Join(hits, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n(stopped at %d matches; narrow the pattern or path)", maxHits)
	} else {
		out += fmt.Sprintf("\n\n(%d matches in %d files)", len(hits), files)
	}
	return out, nil
}
