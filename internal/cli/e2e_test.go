package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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
	r = kalonide(t, env, work, "exec", "MCPREAD")
	if r.code != 0 || !strings.Contains(r.stdout, `RESULT: mcp says {"text": "zz"}`) {
		t.Fatalf("read-only MCP tool: %s %s", r.stdout, r.stderr)
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
