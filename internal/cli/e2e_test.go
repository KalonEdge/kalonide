package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeModel is a scripted OpenAI-compatible server. It decides its reply
// from the conversation, so it is stateless.
func fakeModel(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"fake-coder"},{"id":"fake-small"}]}`)
			return
		case "/v1/chat/completions":
		default:
			w.WriteHeader(404)
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				RawBody json.RawMessage `json:"content"`
				Content string          `json:"-"`
				Images  int             `json:"-"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for i := range req.Messages {
			m := &req.Messages[i]
			if json.Unmarshal(m.RawBody, &m.Content) != nil {
				var parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				json.Unmarshal(m.RawBody, &parts)
				for _, p := range parts {
					if p.Type == "image_url" {
						m.Images++
					}
					m.Content += p.Text
				}
			}
		}
		last := req.Messages[len(req.Messages)-1]
		users := 0
		loop := false
		for _, m := range req.Messages {
			if m.Role == "user" {
				users++
				loop = loop || strings.Contains(m.Content, "LOOP")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(chunk string) { fmt.Fprintf(w, "data: %s\n\n", chunk) }
		toolCall := func(name, args string) {
			b, _ := json.Marshal(args)
			send(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, name, b))
		}
		textReply := func(s string) {
			b, _ := json.Marshal(s)
			send(fmt.Sprintf(`{"choices":[{"delta":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, b))
		}
		switch {
		case loop:
			toolCall("list_directory", `{}`)
		case last.Role == "tool":
			c := last.Content
			if len(c) > 80 {
				c = c[:80]
			}
			textReply("RESULT: " + c)
		case strings.Contains(last.Content, "MCPREAD"):
			toolCall("mcp__fake__echo", `{"text":"zz"}`)
		case strings.Contains(last.Content, "MCPWRITE"):
			toolCall("mcp__fake__store", `{"key":"k"}`)
		case strings.Contains(last.Content, "MAKEFILE"):
			toolCall("write_file", `{"path":"notes.txt","content":"hello\n"}`)
		default:
			textReply(fmt.Sprintf("echo: %s | users=%d | model=%s | images=%d", last.Content, users, req.Model, last.Images))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kalonide-e2e")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "kalonide")
	out, err := exec.Command("go", "build", "-o", binPath, "../../cmd/kalonide").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	stdout, stderr string
	code           int
}

func kalonide(t *testing.T, env []string, dir string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "NO_COLOR=1", "KALONIDE_RECONNECT_WAIT=0")
	cmd.Stdin = strings.NewReader("")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func setup(t *testing.T) ([]string, string) {
	srv := fakeModel(t)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	work := t.TempDir()
	env := []string{"KALONIDE_HOME=" + home, "KALONIDE_BASE_URL=" + srv.URL + "/v1", "KALONIDE_PROVIDER=", "KALONIDE_MODEL=", "KALONIDE_API_KEY="}
	return env, work
}

func TestExecTextPicksFirstModelAndAnswers(t *testing.T) {
	env, work := setup(t)
	r := kalonide(t, env, work, "exec", "hello there")
	if r.code != 0 || !strings.Contains(r.stdout, "echo: hello there | users=1 | model=fake-coder") {
		t.Fatalf("code %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "using fake-coder") {
		t.Fatalf("expected model notice on stderr: %s", r.stderr)
	}
}

func TestExecRefusesWritesWithoutPermission(t *testing.T) {
	env, work := setup(t)
	r := kalonide(t, env, work, "exec", "MAKEFILE")
	if r.code != 0 {
		t.Fatalf("code %d %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(work, "notes.txt")); err == nil {
		t.Fatal("file written without permission")
	}
	if !strings.Contains(r.stdout, "did not allow") || !strings.Contains(r.stderr, "refused") {
		t.Fatalf("stdout %s\nstderr %s", r.stdout, r.stderr)
	}

	r = kalonide(t, env, work, "exec", "--auto-edit", "MAKEFILE")
	b, err := os.ReadFile(filepath.Join(work, "notes.txt"))
	if r.code != 0 || err != nil || string(b) != "hello\n" || !strings.Contains(r.stdout, "RESULT: Created notes.txt") {
		t.Fatalf("code %d file %q err %v\n%s\n%s", r.code, b, err, r.stdout, r.stderr)
	}
}

func TestExecJSONAndStreamJSON(t *testing.T) {
	env, work := setup(t)
	r := kalonide(t, env, work, "exec", "-o", "json", "--yolo", "MAKEFILE")
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	if out["schema"] != "kalonide.exec.v1" || out["stop"] != "done" || !strings.HasPrefix(out["text"].(string), "RESULT") || len(out["tools"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}

	r = kalonide(t, env, work, "exec", "-o", "stream-json", "hi")
	sc := bufio.NewScanner(strings.NewReader(r.stdout))
	var types []string
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad line %q", sc.Text())
		}
		if ev["schema"] != "kalonide.stream.v1" {
			t.Fatalf("schema %v", ev["schema"])
		}
		types = append(types, ev["type"].(string))
	}
	if len(types) < 2 || types[len(types)-1] != "result" {
		t.Fatalf("events %v", types)
	}
}

func TestExecResumeContinuesSession(t *testing.T) {
	env, work := setup(t)
	if r := kalonide(t, env, work, "exec", "first"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r := kalonide(t, env, work, "exec", "--resume", "second")
	if r.code != 0 || !strings.Contains(r.stdout, "users=2") {
		t.Fatalf("resume did not carry history: %s %s", r.stdout, r.stderr)
	}
	r = kalonide(t, env, work, "sessions")
	if r.code != 0 || !strings.Contains(r.stdout, "first") || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("sessions list: %q", r.stdout)
	}
}

func TestExitCodes(t *testing.T) {
	env, work := setup(t)
	if r := kalonide(t, env, work, "exec", "--max-turns", "2", "LOOP"); r.code != 3 {
		t.Fatalf("max turns: code %d %s", r.code, r.stderr)
	}
	if r := kalonide(t, env, work, "exec", "--bogus", "x"); r.code != 2 {
		t.Fatalf("bad flag: code %d", r.code)
	}
	if r := kalonide(t, env, work, "exec"); r.code != 2 {
		t.Fatalf("no prompt: code %d", r.code)
	}
	down := []string{"KALONIDE_HOME=" + t.TempDir(), "KALONIDE_BASE_URL=http://127.0.0.1:1/v1", "KALONIDE_MODEL=x"}
	r := kalonide(t, down, work, "exec", "hi")
	if r.code != 1 || !strings.Contains(r.stderr, "cannot reach") {
		t.Fatalf("unreachable: code %d %s", r.code, r.stderr)
	}
}

func TestPipedPromptAndFile(t *testing.T) {
	env, work := setup(t)
	os.WriteFile(filepath.Join(work, "p.txt"), []byte("from file"), 0o644)
	r := kalonide(t, env, work, "exec", "-f", "p.txt")
	if !strings.Contains(r.stdout, "echo: from file") {
		t.Fatalf("%s %s", r.stdout, r.stderr)
	}
}

func TestModelsProvidersConfigDoctor(t *testing.T) {
	env, work := setup(t)
	r := kalonide(t, env, work, "models")
	if r.code != 0 || !strings.Contains(r.stdout, "fake-coder") || !strings.Contains(r.stdout, "fake-small") {
		t.Fatalf("models: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if r = kalonide(t, env, work, "models", "use", "fake-small"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r = kalonide(t, env, work, "exec", "which model")
	if !strings.Contains(r.stdout, "model=fake-small") {
		t.Fatalf("models use not applied: %s", r.stdout)
	}
	if r = kalonide(t, env, work, "providers", "add", "other", "--base-url", "localhost:9", "--api-key-env", "OTHER_KEY"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r = kalonide(t, env, work, "providers")
	if !strings.Contains(r.stdout, "other") || !strings.Contains(r.stdout, "http://localhost:9/v1") || !strings.Contains(r.stdout, "not set") {
		t.Fatalf("providers: %s", r.stdout)
	}
	if r = kalonide(t, env, work, "config", "set", "max-turns", "7"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r = kalonide(t, env, work, "config", "--json")
	if !strings.Contains(r.stdout, `"maxTurns": 7`) {
		t.Fatalf("config: %s", r.stdout)
	}
	r = kalonide(t, env, work, "doctor")
	if r.code != 0 || !strings.Contains(r.stdout, "connection") || !strings.Contains(r.stdout, "2 model(s)") {
		t.Fatalf("doctor: %d %s", r.code, r.stdout)
	}
	if r = kalonide(t, env, work, "config", "set", "permission-mode", "reckless"); r.code != 1 {
		t.Fatalf("bad mode accepted: %d", r.code)
	}
}

func TestShellNeedsTerminal(t *testing.T) {
	env, work := setup(t)
	r := kalonide(t, env, work)
	if r.code != 2 || !strings.Contains(r.stderr, "needs a terminal") {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
}

func TestExecDoesNotWaitOnIdleStdin(t *testing.T) {
	env, work := setup(t)
	r, w, err := os.Pipe() // open, but nothing is ever written
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cmd := exec.Command(binPath, "exec", "hello")
	cmd.Dir, cmd.Stdin = work, r
	cmd.Env = append(append(os.Environ(), env...), "NO_COLOR=1")
	done := make(chan error, 1)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(out.String(), "echo: hello") {
			t.Fatalf("err %v out %q", err, out.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("exec hung waiting on an idle stdin")
	}

	// Piped input that does arrive is still used alongside the prompt.
	cmd = exec.Command(binPath, "exec", "summarize")
	cmd.Dir, cmd.Stdin = work, strings.NewReader("piped text")
	cmd.Env = append(append(os.Environ(), env...), "NO_COLOR=1")
	b, _ := cmd.Output()
	if !strings.Contains(string(b), "piped text") {
		t.Fatalf("piped input lost: %q", b)
	}
}

func TestExecSendsImages(t *testing.T) {
	env, work := setup(t)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	f, _ := os.Create(filepath.Join(work, "pic.png"))
	png.Encode(f, img)
	f.Close()
	r := kalonide(t, env, work, "exec", "--image", "pic.png", "describe it")
	if r.code != 0 || !strings.Contains(r.stdout, "echo: describe it") || !strings.Contains(r.stdout, "images=1") {
		t.Fatalf("code %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	os.WriteFile(filepath.Join(work, "fake.png"), []byte("not an image"), 0o644)
	if r := kalonide(t, env, work, "exec", "--image", "fake.png", "x"); r.code != 2 || !strings.Contains(r.stderr, "not a PNG") {
		t.Fatalf("bad image accepted: %d %s", r.code, r.stderr)
	}
}

const fakeMCPServer = `
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    m = json.loads(line)
    if "id" not in m:
        continue
    if m["method"] == "initialize":
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"protocolVersion": "2025-06-18", "serverInfo": {"name": "fake"}, "capabilities": {}}})
    elif m["method"] == "tools/list":
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"tools": [
            {"name": "echo", "description": "Echo", "inputSchema": {"type": "object"}, "annotations": {"readOnlyHint": True}},
            {"name": "store", "description": "Store", "inputSchema": {"type": "object"}}]}})
    elif m["method"] == "tools/call":
        a = m["params"]["arguments"]
        send({"jsonrpc": "2.0", "id": m["id"], "result": {"content": [{"type": "text", "text": "mcp says " + json.dumps(a, sort_keys=True)}]}})
