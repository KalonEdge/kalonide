package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/KalonEdge/kalonide/internal/agent"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/version"
)

const acpHelp = `Serve the Agent Client Protocol over stdio, for editors such as Zed.

Usage:
  kalonide acp

Point your editor's external-agent setting at "kalonide acp". The editor
hosts the conversation; Kalonide uses your provider and model (Kalon by
default). Actions that change files or run commands ask through the
editor first. Experimental: not yet verified with every editor.
`

func cmdACP(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Print(acpHelp)
		return exitOK
	}
	a := newACPServer(os.Stdout)
	if err := a.serve(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "kalonide acp:", err)
		return exitError
	}
	return exitOK
}

// acpServer speaks JSON-RPC 2.0 over newline-delimited stdio, both ways:
// it answers the editor's requests and sends its own (permission).
type acpServer struct {
	outMu sync.Mutex
	out   *json.Encoder

	nextID  atomic.Int64
	waitMu  sync.Mutex
	waiting map[int64]chan json.RawMessage

	sessMu   sync.Mutex
	sessions map[string]*acpSession
	work     sync.WaitGroup
}

type acpSession struct {
	id       string
	rt       *runtime
	cancel   context.CancelFunc // of the running prompt, if any
	mu       sync.Mutex
	lastTool atomic.Value // id of the tool call being made (tools run one at a time)
}

func newACPServer(out io.Writer) *acpServer {
	return &acpServer{out: json.NewEncoder(out), waiting: map[int64]chan json.RawMessage{}, sessions: map[string]*acpSession{}}
}

type acpMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (a *acpServer) send(v any) {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	_ = a.out.Encode(v)
}

func (a *acpServer) reply(id json.RawMessage, result any) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (a *acpServer) replyErr(id json.RawMessage, code int, msg string) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (a *acpServer) notify(method string, params any) {
	a.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// request sends a request to the editor and waits for its answer.
func (a *acpServer) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := a.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	a.waitMu.Lock()
	a.waiting[id] = ch
	a.waitMu.Unlock()
	defer func() {
		a.waitMu.Lock()
		delete(a.waiting, id)
		a.waitMu.Unlock()
	}()
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *acpServer) serve(in io.Reader) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	defer a.shutdown()
	for sc.Scan() {
		var m acpMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Method == "" { // a response to one of our requests
			var id int64
			if json.Unmarshal(m.ID, &id) == nil {
				a.waitMu.Lock()
				ch := a.waiting[id]
				a.waitMu.Unlock()
				if ch != nil {
					if len(m.Error) > 0 {
						ch <- nil
					} else {
						ch <- m.Result
					}
				}
			}
			continue
		}
		a.handle(m)
	}
	return sc.Err()
}

func (a *acpServer) shutdown() {
	a.sessMu.Lock()
	for _, s := range a.sessions {
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
	}
	a.sessMu.Unlock()
	a.work.Wait()
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	for _, s := range a.sessions {
		s.rt.close()
	}
}

func (a *acpServer) handle(m acpMsg) {
	isRequest := len(m.ID) > 0 && string(m.ID) != "null"
	switch m.Method {
	case "initialize":
		a.reply(m.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession":        false,
				"promptCapabilities": map[string]any{"image": true, "audio": false, "embeddedContext": true},
			},
			"authMethods": []any{},
			"agentInfo":   map[string]string{"name": "kalonide", "version": version.Version},
		})
	case "authenticate":
		a.reply(m.ID, map[string]any{})
	case "session/new":
		a.newSession(m)
	case "session/prompt":
		a.work.Add(1)
		go func() {
			defer a.work.Done()
			a.prompt(m)
		}()
	case "session/cancel":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if s := a.session(p.SessionID); s != nil {
			s.mu.Lock()
			if s.cancel != nil {
				s.cancel()
			}
			s.mu.Unlock()
		}
	default:
		if isRequest {
			a.replyErr(m.ID, -32601, "method not found: "+m.Method)
		}
	}
}

func (a *acpServer) session(id string) *acpSession {
	a.sessMu.Lock()
	defer a.sessMu.Unlock()
	return a.sessions[id]
}

func (a *acpServer) newSession(m acpMsg) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if p.Cwd == "" {
		a.replyErr(m.ID, -32602, "cwd is required")
		return
	}
	rt, err := newRuntime(runOptions{cwd: p.Cwd})
	if err != nil {
		a.replyErr(m.ID, -32603, err.Error())
		return
	}
	s := &acpSession{id: rt.sess.ID, rt: rt}
	rt.interactive = true // the editor approves actions
	rt.agent.Gate = &acpGate{srv: a, sess: s}
	rt.agent.Sink = &acpSink{srv: a, sess: s}
	rt.env.Ask = nil // no free-text questions through ACP; the model proceeds on its best assumption
	rt.env.OnPlan = func(items []tools.PlanItem) {
		entries := make([]map[string]string, len(items))
		for i, it := range items {
			entries[i] = map[string]string{"content": it.Content, "status": it.Status, "priority": "medium"}
		}
		a.notify("session/update", map[string]any{"sessionId": s.id, "update": map[string]any{"sessionUpdate": "plan", "entries": entries}})
	}
	a.sessMu.Lock()
	a.sessions[s.id] = s
	a.sessMu.Unlock()
	a.reply(m.ID, map[string]any{"sessionId": s.id})
}

