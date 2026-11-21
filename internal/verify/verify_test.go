package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) {
	os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
	os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

func labels(cs []Check) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Label)
	}
	return strings.Join(out, " | ")
}

func TestDetect(t *testing.T) {
	g := t.TempDir()
	write(t, g, "go.mod", "module x\n")
	if got := labels(Detect(g)); got != "go build ./... | go vet ./... | go test ./..." {
		t.Errorf("go: %s", got)
	}
	n := t.TempDir()
	write(t, n, "package.json", `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1","lint":"eslint .","build":"tsc"}}`)
	write(t, n, "pnpm-lock.yaml", "")
	if got := labels(Detect(n)); got != "pnpm run build | pnpm run lint" {
		t.Errorf("node: %s", got)
	}
	m := t.TempDir()
	write(t, m, "go.mod", "module x\n")
	write(t, m, "Makefile", "build:\n\tgo build\n\ntest: build\n\tgo test ./...\n")
	if got := labels(Detect(m)); got != "make build | make test" {
		t.Errorf("make wins: %s", got)
	}
	p := t.TempDir()
	write(t, p, "test_x.py", "")
	if got := labels(Detect(p)); !strings.Contains(got, "unittest") && !strings.Contains(got, "pytest") {
		t.Errorf("python: %s", got)
	}
	r := t.TempDir()
	write(t, r, "Cargo.toml", "")
	if got := labels(Detect(r)); got != "cargo build | cargo test" {
		t.Errorf("rust: %s", got)
	}
	if len(Detect(t.TempDir())) != 0 {
		t.Error("empty folder has checks")
	}
}

func TestRunStopsAtFirstFailure(t *testing.T) {
	checks := []Check{{Label: "ok", Command: "echo fine"}, {Label: "bad", Command: "echo broken >&2; exit 2"}, {Label: "later", Command: "true"}}
	res := Run(context.Background(), t.TempDir(), checks, time.Minute, false, nil)
	if len(res) != 2 || !res[0].Passed || res[1].Passed || res[1].Output != "broken" || AllPassed(res) {
		t.Fatalf("%+v", res)
	}
	sum := Summary(res, 1)
	if !strings.Contains(sum, "PASS  ok") || !strings.Contains(sum, "FAIL  bad") || !strings.Contains(sum, "    broken") || !strings.Contains(sum, "1 later check") {
		t.Fatalf("summary:\n%s", sum)
	}
	if all := Run(context.Background(), t.TempDir(), checks, time.Minute, true, nil); len(all) != 3 {
		t.Fatal("all=true must run every check")
	}
}
