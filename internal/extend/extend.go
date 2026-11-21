// Package extend loads the user's own extensions from markdown files:
// slash commands (a prompt template per file) and skills (instruction
// packs the model loads when it needs them). Project files live in
// .kalonide/, personal ones in the Kalonide home folder; a project file
// wins over a personal one with the same name.
package extend

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Item is one command or skill.
type Item struct {
	Name        string
	Description string
	Body        string
	Scope       string // "project" or "user"
	Path        string
}

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,48}$`)

// ValidName reports whether s can name a command or skill.
func ValidName(s string) bool { return reName.MatchString(s) }

// Commands loads slash commands from <dir>/commands/*.md.
func Commands(projectDir, userDir string) []Item {
	return load(projectDir, userDir, "commands", false)
}

// Skills loads skills from <dir>/skills/<name>.md or <name>/SKILL.md.
func Skills(projectDir, userDir string) []Item {
	return load(projectDir, userDir, "skills", true)
}

func load(projectDir, userDir, kind string, allowFolders bool) []Item {
	byName := map[string]Item{}
	// User first, so project items replace them.
	for _, src := range []struct{ scope, dir string }{{"user", userDir}, {"project", projectDir}} {
		if src.dir == "" {
			continue
		}
		dir := filepath.Join(src.dir, kind)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			var name, path string
			switch {
			case !e.IsDir() && strings.HasSuffix(e.Name(), ".md"):
				name, path = strings.TrimSuffix(e.Name(), ".md"), filepath.Join(dir, e.Name())
			case e.IsDir() && allowFolders:
				name, path = e.Name(), filepath.Join(dir, e.Name(), "SKILL.md")
			default:
				continue
			}
			name = strings.ToLower(name)
			if !ValidName(name) {
				continue
			}
			// A symlink must not pull in a file from outside this folder.
			real, err := filepath.EvalSymlinks(path)
			if err != nil || !inside(realDir, real) {
				continue
			}
			data, err := os.ReadFile(real)
			if err != nil || len(data) > 64<<10 {
				continue
			}
			meta, body := frontmatter(clean(string(data), true))
			if meta["name"] != "" && ValidName(strings.ToLower(meta["name"])) {
				name = strings.ToLower(meta["name"])
			}
			desc := clean(meta["description"], false)
			if desc == "" {
				desc = firstLine(body)
			}
			byName[name] = Item{Name: name, Description: desc, Body: strings.TrimSpace(body), Scope: src.scope, Path: path}
		}
	}
	out := make([]Item, 0, len(byName))
	for _, it := range byName {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// frontmatter splits "---\nkey: value\n---\nbody" into simple key/value
// pairs and the body.
func frontmatter(s string) (map[string]string, string) {
	meta := map[string]string{}
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---\n") {
		return meta, s
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return meta, s
	}
	for _, line := range strings.Split(s[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			meta[strings.TrimSpace(strings.ToLower(k))] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return meta, strings.TrimLeft(s[4+end+4:], "\n")
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "# "))
		if l != "" {
			if len(l) > 100 {
				l = l[:100] + "…"
			}
			return l
		}
	}
	return ""
}

// Expand fills a command's template: $ARGUMENTS becomes args; without the
// placeholder, args are appended on a new line.
func Expand(cmd Item, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(cmd.Body, "$ARGUMENTS") {
		return strings.ReplaceAll(cmd.Body, "$ARGUMENTS", args)
	}
	if args == "" {
		return cmd.Body
	}
	return cmd.Body + "\n\n" + args
}

// Find returns the item with the given name.
func Find(items []Item, name string) (Item, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

func inside(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// clean removes terminal control characters (escape sequences, C1 codes,
// bidirectional overrides) from text read from these files, keeping
// newlines and tabs when multiline is set.
func clean(s string, multiline bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case (r == '\n' || r == '\t') && multiline:
			return r
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f,
			r == 0x2028, r == 0x2029, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}
