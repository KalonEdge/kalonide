package cli

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/mcp"
	"github.com/KalonEdge/kalonide/internal/ui"
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
				return usageFail(fmt.Errorf("give the command after --, e.g. kalonide mcp add files -- npx -y @modelcontextprotocol/server-filesystem ."))
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
