package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RepoMap outlines a codebase: one line per source file with its
// top-level declarations, so a model can find its way without reading
// file after file.
type RepoMap struct{}

func (RepoMap) Name() string { return "repo_map" }
func (RepoMap) Kind() Kind   { return KindRead }
func (RepoMap) Description() string {
	return "Outline the codebase: each source file with its top-level functions, types and classes. Use it first in an unfamiliar project. Optional path limits it to a folder; query keeps only files or names containing that text."
}
func (RepoMap) Schema() json.RawMessage {
	return schema(nil,
		[3]string{"path", "string", "Folder to map (default: workspace root)"},
		[3]string{"query", "string", "Only files or declarations containing this text (case-insensitive)"},
	)
}

type repoMapArgs struct {
	Path  string `json:"path"`
	Query string `json:"query"`
}

func (RepoMap) Summary(args json.RawMessage) string {
	var a repoMapArgs
	_ = json.Unmarshal(args, &a)
	s := a.Path
	if s == "" {
		s = "."
	}
	if a.Query != "" {
		s += " matching " + a.Query
	}
	return s
}

func (RepoMap) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a repoMapArgs
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
	return BuildRepoMap(ctx, root, a.Query, 14000)
}

// declaration patterns per language; the last capture group is the name.
var declPatterns = map[string][]*regexp.Regexp{
	".go": {
		regexp.MustCompile(`^func (?:\([^)]*\) )?([A-Za-z_]\w*)`),
		regexp.MustCompile(`^type ([A-Za-z_]\w*)`),
	},
	".py": {regexp.MustCompile(`^(?:async )?(?:def|class) ([A-Za-z_]\w*)`)},
	".js": jsDecls, ".jsx": jsDecls, ".ts": jsDecls, ".tsx": jsDecls, ".mjs": jsDecls,
	".rs": {regexp.MustCompile(`^(?:pub(?:\([^)]*\))? )?(?:async )?(?:fn|struct|enum|trait|type|mod) ([A-Za-z_]\w*)`),
		regexp.MustCompile(`^impl(?:<[^>]*>)? (?:[\w:<>]+ for )?([A-Za-z_]\w*)`)},
	".swift": {regexp.MustCompile(`^(?:(?:public|private|internal|open|fileprivate|final|@\w+) )*(?:func|class|struct|enum|protocol|extension|actor) ([A-Za-z_]\w*)`)},
	".java":  classDecls, ".kt": classDecls, ".cs": classDecls,
	".rb": {regexp.MustCompile(`^(?:class|module|def) ([A-Za-z_][\w.]*)`)},
}

var jsDecls = []*regexp.Regexp{
	regexp.MustCompile(`^(?:export )?(?:default )?(?:async )?(?:function\*?|class|interface|type|enum) ([A-Za-z_$][\w$]*)`),
	regexp.MustCompile(`^export (?:const|let|var) ([A-Za-z_$][\w$]*)`),
}

var classDecls = []*regexp.Regexp{
	regexp.MustCompile(`^(?:(?:public|private|protected|internal|abstract|final|static|sealed|data|open) )*(?:class|interface|enum|record|object|struct) ([A-Za-z_]\w*)`),
	regexp.MustCompile(`^(?:(?:public|private|protected|internal|override|suspend) )*fun ([A-Za-z_]\w*)`),
}

// BuildRepoMap outlines the source files under root within budget bytes.
func BuildRepoMap(ctx context.Context, root, query string, budget int) (string, error) {
	query = strings.ToLower(query)
	type entry struct {
		path  string
		decls []string
	}
	var entries []entry
	others := 0
	err := walkFiles(ctx, root, func(rel string, d fs.DirEntry) bool {
		if slash := filepath.ToSlash(rel); strings.HasPrefix(slash, "testdata/") || strings.Contains(slash, "/testdata/") {
			return true // fixtures, not the project's code
		}
		pats, ok := declPatterns[strings.ToLower(filepath.Ext(rel))]
		if !ok {
			others++
			return true
		}
		decls := fileDecls(filepath.Join(root, rel), pats)
		if query != "" {
			if !strings.Contains(strings.ToLower(rel), query) {
				var keep []string
				for _, dcl := range decls {
					if strings.Contains(strings.ToLower(dcl), query) {
						keep = append(keep, dcl)
					}
				}
				if len(keep) == 0 {
					return true
				}
				decls = keep
			}
		}
		entries = append(entries, entry{filepath.ToSlash(rel), decls})
		return len(entries) < 3000
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		if query != "" {
			return "No source files or declarations match " + query + ".", nil
		}
		return "No source files found here.", nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var b strings.Builder
	shown := 0
	for _, e := range entries {
		line := e.path
		if len(e.decls) > 0 {
			ds := e.decls
			if len(ds) > 25 {
				ds = append(ds[:25:25], fmt.Sprintf("… %d more", len(e.decls)-25))
			}
			line += ": " + strings.Join(ds, ", ")
		}
		if b.Len()+len(line) > budget {
			fmt.Fprintf(&b, "… %d more files (map a subfolder with path, or narrow with query)\n", len(entries)-shown)
			break
		}
		b.WriteString(line + "\n")
		shown++
	}
	fmt.Fprintf(&b, "(%d source files", len(entries))
	if others > 0 {
		fmt.Fprintf(&b, "; %d other files not outlined", others)
	}
	b.WriteString(")")
	return b.String(), nil
}

func fileDecls(path string, pats []*regexp.Regexp) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > 2<<20 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	swift := strings.HasSuffix(path, ".swift") // members are indented but worth listing
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || ((line[0] == ' ' || line[0] == '\t') && !swift) {
			continue // top-level declarations only
		}
		trimmed := strings.TrimLeft(line, " \t")
		for _, re := range pats {
			if m := re.FindStringSubmatch(trimmed); m != nil {
				name := m[len(m)-1]
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
				break
			}
		}
	}
	return out
}
