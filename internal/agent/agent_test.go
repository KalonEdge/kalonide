package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
)

// script is a fake model: each call returns the next step.
type step struct {
	resp *llm.Response
	err  error
	// check inspects the request, e.g. to assert tools were dropped.
	check func(t *testing.T, req llm.Request)
}

type script struct {
	t     *testing.T
	mu    sync.Mutex
	steps []step
	reqs  []llm.Request
}

func (s *script) Chat(ctx context.Context, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected extra model call %d", len(s.reqs))
	}
	st := s.steps[0]
	s.steps = s.steps[1:]
	if st.check != nil {
		st.check(s.t, req)
	}
	if st.resp != nil && onDelta != nil && st.resp.Content != "" {
		onDelta(llm.Delta{Content: st.resp.Content})
	}
	return st.resp, st.err
}

func text(s string) step { return step{resp: &llm.Response{Content: s, FinishReason: "stop"}} }

func call(name string, args any) step {
	b, _ := json.Marshal(args)
	return step{resp: &llm.Response{ToolCalls: []llm.ToolCall{{ID: "c-" + name, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: string(b)}}}}}
}

func newAgent(t *testing.T, steps ...step) (*Agent, *script) {
	t.Helper()
	root, _ := tools.RealPath(t.TempDir())
	s := &script{t: t, steps: steps}
	a := &Agent{
		Client: s, Model: "m", Tools: tools.Default(),
		Env:          &tools.Env{Root: root, ProjectMemoryDir: filepath.Join(root, ".mem")},
		Gate:         AllowAll{},
		SystemPrompt: func(string, bool) string { return "sys" },
	}
	return a, s
}

func lastTool(a *Agent) llm.Message {
	for i := len(a.Messages) - 1; i >= 0; i-- {
		if a.Messages[i].Role == llm.RoleTool {
			return a.Messages[i]
		}
	}
	return llm.Message{}
}

func TestToolLoopRunsToolsAndFinishes(t *testing.T) {
	a, s := newAgent(t,
		call("write_file", map[string]string{"path": "hello.txt", "content": "hi"}),
		call("read_file", map[string]string{"path": "hello.txt"}),
		text("Done."),
	)
	res, err := a.Run(context.Background(), "make hello.txt")
	if err != nil || res.Text != "Done." || res.Turns != 3 || res.Stop != StopDone {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(lastTool(a).Content, "1\thi") {
		t.Fatalf("tool result not fed back: %+v", lastTool(a))
	}
	// Every request starts with the system prompt and carries tools.
	for _, r := range s.reqs {
		if r.Messages[0].Role != llm.RoleSystem || len(r.Tools) == 0 {
			t.Fatal("bad request shape")
		}
	}
	assertPairing(t, a.Messages)
}

// assertPairing checks every tool call has exactly one result.
func assertPairing(t *testing.T, msgs []llm.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			pending[c.ID] = true
		}
		if m.Role == llm.RoleTool {
			if !pending[m.ToolCallID] {
				t.Fatalf("orphan tool result %s", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("tool calls without results: %v", pending)
	}
}

func TestTextToolCallsAreRecovered(t *testing.T) {
	a, _ := newAgent(t,
		text("Let me look.\n<tool_call>\n{\"name\": \"list_directory\", \"arguments\": {\"path\": \".\"}}\n</tool_call>"),
		text("```json\n{\"name\":\"glob\",\"parameters\":{\"pattern\":\"*.go\"}}\n```"),
		text("{\"tool\": \"not_a_tool\", \"args\": {}}"),
	)
	res, err := a.Run(context.Background(), "look")
	if err != nil {
		t.Fatal(err)
	}
	// The third reply names no real tool, so it is the final answer.
	if res.Turns != 3 || !strings.Contains(res.Text, "not_a_tool") {
		t.Fatalf("%+v", res)
	}
	if a.Messages[1].Content != "Let me look." || a.Messages[1].ToolCalls[0].Function.Name != "list_directory" {
		t.Fatalf("%+v", a.Messages[1])
	}
	assertPairing(t, a.Messages)
}

func TestBadArgumentsAreRepairedOrReported(t *testing.T) {
	a, _ := newAgent(t,
		step{resp: &llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Function: llm.FunctionCall{Name: "list_directory", Arguments: `{"path": "."`}}}}},
		step{resp: &llm.Response{ToolCalls: []llm.ToolCall{{ID: "2", Function: llm.FunctionCall{Name: "read_file", Arguments: `path=x`}}}}},
		step{resp: &llm.Response{ToolCalls: []llm.ToolCall{{ID: "3", Function: llm.FunctionCall{Name: "nope", Arguments: `{}`}}}}},
		text("ok"),
	)
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	results := []string{}
	for _, m := range a.Messages {
		if m.Role == llm.RoleTool {
			results = append(results, m.Content)
		}
	}
	if strings.HasPrefix(results[0], "Error") {
		t.Errorf("truncated JSON should be repaired: %s", results[0])
	}
	if !strings.Contains(results[1], "not valid JSON") {
		t.Errorf("bad JSON not reported: %s", results[1])
	}
	if !strings.Contains(results[2], "no tool named") || !strings.Contains(results[2], "read_file") {
		t.Errorf("unknown tool not reported: %s", results[2])
	}
}

