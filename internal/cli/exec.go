package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KalonEdge/kalonide/internal/agent"
	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/ui"
)

// Exit codes.
const (
	exitOK        = 0
	exitError     = 1
	exitUsage     = 2
	exitMaxTurns  = 3
	exitCancelled = 130
)

const execHelp = `Run one prompt without the interactive shell, for scripts and CI.

Usage:
  kalonide exec [flags] <prompt>
  echo "<prompt>" | kalonide exec [flags]

Without --auto-edit or --yolo, file changes, shell commands and web access
are refused (nobody is there to approve them). Destructive commands are
always refused in exec.

Exit codes: 0 done · 1 error · 2 bad usage · 3 turn limit · 130 cancelled

Flags:
`

func cmdExec(args []string) int {
	var (
		o          runOptions
		promptFlag string
		file       string
		output     = "text"
		autoEdit   bool
		yolo       bool
		resume     bool
		resumeID   string
		fork       string
		title      string
		listTools  bool
		quiet      bool
	)
	f := newFlags("exec")
	o.register(f)
	f.String(&promptFlag, "<text>", "Prompt text", "p", "prompt")
	f.String(&file, "<path>", "Read the prompt from a file", "f", "file")
	f.String(&output, "<format>", "Output: text, json or stream-json", "o", "output")
	f.Bool(&autoEdit, "Allow file changes without asking", "auto-edit")
	f.Bool(&yolo, "Allow file changes, shell and web without asking (destructive commands stay blocked)", "yolo")
	f.Optional(&resume, &resumeID, "id", "Continue a session (latest, or the given id)", "resume")
	f.String(&fork, "<id>", "Start from a copy of a session", "fork")
	f.String(&title, "<text>", "Title for the new session", "title")
	f.Bool(&listTools, "List the tools the model would get, then exit", "list-tools")
	f.Bool(&quiet, "Text output: hide tool activity on stderr", "q", "quiet")
	help := false
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print(execHelp + f.Usage())
		return exitOK
	}
	switch output {
	case "text", "json", "stream-json":
	default:
		return usageFail(fmt.Errorf("--output must be text, json or stream-json"))
	}
	switch {
	case yolo:
		o.mode = config.ModeYolo
	case autoEdit:
		o.mode = config.ModeAutoEdit
	default:
		o.mode = config.ModeAsk
	}

	rt, err := newRuntime(o)
	if err != nil {
		return fail(err)
	}
	if listTools {
		for _, t := range rt.reg.List() {
			fmt.Printf("%-15s %-8s %s\n", t.Name(), t.Kind(), t.Description())
		}
		return exitOK
	}

	text, err := gatherPrompt(f.args, promptFlag, file)
	if err != nil {
		return usageFail(err)
	}

	switch {
	case fork != "":
		s, err := rt.store.Fork(fork)
		if err != nil {
			return fail(err)
		}
		rt.loadSession(s)
	case resume:
		s, err := rt.store.Load(resumeID)
		if err != nil {
			return fail(err)
		}
		rt.loadSession(s)
	}
	if title != "" {
		rt.sess.Title = title
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sink execSink
	switch output {
	case "text":
		sink = &textSink{quiet: quiet}
	case "stream-json":
		sink = &streamSink{enc: json.NewEncoder(os.Stdout)}
	case "json":
		sink = &jsonSink{}
	}
	if notice, err := rt.ensureModel(ctx); err != nil {
		return sink.fail(err)
	} else if notice != "" {
		sink.Notice(notice)
	}
	g := newGate(rt.mode, false, nil, nil)
	rt.agent.Gate = g
	rt.agent.Sink = sink
	rt.env.Ask = nil
	rt.env.OnPlan = nil

	start := time.Now()
	res, runErr := rt.agent.Run(ctx, text)
	saveErr := rt.saveSession()

	code := exitOK
	switch {
	case runErr == nil:
	case errors.Is(runErr, agent.ErrMaxTurns):
		code = exitMaxTurns
	case ctx.Err() != nil:
		code = exitCancelled
	default:
		code = exitError
		runErr = connectHint(runErr, rt.provider)
	}
	sink.finish(execResult{
		res: res, err: runErr, sessionID: rt.sess.ID, model: rt.agent.Model,
		elapsed: time.Since(start), denied: g.denied, mode: rt.mode,
	})
	if saveErr != nil {
		fmt.Fprintln(os.Stderr, "kalonide: could not save session:", saveErr)
	}
	return code
}

func gatherPrompt(args []string, flagText, file string) (string, error) {
	var parts []string
	if flagText != "" {
		parts = append(parts, flagText)
	}
	if len(args) > 0 {
		parts = append(parts, strings.Join(args, " "))
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(data))
	}
	if !ui.IsTerminal(os.Stdin) {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err == nil && strings.TrimSpace(string(data)) != "" {
			if len(parts) > 0 {
				parts = append(parts, "Input:\n"+string(data))
			} else {
				parts = append(parts, string(data))
			}
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		return "", errors.New("no prompt given (pass it as an argument, with -p, -f, or on stdin)")
	}
	return text, nil
}

type execResult struct {
	res       *agent.Result
	err       error
	sessionID string
	model     string
	elapsed   time.Duration
	denied    int
	mode      string
}