`

func TestMCPServerToolsReachTheModel(t *testing.T) {
	env, work := setup(t)
	script := filepath.Join(t.TempDir(), "server.py")
	os.WriteFile(script, []byte(fakeMCPServer), 0o644)
	if r := kalonide(t, env, work, "mcp", "add", "fake", "--", "python3", "-I", script); r.code != 0 {
		t.Fatalf("add: %s", r.stderr)
	}
	r := kalonide(t, env, work, "mcp")
	if r.code != 0 || !strings.Contains(r.stdout, "2 tools: echo, store") {
		t.Fatalf("list: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// The server's read-only hint alone is not trusted.
	r = kalonide(t, env, work, "exec", "MCPREAD")
	if !strings.Contains(r.stdout, "did not allow") {
		t.Fatalf("server-claimed read-only tool ran without approval: %s %s", r.stdout, r.stderr)
	}
	kalonide(t, env, work, "mcp", "remove", "fake")
	if r := kalonide(t, env, work, "mcp", "add", "fake", "--auto-approve", "echo", "--", "python3", "-I", script); r.code != 0 {
		t.Fatalf("re-add: %s", r.stderr)
	}
	r = kalonide(t, env, work, "exec", "MCPREAD")
	if r.code != 0 || !strings.Contains(r.stdout, `RESULT: mcp says {"text": "zz"}`) {
		t.Fatalf("user-approved MCP tool: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "mcp", "add", "Bad__Name", "--", "x"); r.code != 2 {
		t.Fatalf("bad server name accepted: %d", r.code)
	}
	r = kalonide(t, env, work, "exec", "MCPWRITE")
	if !strings.Contains(r.stdout, "did not allow") || !strings.Contains(r.stderr, "refused") {
		t.Fatalf("MCP tool with side effects ran without approval: %s %s", r.stdout, r.stderr)
	}
	r = kalonide(t, env, work, "exec", "--yolo", "MCPWRITE")
	if !strings.Contains(r.stdout, `RESULT: mcp says {"key": "k"}`) {
		t.Fatalf("yolo MCP call: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "mcp", "disable", "fake"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r = kalonide(t, env, work, "exec", "--list-tools")
	if strings.Contains(r.stdout, "mcp__fake") {
		t.Fatal("disabled server's tools still offered")
	}
	// A broken server is a warning, not a failure.
	kalonide(t, env, work, "mcp", "add", "broken", "--", "/no/such/binary")
	r = kalonide(t, env, work, "exec", "hello")
	if r.code != 0 || !strings.Contains(r.stderr, "MCP server broken is unavailable") {
		t.Fatalf("broken server: %d %s", r.code, r.stderr)
	}
}

func TestCustomCommandsAndSkills(t *testing.T) {
	env, work := setup(t)
	os.MkdirAll(filepath.Join(work, ".kalonide", "commands"), 0o755)
	os.WriteFile(filepath.Join(work, ".kalonide", "commands", "hello.md"), []byte("Say hello to $ARGUMENTS"), 0o644)
	// An untrusted folder's commands are not used.
	r := kalonide(t, env, work, "exec", "/hello world")
	if strings.Contains(r.stdout, "Say hello") || !strings.Contains(r.stderr, "not trusted") {
		t.Fatalf("untrusted command used: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "trust"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r = kalonide(t, env, work, "exec", "/hello world")
	if !strings.Contains(r.stdout, "echo: Say hello to world") || strings.Contains(r.stderr, "not trusted") {
		t.Fatalf("custom command not expanded after trust: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "exec", "--list-tools"); strings.Contains(r.stdout, "skill ") {
		t.Fatal("skill tool offered without skills")
	}
	os.MkdirAll(filepath.Join(work, ".kalonide", "skills"), 0o755)
	os.WriteFile(filepath.Join(work, ".kalonide", "skills", "deploy.md"), []byte("---\ndescription: How to deploy\n---\nRun make deploy."), 0o644)
	if r := kalonide(t, env, work, "exec", "--list-tools"); !strings.Contains(r.stdout, "skill ") {
		t.Fatalf("skill tool missing: %s", r.stdout)
	}
}

func TestHooksAroundToolCalls(t *testing.T) {
	env, work := setup(t)
	home := strings.TrimPrefix(env[0], "KALONIDE_HOME=")
	settings := `{"hooks": [
		{"event": "after_tool", "tools": ["write_file"], "command": "echo formatted $(basename $KALONIDE_FILE)"},
		{"event": "turn_end", "command": "echo all done"}
	]}`
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0o600)
	r := kalonide(t, env, work, "exec", "-o", "stream-json", "--auto-edit", "MAKEFILE")
	if !strings.Contains(r.stdout, "formatted notes.txt") || !strings.Contains(r.stdout, "all done") {
		t.Fatalf("after_tool / turn_end hook output missing:\n%s\n%s", r.stdout, r.stderr)
	}

	os.Remove(filepath.Join(work, "notes.txt"))
	settings = `{"hooks": [{"event": "before_tool", "tools": ["write_file"], "command": "echo no new files today; exit 1"}, {"event": "bogus", "command": "x"}]}`
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0o600)
	r = kalonide(t, env, work, "exec", "--auto-edit", "MAKEFILE")
	if _, err := os.Stat(filepath.Join(work, "notes.txt")); err == nil {
		t.Fatal("blocked write happened")
	}
	if !strings.Contains(r.stdout, "Blocked by one of the user") || !strings.Contains(r.stderr, "hook 2 in settings is ignored") {
		t.Fatalf("before_tool block / invalid hook warning:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestVerifyCommand(t *testing.T) {
	env, _ := setup(t)
	work := t.TempDir()
	src := filepath.Join("..", "..", "bench", "testdata", "tasks", "01-go-off-by-one")
	for _, sub := range []string{"repo", "hidden"} {
		filepath.Walk(filepath.Join(src, sub), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				data, _ := os.ReadFile(p)
				os.WriteFile(filepath.Join(work, info.Name()), data, 0o644)
			}
			return nil
		})
	}
	r := kalonide(t, env, work, "verify")
	if r.code != 1 || !strings.Contains(r.stdout, "✘ go test ./...") || !strings.Contains(r.stdout, "Average") {
		t.Fatalf("broken project: %d\n%s", r.code, r.stdout)
	}
	fixed, _ := os.ReadFile(filepath.Join(src, "solution", "stats.go"))
	os.WriteFile(filepath.Join(work, "stats.go"), fixed, 0o644)
	if r := kalonide(t, env, work, "verify", "--json"); r.code != 0 || !strings.Contains(r.stdout, `"passed": true`) {
		t.Fatalf("fixed project: %d\n%s", r.code, r.stdout)
	}
}

func TestAuditLog(t *testing.T) {
	env, work := setup(t)
	kalonide(t, env, work, "exec", "MAKEFILE")                                   // refused: no --auto-edit
	r := kalonide(t, env, work, "exec", "-o", "json", "--auto-edit", "MAKEFILE") // runs
	var out map[string]any
	json.Unmarshal([]byte(r.stdout), &out)
	id, _ := out["session_id"].(string)
	r = kalonide(t, env, work, "sessions", "audit", id)
	if r.code != 0 || !strings.Contains(r.stdout, "write_file") || !strings.Contains(r.stdout, "ran") {
		t.Fatalf("audit of the allowed run:\n%s %s", r.stdout, r.stderr)
	}
	list := kalonide(t, env, work, "sessions", "--json")
	var sessions []map[string]any
	json.Unmarshal([]byte(list.stdout), &sessions)
	var deniedID string
	for _, s := range sessions {
		if s["ID"] != id {
			deniedID, _ = s["ID"].(string)
		}
	}
	r = kalonide(t, env, work, "sessions", "audit", deniedID, "--json")
	if !strings.Contains(r.stdout, `"decision":"denied"`) || !strings.Contains(r.stdout, `"tool":"write_file"`) {
		t.Fatalf("audit of the refused run:\n%s", r.stdout)
	}
	if len(sessions) != 2 {
		t.Fatalf("audit files must not show up as sessions: %d", len(sessions))
	}
}

func TestChangesSearchUsage(t *testing.T) {
	env, work := setup(t)
	gitc := func(args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", work, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...).CombinedOutput()
		return string(out)
	}
	gitc("init", "-q")
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	if r := kalonide(t, env, work, "changes"); !strings.Contains(r.stdout, "a.txt") {
		t.Fatalf("changes: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "changes", "commit"); r.code != 2 {
		t.Fatal("commit without a message accepted")
	}
	r := kalonide(t, env, work, "changes", "commit", "-m", "Add a")
	if r.code != 0 || !strings.Contains(gitc("log", "--oneline"), "Add a") {
		t.Fatalf("commit -m: %s %s", r.stdout, r.stderr)
	}
	os.WriteFile(filepath.Join(work, "b.txt"), []byte("two\n"), 0o644)
	gitEnv := append(env, "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
	r = kalonide(t, gitEnv, work, "changes", "commit", "--auto", "--yes")
	if r.code != 0 || !strings.Contains(gitc("log", "-1", "--format=%s"), "echo:") {
		t.Fatalf("commit --auto: %d %s %s", r.code, r.stdout, r.stderr)
	}

	kalonide(t, env, work, "exec", "please remember the purple elephant")
	r = kalonide(t, env, work, "search", "purple", "elephant")
	if r.code != 0 || !strings.Contains(r.stdout, "purple elephant") {
		t.Fatalf("search: %s %s", r.stdout, r.stderr)
	}
	if r := kalonide(t, env, work, "search", "nothing-matches-this"); !strings.Contains(r.stdout, "No matches") {
		t.Fatalf("search miss: %s", r.stdout)
	}
	r = kalonide(t, env, work, "usage")
	if r.code != 0 || !strings.Contains(r.stdout, "fake-coder") || !strings.Contains(r.stdout, "~") {
		t.Fatalf("usage: %s %s", r.stdout, r.stderr)
	}
}

func TestServeMCP(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.go"), []byte("package hello\n\nfunc Greet() string { return \"hi\" }\n"), 0o644)
	runServer := func(extra ...string) map[int]map[string]any {
		cmd := exec.Command(binPath, append([]string{"serve", "--mcp", "-C", work}, extra...)...)
		cmd.Stdin = strings.NewReader(strings.Join([]string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"grep","arguments":{"pattern":"Greet"}}}`,
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"bash","arguments":{"command":"echo hi"}}}`,
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"../../etc/passwd"}}}`,
		}, "\n") + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		replies := map[int]map[string]any{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				if id, ok := m["id"].(float64); ok {
					replies[int(id)] = m
				}
			}
		}
		return replies
	}
	r := runServer()
	if info := r[1]["result"].(map[string]any)["serverInfo"].(map[string]any); info["name"] != "kalonide" {
		t.Fatalf("initialize: %v", r[1])
	}
	list, _ := json.Marshal(r[2])
	if !strings.Contains(string(list), `"grep"`) || strings.Contains(string(list), `"bash"`) {
		t.Fatalf("read-only server offered %s", list)
	}
	got, _ := json.Marshal(r[3])
	if !strings.Contains(string(got), "hello.go:3") {
		t.Fatalf("grep call: %s", got)
	}
	bash, _ := json.Marshal(r[4])
	if !strings.Contains(string(bash), "no tool named bash") {
		t.Fatalf("bash ran on a read-only server: %s", bash)
	}
	esc, _ := json.Marshal(r[5])
	if !strings.Contains(string(esc), "outside the workspace") {
		t.Fatalf("path escape: %s", esc)
	}
	r = runServer("--allow-unsafe-tools")
	if b, _ := json.Marshal(r[4]); !strings.Contains(string(b), "hi") {
		t.Fatalf("bash with --allow-unsafe-tools: %s", b)
	}
}