func TestToolsRejectedFallsBackToPlainChat(t *testing.T) {
	a, _ := newAgent(t,
		step{err: &llm.Error{Kind: llm.KindToolsRejected, Status: 400, Msg: "does not support tools"}},
		step{resp: &llm.Response{Content: "plain"}, check: func(t *testing.T, r llm.Request) {
			if len(r.Tools) != 0 {
				t.Error("tools still sent")
			}
		}},
	)
	var notes []string
	a.Sink = noteSink{&notes}
	res, err := a.Run(context.Background(), "hi")
	if err != nil || res.Text != "plain" || len(notes) != 1 {
		t.Fatalf("%+v %v %v", res, err, notes)
	}
}

func TestOverloadSwitchesToFallbackModel(t *testing.T) {
	a, _ := newAgent(t,
		step{err: &llm.Error{Kind: llm.KindOverloaded, Status: 503, Msg: "busy"}},
		step{resp: &llm.Response{Content: "from b"}, check: func(t *testing.T, r llm.Request) {
			if r.Model != "b" {
				t.Errorf("model %s", r.Model)
			}
		}},
	)
	a.Fallbacks = []string{"m", "b"}
	res, err := a.Run(context.Background(), "hi")
	if err != nil || res.Text != "from b" || a.Model != "b" {
		t.Fatalf("%+v %v model %s", res, err, a.Model)
	}
}

func TestContextOverflowCompactsAndRetries(t *testing.T) {
	a, s := newAgent(t,
		step{err: &llm.Error{Kind: llm.KindContextTooLong, Status: 400, Msg: "maximum context length"}},
		// prune had nothing to clear, so the agent summarizes:
		step{resp: &llm.Response{Content: "SUMMARY: user wants x"}, check: func(t *testing.T, r llm.Request) {
			if len(r.Tools) != 0 || !strings.Contains(r.Messages[1].Content, "older question") {
				t.Errorf("bad summarize request: %+v", r.Messages)
			}
		}},
		text("answer"),
	)
	a.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: "older question"},
		{Role: llm.RoleAssistant, Content: "older answer"},
		{Role: llm.RoleUser, Content: "mid question"},
		{Role: llm.RoleAssistant, Content: "mid answer"},
	}
	res, err := a.Run(context.Background(), "new question")
	if err != nil || res.Text != "answer" {
		t.Fatalf("%+v %v", res, err)
	}
	last := s.reqs[len(s.reqs)-1]
	joined := ""
	for _, m := range last.Messages {
		joined += m.Content + "|"
	}
	if !strings.Contains(joined, "SUMMARY") || strings.Contains(joined, "older question") || !strings.Contains(joined, "new question") {
		t.Fatalf("history not compacted: %s", joined)
	}
}

func TestContextOverflowGivesUpAfterLadder(t *testing.T) {
	overflow := step{err: &llm.Error{Kind: llm.KindContextTooLong, Msg: "too long"}}
	a, _ := newAgent(t, overflow, step{err: errors.New("summarizer down")}, overflow)
	_, err := a.Run(context.Background(), "q")
	if llm.KindOf(err) != llm.KindContextTooLong {
		t.Fatalf("expected the overflow error after the ladder, got %v", err)
	}
}

func TestProactivePruneOfOldToolOutput(t *testing.T) {
	a, _ := newAgent(t, text("ok"))
	a.ContextWindow = 32768 // 6 outputs overflow it; pruning the old 4 is enough
	big := strings.Repeat("x", 20000)
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		a.Messages = append(a.Messages,
			llm.Message{Role: llm.RoleUser, Content: "q"},
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Name: "read_file", Content: big},
		)
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Messages[2].Content, "cleared") {
		t.Fatalf("old output not pruned: %d bytes", len(a.Messages[2].Content))
	}
	assertPairing(t, a.Messages)
}

