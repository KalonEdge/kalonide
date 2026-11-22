package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/prompt"
	"github.com/KalonEdge/kalonide/internal/sandbox"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/version"
)

const serveHelp = `Serve Kalonide's tools to other apps over MCP (stdio).

Usage:
  kalonide serve --mcp [-C dir] [--allow-unsafe-tools]

Add it to another app's MCP settings as a command, e.g.:  {"command": "kalonide", "args": ["serve", "--mcp", "-C", "/path/to/project"]}

By default only read-only tools are offered (read_file, list_directory,
glob, grep, repo_map). --allow-unsafe-tools adds file edits and shell
commands; those run in the sandbox when one is available, and commands
that look destructive are always refused.

Flags:
`

func cmdServe(args []string) int {
	var cwd string
	var mcpMode, unsafe, help bool
	f := newFlags("serve")
	f.Bool(&mcpMode, "Serve MCP over stdin and stdout", "mcp")
	f.String(&cwd, "<dir>", "Project folder (default: current)", "C", "cwd")
	f.Bool(&unsafe, "Also offer file edits and shell commands", "allow-unsafe-tools")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help || !mcpMode {
		fmt.Print(serveHelp + f.Usage())
		if help {
			return exitOK
		}
		return exitUsage
	}
	root := cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	real, err := tools.RealPath(root)
	if err != nil {
		return fail(err)
	}
	srv := newMCPServer(real, unsafe)
	defer srv.env.Procs.StopAll()
	if err := srv.serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kalonide serve:", err)
		return exitError
	}
	return exitOK
}

type mcpServer struct {
	env    *tools.Env
	reg    *tools.Registry
	unsafe bool
	out    *json.Encoder
	outMu  sync.Mutex
}

func newMCPServer(root string, unsafe bool) *mcpServer {
	env := &tools.Env{
		Root:             root,
		Procs:            &tools.Procs{},
		ProjectMemoryDir: prompt.ProjectMemoryDir(root),
		UserMemoryDir:    config.UserMemoryDir(),
	}
	if ok, _ := sandbox.Available(); ok {
		env.Sandbox = func() *sandbox.Policy { return &sandbox.Policy{Writable: []string{root}} }
	}
	reg := tools.NewRegistry(tools.ReadFile{}, tools.ListDirectory{}, tools.Glob{}, tools.Grep{}, tools.RepoMap{})
	if unsafe {
		reg.Add(tools.WriteFile{})
		reg.Add(tools.EditFile{})
		reg.Add(tools.ApplyPatch{})
		reg.Add(tools.Bash{})
		reg.Add(tools.Verify{Root: root})
	}
	return &mcpServer{env: env, reg: reg, unsafe: unsafe}
}

type rpcIn struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *mcpServer) send(v any) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = s.out.Encode(v)
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *mcpServer) replyError(id json.RawMessage, code int, msg string) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

// serve reads newline-delimited JSON-RPC from in until it closes.
func (s *mcpServer) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	var wg sync.WaitGroup
	defer wg.Wait()
	for sc.Scan() {
		var m rpcIn
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			s.replyError(json.RawMessage("null"), -32700, "parse error")
			continue
		}
		if len(m.ID) == 0 || string(m.ID) == "null" {
			continue // notifications need no answer
		}
		switch m.Method {
		case "initialize":
			s.reply(m.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "kalonide", "version": version.Version},
				"instructions":    "Tools for working in the project at " + s.env.Root + ". Paths are relative to it.",
			})
		case "ping":
			s.reply(m.ID, map[string]any{})
		case "tools/list":
			s.reply(m.ID, map[string]any{"tools": s.toolList()})
		case "tools/call":
			wg.Add(1)
			go func(m rpcIn) { // calls may run long; keep reading
				defer wg.Done()
				s.reply(m.ID, s.call(ctx, m.Params))
			}(m)
		default:
			s.replyError(m.ID, -32601, "method not found: "+m.Method)
		}
	}
	return sc.Err()
}

func (s *mcpServer) toolList() []map[string]any {
	var out []map[string]any
	for _, t := range s.reg.List() {
		ro := t.Kind() == tools.KindRead
		out = append(out, map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"inputSchema": t.Schema(),
			"annotations": map[string]any{"readOnlyHint": ro, "destructiveHint": !ro},
		})
	}
	return out
}

func (s *mcpServer) call(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	result := func(text string, isErr bool) map[string]any {
		return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isErr}
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return result("invalid parameters: "+err.Error(), true)
	}
	t, ok := s.reg.Get(p.Name)
	if !ok {
		return result("no tool named "+p.Name, true)
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if d, ok := t.(tools.Dangerous); ok {
		if why := d.Danger(args); why != "" {
			return result("Refused: this command looks destructive ("+why+"). Run it yourself if you mean it.", true)
		}
	}
	out, err := t.Run(ctx, s.env, args)
	if err != nil {
		return result(tools.Redact(err.Error()), true)
	}
	return result(tools.Truncate(tools.Redact(out), tools.MaxOutput), false)
}