func TestKalonideTalksToItselfOverMCP(t *testing.T) {
	env, work := setup(t)
	if r := kalonide(t, env, work, "mcp", "add", "self", "--", binPath, "serve", "--mcp", "-C", work); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r := kalonide(t, env, work, "mcp")
	if r.code != 0 || !strings.Contains(r.stdout, "5 tools: read_file, list_directory, glob, grep, repo_map") {
		t.Fatalf("self over MCP: %s %s", r.stdout, r.stderr)
	}
}

// acpClient plays the editor: it writes requests and reads every line.
type acpClient struct {
	t     *testing.T
	in    io.WriteCloser
	lines chan map[string]any
}

func (c *acpClient) send(v any) {
	b, _ := json.Marshal(v)
	c.in.Write(append(b, '\n'))
}

// until reads messages until match returns true, answering permission
// requests with answer. It returns the matching message.
func (c *acpClient) until(match func(map[string]any) bool, answer string, seen *[]map[string]any) map[string]any {
	timeout := time.After(20 * time.Second)
	for {
		select {
		case m, ok := <-c.lines:
			if !ok {
				c.t.Fatal("acp closed its output")
			}
			*seen = append(*seen, m)
			if m["method"] == "session/request_permission" {
				c.send(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": answer}}})
				continue
			}
			if match(m) {
				return m
			}
		case <-timeout:
			c.t.Fatalf("timed out; saw %v", *seen)
		}
	}
}

func TestACP(t *testing.T) {
	env, work := setup(t)
	cmd := exec.Command(binPath, "acp")
	cmd.Env = append(append(os.Environ(), env...), "NO_COLOR=1", "KALONIDE_RECONNECT_WAIT=0")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cmd.Wait() }()
	c := &acpClient{t: t, in: in, lines: make(chan map[string]any, 100)}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				c.lines <- m
			}
		}
		close(c.lines)
	}()
	var seen []map[string]any
	byID := func(id float64) func(map[string]any) bool {
		return func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id && m["method"] == nil }
	}

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}})
	init := c.until(byID(1), "", &seen)
	if init["result"].(map[string]any)["protocolVersion"] != 1.0 {
		t.Fatalf("initialize: %v", init)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": work, "mcpServers": []any{}}})
	sess := c.until(byID(2), "", &seen)["result"].(map[string]any)["sessionId"].(string)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sess, "prompt": []any{map[string]any{"type": "text", "text": "MAKEFILE"}}}})
	done := c.until(byID(3), "allow_once", &seen)
	if done["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("prompt: %v", done)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "notes.txt")); string(b) != "hi\n" && string(b) != "hello\n" {
		t.Fatalf("file not written after permission: %q", b)
	}
	all, _ := json.Marshal(seen)
	for _, want := range []string{`"session/request_permission"`, `"sessionUpdate":"tool_call"`, `"kind":"edit"`, `"status":"completed"`, `"sessionUpdate":"agent_message_chunk"`, "RESULT: Created notes.txt"} {
		if !strings.Contains(string(all), want) {
			t.Errorf("missing %s in the stream", want)
		}
	}
	// A denied call does not run.
	os.Remove(filepath.Join(work, "notes.txt"))
	seen = nil
	c.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/prompt", "params": map[string]any{"sessionId": sess, "prompt": []any{map[string]any{"type": "text", "text": "MAKEFILE"}}}})
	c.until(byID(4), "reject_once", &seen)
	if _, err := os.Stat(filepath.Join(work, "notes.txt")); err == nil {
		t.Fatal("denied write happened")
	}
}