func (a *acpServer) prompt(m acpMsg) {
	var p struct {
		SessionID string            `json:"sessionId"`
		Prompt    []json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		a.replyErr(m.ID, -32602, "invalid params")
		return
	}
	s := a.session(p.SessionID)
	if s == nil {
		a.replyErr(m.ID, -32602, "no session "+p.SessionID)
		return
	}
	msg := llm.Message{}
	for _, raw := range p.Prompt {
		var block struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "image":
			if block.Data != "" && strings.HasPrefix(block.MimeType, "image/") {
				msg.Images = append(msg.Images, "data:"+block.MimeType+";base64,"+block.Data)
			}
		case "resource":
			msg.Content += fmt.Sprintf("\n\n[%s]\n%s", block.Resource.URI, block.Resource.Text)
		case "resource_link":
			var link struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(raw, &link)
			msg.Content += "\n(see " + link.URI + ")"
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		cancel()
		a.replyErr(m.ID, -32603, "a prompt is already running in this session")
		return
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()

	if s.rt.agent.Model == "" {
		if _, err := s.rt.ensureModel(ctx); err != nil {
			a.replyErr(m.ID, -32603, err.Error())
			return
		}
	}
	res, err := s.rt.agent.RunMessage(ctx, msg)
	_ = s.rt.saveSession()
	stop := "end_turn"
	switch {
	case ctx.Err() != nil:
		stop = "cancelled"
	case errors.Is(err, agent.ErrMaxTurns):
		stop = "max_turn_requests"
	case err != nil:
		a.replyErr(m.ID, -32603, connectHint(err, s.rt.provider).Error())
		return
	case res != nil && res.Stop == agent.StopDone:
	}
	a.reply(m.ID, map[string]any{"stopReason": stop})
}

// acpKind maps a tool to the editor's tool kinds.
func acpKind(name string, k tools.Kind) string {
	switch {
	case name == "grep" || name == "glob" || name == "repo_map":
		return "search"
	case name == "web_fetch":
		return "fetch"
	case k == tools.KindRead:
		return "read"
	case k == tools.KindWrite:
		return "edit"
	case k == tools.KindShell:
		return "execute"
	case name == "update_plan":
		return "think"
	}
	return "other"
}

// acpSink streams the agent's work to the editor as session updates.
type acpSink struct {
	srv  *acpServer
	sess *acpSession
}

func (s *acpSink) update(u map[string]any) {
	s.srv.notify("session/update", map[string]any{"sessionId": s.sess.id, "update": u})
}

func (s *acpSink) Text(t string) {
	s.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": t}})
}

func (s *acpSink) Thinking(t string) {
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": t}})
}

func (s *acpSink) ToolStart(id, name, summary string) {
	s.sess.lastTool.Store(id)
	title := name
	if summary != "" {
		title = name + ": " + summary
	}
	s.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": title, "kind": acpKind(name, kindOf(name)), "status": "in_progress"})
}

func (s *acpSink) ToolEnd(id, name, result string, failed bool) {
	status := "completed"
	if failed {
		status = "failed"
	}
	s.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status,
		"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": tools.Truncate(result, 8000)}}}})
}

func (s *acpSink) Notice(n string) {
	s.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "[" + n + "]\n"}})
}

// kindOf finds a default tool's kind by name, for display.
func kindOf(name string) tools.Kind {
	if t, ok := tools.Default().Get(name); ok {
		return t.Kind()
	}
	return tools.KindExternal
}

// acpGate asks the editor for permission, like the shell's prompt: reads
// run freely; destructive calls can only be allowed once.
type acpGate struct {
	srv    *acpServer
	sess   *acpSession
	mu     sync.Mutex
	always map[string]bool
}

func (g *acpGate) Allow(ctx context.Context, t tools.Tool, summary, danger string) (bool, error) {
	k := t.Kind()
	if k == tools.KindRead || k == tools.KindInteract || k == tools.KindMemory {
		return true, nil
	}
	g.mu.Lock()
	if g.always == nil {
		g.always = map[string]bool{}
	}
	always := g.always[t.Name()] && danger == ""
	g.mu.Unlock()
	if always {
		return true, nil
	}
	options := []map[string]string{
		{"optionId": "allow_once", "name": "Allow", "kind": "allow_once"},
		{"optionId": "allow_always", "name": "Always allow " + t.Name(), "kind": "allow_always"},
		{"optionId": "reject_once", "name": "Deny", "kind": "reject_once"},
	}
	title := t.Name() + ": " + summary
	if danger != "" {
		options = []map[string]string{options[0], options[2]}
		title = "Destructive (" + danger + "): " + summary
	}
	callID, _ := g.sess.lastTool.Load().(string)
	res, err := g.srv.request(ctx, "session/request_permission", map[string]any{
		"sessionId": g.sess.id,
		"toolCall":  map[string]any{"toolCallId": callID, "title": title, "kind": acpKind(t.Name(), k), "status": "pending"},
		"options":   options,
	})
	if err != nil || res == nil {
		return false, nil
	}
	var out struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(res, &out) != nil || out.Outcome.Outcome != "selected" {
		return false, nil
	}
	switch out.Outcome.OptionID {
	case "allow_once":
		return true, nil
	case "allow_always":
		if danger != "" {
			return true, nil
		}
		g.mu.Lock()
		g.always[t.Name()] = true
		g.mu.Unlock()
		return true, nil
	}
	return false, nil
}