func TestTruncatedReplyIsContinued(t *testing.T) {
	a, _ := newAgent(t,
		step{resp: &llm.Response{Content: "part one ", FinishReason: "length"}},
		text("part two"),
	)
	res, err := a.Run(context.Background(), "long")
	if err != nil || res.Text != "part two" || res.Turns != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestEmptyReplyIsNudgedOnce(t *testing.T) {
	a, _ := newAgent(t, text(""), text("real"))
	res, err := a.Run(context.Background(), "q")
	if err != nil || res.Text != "real" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMaxTurns(t *testing.T) {
	a, _ := newAgent(t, call("list_directory", map[string]string{}), call("list_directory", map[string]string{}))
	a.MaxTurns = 2
	res, err := a.Run(context.Background(), "loop")
	if !errors.Is(err, ErrMaxTurns) || res.Stop != StopMaxTurns {
		t.Fatalf("%+v %v", res, err)
	}
	assertPairing(t, a.Messages)
}

type denyGate struct{}

func (denyGate) Allow(context.Context, tools.Tool, string, string) (bool, error) { return false, nil }

func TestDeniedToolTellsModel(t *testing.T) {
	a, _ := newAgent(t, call("bash", map[string]string{"command": "rm -rf /"}), text("ok"))
	a.Gate = denyGate{}
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if out := lastTool(a).Content; !strings.Contains(out, "explicit approval") {
		t.Fatalf("%s", out)
	}
}

func TestDangerousCallsBlockedByAllowAll(t *testing.T) {
	a, _ := newAgent(t, call("bash", map[string]string{"command": "git push --force"}), text("ok"))
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastTool(a).Content, "Blocked") {
		t.Fatal(lastTool(a).Content)
	}
}

func TestRepeatedFailureWarning(t *testing.T) {
	read := call("read_file", map[string]string{"path": "missing.txt"})
	a, _ := newAgent(t, read, read, read, text("gave up"))
	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastTool(a).Content, "failed 3 times") {
		t.Fatal(lastTool(a).Content)
	}
}

type cancelChatter struct{ cancel context.CancelFunc }

func (c cancelChatter) Chat(ctx context.Context, req llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	c.cancel()
	return &llm.Response{ToolCalls: []llm.ToolCall{
		{ID: "1", Function: llm.FunctionCall{Name: "list_directory", Arguments: "{}"}},
		{ID: "2", Function: llm.FunctionCall{Name: "list_directory", Arguments: "{}"}},
	}}, nil
}

func TestCancelKeepsHistoryValid(t *testing.T) {
	a, _ := newAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.Client = cancelChatter{cancel}
	res, err := a.Run(ctx, "x")
	if !errors.Is(err, context.Canceled) || res.Stop != StopCancelled {
		t.Fatalf("%+v %v", res, err)
	}
	assertPairing(t, a.Messages)
}

func TestRepairJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1`:                     `{"a": 1}`,
		`{"a": "x`:                    `{"a": "x"}`,
		"```json\n{\"a\":[1,2]}\n```": `{"a":[1,2]}`,
		`Sure: {"a":1} hope it helps`: `{"a":1}`,
	} {
		got, ok := repairJSON(in)
		if !ok || got != want {
			t.Errorf("repairJSON(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

type noteSink struct{ notes *[]string }

func (noteSink) Text(string)                          {}
func (noteSink) Thinking(string)                      {}
func (noteSink) ToolStart(string, string, string)     {}
func (noteSink) ToolEnd(string, string, string, bool) {}
func (n noteSink) Notice(s string)                    { *n.notes = append(*n.notes, s) }

func TestAnnouncedButUntakenStepIsNudged(t *testing.T) {
	a, s := newAgent(t,
		text("I have edited mathx.go. Now I will run go test again to confirm the fix."),
		call("list_directory", map[string]string{}),
		text("Done: the tests pass."),
	)
	res, err := a.Run(context.Background(), "fix it")
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 3 || len(s.steps) != 0 || res.Text != "Done: the tests pass." {
		t.Fatalf("%+v", res)
	}
}

func TestNudgesAreCapped(t *testing.T) {
	announce := text("I will start by listing the directory.")
	a, s := newAgent(t, announce, announce, announce, announce)
	res, err := a.Run(context.Background(), "fix it")
	if err != nil {
		t.Fatal(err)
	}
	// Three nudges, then the fourth announcement is accepted as final.
	if res.Turns != 1+maxNudges || len(s.steps) != 0 || res.Stop != StopDone {
		t.Fatalf("turns %d left %d %+v", res.Turns, len(s.steps), res)
	}
}

func TestLostToolCallIsRetried(t *testing.T) {
	a, s := newAgent(t,
		step{resp: &llm.Response{Content: "Let me read the file.", FinishReason: "stop", ToolCallLost: true}},
		call("list_directory", map[string]string{}),
		text("ok"),
	)
	var notes []string
	a.Sink = noteSink{&notes}
	res, err := a.Run(context.Background(), "x")
	if err != nil || res.Text != "ok" || len(s.steps) != 0 || len(notes) != 1 {
		t.Fatalf("%+v %v notes=%v", res, err, notes)
	}
	if !strings.Contains(a.Messages[2].Content, "did not come through") {
		t.Fatalf("wrong nudge: %q", a.Messages[2].Content)
	}
}

func TestAnnouncesNextStep(t *testing.T) {
	yes := []string{
		"Done editing.\n\nNow I will run the tests.", "Let me look at the config file:", "I'll now apply the fix",
		"Next, I'm going to update the docs.", "I will start by listing the directory to get an overview.",
		"I will use `grep` for this.", "Calling `grep` now.", "First, valid port:", "I need to read port.go first.",
	}
	no := []string{
		"I fixed the loop and the tests pass.", "Let me know if you want more changes.", "",
		"The bug was in line 9. I changed it and confirmed with go test.",
		"It listens on port 8087 by default; set DASH_LISTEN_PORT to change it.", "internal/conf/load.go",
		"All done. If you want, I can also add tests.",
	}
	for _, s := range yes {
		if !announcesNextStep(s) {
			t.Errorf("missed: %q", s)
		}
	}
	for _, s := range no {
		if announcesNextStep(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

func TestTaskToolRunsAHelper(t *testing.T) {
	a, s := newAgent(t,
		call("task", map[string]string{"description": "find main", "prompt": "Where is main defined?"}),
		// The helper's own calls, on the same scripted model:
		step{resp: &llm.Response{ToolCalls: []llm.ToolCall{{ID: "h1", Function: llm.FunctionCall{Name: "list_directory", Arguments: "{}"}}}},
			check: func(t *testing.T, r llm.Request) {
				for _, tool := range r.Tools {
					if tool.Function.Name == "task" || tool.Function.Name == "bash" || tool.Function.Name == "write_file" {
						t.Errorf("helper was offered %s", tool.Function.Name)
					}
				}
				if !strings.Contains(r.Messages[0].Content, "helper") || r.Messages[1].Content != "Where is main defined?" {
					t.Errorf("helper request: %+v", r.Messages)
				}
			}},
		text("main is in cmd/app/main.go line 3"),
		text("Done."),
	)
	task := &TaskTool{Parent: a}
	a.Tools.Add(task)
	var events []string
	a.Sink = recordSink{&events}
	res, err := a.Run(context.Background(), "where is main?")
	if err != nil || res.Text != "Done." || len(s.steps) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if out := lastTool(a).Content; !strings.Contains(out, "main is in cmd/app/main.go line 3") || !strings.Contains(out, "helper: 2 model calls") {
		t.Fatalf("task result: %q", out)
	}
	if !strings.Contains(strings.Join(events, "|"), "start:task › list_directory") {
		t.Fatalf("helper activity not shown: %v", events)
	}
	// The helper's messages stay out of the main conversation.
	for _, m := range a.Messages {
		if m.ToolCallID == "h1" {
			t.Fatal("helper message leaked into the main conversation")
		}
	}
}

type recordSink struct{ events *[]string }

func (recordSink) Text(string)     {}
func (recordSink) Thinking(string) {}
func (r recordSink) ToolStart(id, name, summary string) {
	*r.events = append(*r.events, "start:"+name)
}
func (recordSink) ToolEnd(string, string, string, bool) {}
func (recordSink) Notice(string)                        {}

func TestSelfCheckMakesTheModelFixFailures(t *testing.T) {
	a, s := newAgent(t,
		call("write_file", map[string]string{"path": "x.go", "content": "package x"}),
		text("Done."),
		call("edit_file", map[string]string{"path": "x.go", "old_string": "package x", "new_string": "package y"}),
		text("Fixed."),
	)
	runs := 0
	a.SelfCheck = func(context.Context) (bool, string) {
		runs++
		return runs > 1, "FAIL  go build ./...\n    x.go: wrong package"
	}
	res, err := a.Run(context.Background(), "make x.go")
	if err != nil || res.Text != "Fixed." || runs != 2 || len(s.steps) != 0 {
		t.Fatalf("%+v %v runs=%d", res, err, runs)
	}
	found := false
	for _, m := range a.Messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "wrong package") {
			found = true
		}
	}
	if !found {
		t.Fatal("the failure report never reached the model")
	}

	// No file changes: no checks.
	b, _ := newAgent(t, text("Just an answer."))
	b.SelfCheck = func(context.Context) (bool, string) { t.Fatal("checks ran without changes"); return true, "" }
	if _, err := b.Run(context.Background(), "explain"); err != nil {
		t.Fatal(err)
	}
}