func (r execResult) stop() string {
	if r.res == nil {
		return string(agent.StopError)
	}
	return string(r.res.Stop)
}

type execSink interface {
	agent.Sink
	finish(execResult)
	fail(error) int
}

// textSink streams the answer to stdout and activity to stderr.
type textSink struct {
	quiet  bool
	mu     sync.Mutex
	inText bool
}

func (s *textSink) Text(t string) {
	t = ui.Sanitize(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inText = true
	fmt.Print(t)
}
func (s *textSink) Thinking(string) {}
func (s *textSink) ToolStart(_, name, summary string) {
	if s.quiet {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inText {
		fmt.Println()
		s.inText = false
	}
	fmt.Fprintf(os.Stderr, "%s %s %s\n", ui.Accent("●"), ui.Sanitize(name), ui.Dim(ui.Sanitize(summary)))
}
func (s *textSink) ToolEnd(_, name, result string, failed bool) {
	if s.quiet || !failed {
		return
	}
	fmt.Fprintf(os.Stderr, "  %s\n", ui.Red("⎿ "+ui.Sanitize(firstLine(result))))
}
func (s *textSink) Notice(n string) {
	fmt.Fprintf(os.Stderr, "%s %s\n", ui.Yellow("!"), ui.Sanitize(n))
}
func (s *textSink) finish(r execResult) {
	if s.inText {
		fmt.Println()
	}
	if r.err != nil {
		fmt.Fprintln(os.Stderr, ui.Red("kalonide: "+r.err.Error()))
	}
	if r.denied > 0 {
		fmt.Fprintf(os.Stderr, "%s %d action(s) were refused in %s mode; rerun with --auto-edit (file changes) or --yolo (also shell and web).\n", ui.Yellow("!"), r.denied, r.mode)
	}
}
func (s *textSink) fail(err error) int { return fail(err) }

// jsonSink prints one JSON object at the end.
type jsonSink struct {
	mu    sync.Mutex
	tools []map[string]any
	notes []string
}

func (s *jsonSink) Text(string)     {}
func (s *jsonSink) Thinking(string) {}
func (s *jsonSink) ToolStart(id, name, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, map[string]any{"id": id, "name": name, "summary": summary})
}
func (s *jsonSink) ToolEnd(id, _, _ string, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tools {
		if t["id"] == id {
			t["failed"] = failed
		}
	}
}
func (s *jsonSink) Notice(n string) {
	s.mu.Lock()
	s.notes = append(s.notes, n)
	s.mu.Unlock()
}
func (s *jsonSink) finish(r execResult) {
	out := map[string]any{
		"schema":     "kalonide.exec.v1",
		"session_id": r.sessionID,
		"model":      r.model,
		"stop":       r.stop(),
		"elapsed_ms": r.elapsed.Milliseconds(),
		"tools":      s.tools,
		"notices":    s.notes,
		"denied":     r.denied,
	}
	if s.tools == nil {
		out["tools"] = []any{}
	}
	if s.notes == nil {
		out["notices"] = []any{}
	}
	if r.res != nil {
		out["text"] = r.res.Text
		out["turns"] = r.res.Turns
		out["usage"] = usageJSON(r.res.Usage)
	}
	if r.err != nil {
		out["error"] = r.err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
func (s *jsonSink) fail(err error) int {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "kalonide.exec.v1", "stop": "error", "error": err.Error()})
	return exitError
}

// streamSink prints one JSON event per line as things happen.
type streamSink struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (s *streamSink) emit(typ string, fields map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := map[string]any{"schema": "kalonide.stream.v1", "type": typ, "time": time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range fields {
		ev[k] = v
	}
	_ = s.enc.Encode(ev)
}
func (s *streamSink) Text(t string)     { s.emit("text", map[string]any{"text": t}) }
func (s *streamSink) Thinking(t string) { s.emit("thinking", map[string]any{"text": t}) }
func (s *streamSink) ToolStart(id, name, summary string) {
	s.emit("tool_start", map[string]any{"id": id, "name": name, "summary": summary})
}
func (s *streamSink) ToolEnd(id, name, result string, failed bool) {
	s.emit("tool_end", map[string]any{"id": id, "name": name, "output": result, "failed": failed})
}
func (s *streamSink) Notice(n string) { s.emit("notice", map[string]any{"text": n}) }
func (s *streamSink) finish(r execResult) {
	f := map[string]any{"session_id": r.sessionID, "model": r.model, "stop": r.stop(), "elapsed_ms": r.elapsed.Milliseconds(), "denied": r.denied}
	if r.res != nil {
		f["text"] = r.res.Text
		f["turns"] = r.res.Turns
		f["usage"] = usageJSON(r.res.Usage)
	}
	if r.err != nil {
		f["error"] = r.err.Error()
	}
	s.emit("result", f)
}
func (s *streamSink) fail(err error) int {
	s.emit("result", map[string]any{"stop": "error", "error": err.Error()})
	return exitError
}

func usageJSON(u llm.Usage) map[string]int {
	return map[string]int{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens, "total_tokens": u.TotalTokens}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, ui.Red("kalonide: "+err.Error()))
	return exitError
}

func usageFail(err error) int {
	fmt.Fprintln(os.Stderr, "kalonide: "+err.Error())
	return exitUsage
}
