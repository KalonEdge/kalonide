package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/mcp"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/ui"
	"github.com/KalonEdge/kalonide/internal/verify"
)

const mcpHelp = `Manage MCP servers: outside tools the agent can use.

Usage:
  kalonide mcp                                   list servers and check them
  kalonide mcp add <name> [--env K=V] [--auto-approve tool] -- <command> [args...]
  kalonide mcp add <name> --url <https://…> [--header K=V] [--trust-read-only]
  kalonide mcp remove <name>
  kalonide mcp enable <name>
  kalonide mcp disable <name>

Servers are stored in settings.json under "mcpServers", in the same shape
other MCP clients use. Their tools appear as mcp__<server>__<tool>. Every
MCP tool asks first unless you are in yolo mode, or you approved it:
--auto-approve <tool> for a named tool, --trust-read-only for tools the
server marks read-only. Tools the server marks destructive always ask.

Flags:
`

func cmdMCP(args []string) int {
	var url string
	var env, headers, approve []string
	var help, trustRO bool
	f := newFlags("mcp")
	f.String(&url, "<url>", "Remote server URL (streamable HTTP)", "url")
	f.List(&env, "<K=V>", "Environment variable for a local server (repeatable)", "env")
	f.List(&headers, "<K=V>", "HTTP header for a remote server (repeatable)", "header")
	f.List(&approve, "<tool>", "Let this tool run without asking (repeatable)", "auto-approve")
	f.Bool(&trustRO, "Let tools the server marks read-only run without asking", "trust-read-only")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print(mcpHelp + f.Usage())
		return exitOK
	}
	s, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if s.MCPServers == nil {
		s.MCPServers = map[string]mcp.ServerConfig{}
	}
	sub, rest := "list", []string(nil)
	if len(f.args) > 0 {
		sub, rest = f.args[0], f.args[1:]
	}
	name := ""
	if len(rest) > 0 {
		name = rest[0]
	}
	save := func(msg string) int {
		if err := config.Save(s); err != nil {
			return fail(err)
		}
		fmt.Println(ui.Green("✔ ") + msg)
		return exitOK
	}
	switch sub {
	case "list", "ls":
		return listMCP(s.MCPServers)
	case "add":
		if name == "" {
			return usageFail(fmt.Errorf("usage: kalonide mcp add <name> -- <command> [args]  or  --url <url>"))
		}
		if !reServerName.MatchString(name) {
			return usageFail(fmt.Errorf("server names use lowercase letters, digits and single dashes, e.g. github or docs-search"))
		}
		cfg := mcp.ServerConfig{URL: url, AutoApprove: splitAll(approve), TrustReadOnlyHints: trustRO}
		if url == "" {
			if len(rest) < 2 {
				return usageFail(errors.New("give the command after --, e.g. kalonide mcp add files -- npx -y @modelcontextprotocol/server-filesystem ~/notes"))
			}
			cfg.Command, cfg.Args = rest[1], rest[2:]
		}
		var err error
		if cfg.Env, err = pairs(env); err != nil {
			return usageFail(err)
		}
		if cfg.Headers, err = pairs(headers); err != nil {
			return usageFail(err)
		}
		s.MCPServers[name] = cfg
		return save("added MCP server " + name + "; check it with: kalonide mcp")
	case "remove", "rm", "enable", "disable":
		cfg, ok := s.MCPServers[name]
		if !ok {
			return fail(fmt.Errorf("no MCP server named %q", name))
		}
		switch sub {
		case "enable", "disable":
			cfg.Disabled = sub == "disable"
			s.MCPServers[name] = cfg
			return save(name + " " + sub + "d")
		}
		delete(s.MCPServers, name)
		return save("removed " + name)
	}
	return usageFail(fmt.Errorf("unknown mcp command %q (see kalonide mcp --help)", sub))
}

// listMCP connects to each enabled server and reports what it offers.
func listMCP(servers map[string]mcp.ServerConfig) int {
	if len(servers) == 0 {
		fmt.Println("No MCP servers yet. Add one with: kalonide mcp add <name> -- <command> [args]")
		return exitOK
	}
	ok := true
	for _, name := range sortedKeys(servers) {
		cfg := servers[name]
		where := cfg.URL
		if where == "" {
			where = strings.TrimSpace(cfg.Command + " " + strings.Join(cfg.Args, " "))
		}
		if cfg.Disabled {
			fmt.Printf("%s %s %s\n", ui.Dim("○"), ui.Bold(name), ui.Dim("disabled · "+where))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c, err := mcp.Connect(ctx, name, cfg)
		var toolNames []string
		if err == nil {
			var ts []mcp.Tool
			if ts, err = c.Tools(ctx); err == nil {
				for _, t := range ts {
					toolNames = append(toolNames, t.Name)
				}
			}
			c.Close()
		}
		cancel()
		if err != nil {
			ok = false
			fmt.Printf("%s %s %s\n  %s\n", ui.Red("✘"), ui.Bold(name), ui.Dim(where), ui.Red(err.Error()))
			continue
		}
		fmt.Printf("%s %s %s\n  %s\n", ui.Green("✔"), ui.Bold(name), ui.Dim(where), ui.Dim(fmt.Sprintf("%d tools: %s", len(toolNames), strings.Join(toolNames, ", "))))
	}
	if !ok {
		return exitError
	}
	return exitOK
}

var reServerName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func pairs(list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range list {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%q should be KEY=value", kv)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const hooksHelp = `Hooks run your own commands around the agent's work. Add them to
settings.json (kalonide config path) under "hooks"; they are never read
from a project folder.

  "hooks": [
    {"event": "after_tool", "tools": ["edit_file", "write_file"], "command": "gofmt -w \"$KALONIDE_FILE\""},
    {"event": "before_tool", "tools": ["bash"], "command": "./scripts/allowed.sh"},
    {"event": "turn_end", "command": "osascript -e 'display notification \"done\" with title \"Kalonide\"'"}
  ]

before_tool  runs before a tool; a non-zero exit blocks the call and its
             output tells the model why
after_tool   runs after a tool; its output is added to the tool's result
turn_end     runs when a request is done; its output is shown to you

Hooks run in the workspace with KALONIDE_EVENT, KALONIDE_TOOL,
KALONIDE_ARGS (JSON), KALONIDE_FILE (when the tool has a path),
KALONIDE_WORKSPACE and, for turn_end, KALONIDE_STOP. Default timeout 60s
("timeout": seconds).
`

func cmdHooks(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Print(hooksHelp)
		return exitOK
	}
	printHooks()
	return exitOK
}

func printHooks() {
	s, err := config.Load()
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return
	}
	if len(s.Hooks) == 0 {
		fmt.Println("No hooks. See: kalonide hooks --help")
		return
	}
	for i, h := range s.Hooks {
		mark := ui.Green("●")
		switch {
		case h.Valid() != "":
			mark = ui.Red("✘")
		case h.Disabled:
			mark = ui.Dim("○")
		}
		scope := "all tools"
		if len(h.Tools) > 0 {
			scope = strings.Join(h.Tools, ", ")
		}
		if h.Event == "turn_end" {
			scope = "every request"
		}
		fmt.Printf("%s %d. %s %s  %s\n", mark, i+1, ui.Bold(h.Event), ui.Dim("("+scope+")"), h.Command)
		if p := h.Valid(); p != "" {
			fmt.Println("   " + ui.Red(p))
		}
	}
}

func cmdVerify(args []string) int {
	var cwd string
	var all, asJSON, help bool
	f := newFlags("verify")
	f.String(&cwd, "<dir>", "Project folder (default: current)", "C", "cwd")
	f.Bool(&all, "Run every check even after a failure", "all")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Run the project's own build, lint and test commands.\n\nUsage:\n  kalonide verify [--all] [--json] [-C dir]\n\n" + f.Usage())
		return exitOK
	}
	root := cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	checks := verify.Detect(root)
	if len(checks) == 0 {
		fmt.Println("No checks found (go.mod, package.json, Cargo.toml, pyproject.toml, Package.swift or a Makefile test target).")
		return exitError
	}
	results := verify.Run(context.Background(), root, checks, 10*time.Minute, all, nil)
	if asJSON {
		out := []map[string]any{}
		for _, r := range results {
			out = append(out, map[string]any{"id": r.Check.ID, "command": r.Check.Command, "passed": r.Passed, "seconds": r.Seconds, "output": r.Output})
		}
		printJSON(map[string]any{"passed": verify.AllPassed(results), "checks": out})
	} else {
		for _, r := range results {
			mark := ui.Green("✔")
			if !r.Passed {
				mark = ui.Red("✘")
			}
			fmt.Printf("%s %s %s\n", mark, r.Check.Label, ui.Dim(fmt.Sprintf("%.1fs", r.Seconds)))
			if !r.Passed && r.Output != "" {
				fmt.Println(ui.Dim("    " + strings.ReplaceAll(r.Output, "\n", "\n    ")))
			}
		}
	}
	if verify.AllPassed(results) {
		return exitOK
	}
	return exitError
}

func cmdRepoMap(args []string) int {
	var cwd, query string
	var help bool
	f := newFlags("repo-map")
	f.String(&cwd, "<dir>", "Project folder (default: current)", "C", "cwd")
	f.String(&query, "<text>", "Only files or names containing this text", "query", "q")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Outline the codebase: each source file with its top-level declarations.\n\nUsage:\n  kalonide repo-map [--query text] [-C dir]\n\n" + f.Usage())
		return exitOK
	}
	root := cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	out, err := tools.BuildRepoMap(context.Background(), root, query, 1<<20)
	if err != nil {
		return fail(err)
	}
	fmt.Println(out)
	return exitOK
}

func cmdTrust(args []string, trusted bool) int {
	var help, list bool
	f := newFlags("trust")
	f.Bool(&list, "List trusted folders", "list")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Trusted folders may steer the agent with their own KALONIDE.md, AGENTS.md,\nmemories, skills and commands.\n\nUsage:\n  kalonide trust [dir]     trust a folder (default: current)\n  kalonide untrust [dir]\n  kalonide trust --list\n")
		return exitOK
	}
	s, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if list {
		if len(s.TrustedFolders) == 0 {
			fmt.Println("No trusted folders.")
		}
		for _, d := range s.TrustedFolders {
			fmt.Println(d)
		}
		return exitOK
	}
	dir := "."
	if len(f.args) > 0 {
		dir = f.args[0]
	}
	real, err := tools.RealPath(dir)
	if err != nil {
		return fail(err)
	}
	s.SetTrust(real, trusted)
	if err := config.Save(s); err != nil {
		return fail(err)
	}
	if trusted {
		fmt.Println(ui.Green("✔ ") + "trusted " + real)
	} else {
		fmt.Println(ui.Green("✔ ") + "no longer trusted: " + real)
	}
	return exitOK
}
