// Package cli implements the kalonide command line.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/discover"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/prompt"
	"github.com/KalonEdge/kalonide/internal/sandbox"
	"github.com/KalonEdge/kalonide/internal/ui"
	"github.com/KalonEdge/kalonide/internal/version"
)

const mainHelp = `kalonide — a coding agent for your terminal, powered by Kalon or any
OpenAI-compatible model server.

Usage:
  kalonide [flags]              start the interactive shell
  kalonide exec <prompt>        run one prompt (scripts, CI)
  kalonide resume [id]          continue a saved session

Commands:
  exec        Run one prompt without the shell
  resume      Continue a saved session in the shell
  models      List models the server offers; models use <id> sets the default
  providers   List, add, switch or remove provider profiles
  config      Show settings, or config set <key> <value>
  doctor      Check the setup and look for model servers on this machine
  mcp         Add, list and manage MCP servers (outside tools)
  hooks       List your hooks (commands run around tool calls)
  verify      Run the project's own build, lint and test commands
  repo-map    Outline the codebase: files and their top-level declarations
  trust       Let a folder's own notes, memories, skills and commands guide the agent
  changes     Show git changes; changes commit [-m msg | --auto] commits them
  serve       serve --mcp: offer Kalonide's tools to other apps over MCP
  acp         Agent Client Protocol for editors such as Zed (experimental)
  search      Search past sessions
  usage       Sessions, requests and tokens per day and model
  sessions    List, show or delete saved sessions
  init        Write a KALONIDE.md guide for the current project
  version     Print the version

Shell flags:
`

const envHelp = `
Environment:
  KALONIDE_HOME                 settings folder (default ~/.config/kalonide)
  KALONIDE_PROVIDER             provider profile to use
  KALONIDE_BASE_URL             override the provider's URL
  KALONIDE_MODEL                override the model
  KALONIDE_API_KEY              API key (otherwise the provider's apiKeyEnv)
  KALONIDE_PERMISSION_MODE      ask, auto-edit or yolo
  KALONIDE_MAX_TURNS            model calls per prompt
  KALONIDE_STREAM_IDLE_TIMEOUT  seconds of silence before a stream is retried
  KALONIDE_RECONNECT_WAIT       seconds to wait for a local server to come back (default 20, 0 = off)
  NO_COLOR                      disable colors
`

// Main runs the CLI and returns the exit code.
func Main(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "exec", "run":
			return cmdExec(args[1:])
		case "resume":
			return cmdShell(args[1:], true)
		case "models", "model":
			return cmdModels(args[1:])
		case "providers", "provider":
			return cmdProviders(args[1:])
		case "config":
			return cmdConfig(args[1:])
		case "doctor":
			return cmdDoctor(args[1:])
		case "mcp":
			return cmdMCP(args[1:])
		case "hooks":
			return cmdHooks(args[1:])
		case "verify":
			return cmdVerify(args[1:])
		case "repo-map":
			return cmdRepoMap(args[1:])
		case "changes":
			return cmdChanges(args[1:])
		case "serve":
			return cmdServe(args[1:])
		case "acp":
			return cmdACP(args[1:])
		case "search", "find":
			return cmdSearch(args[1:])
		case "usage":
			return cmdUsage(args[1:])
		case "trust":
			return cmdTrust(args[1:], true)
		case "untrust":
			return cmdTrust(args[1:], false)
		case "sessions", "session":
			return cmdSessions(args[1:])
		case "init":
			return cmdInit(args[1:])
		case "version", "--version", "-v":
			fmt.Println("kalonide " + version.Version)
			return exitOK
		case "help":
			return cmdShell([]string{"--help"}, false)
		}
	}
	return cmdShell(args, false)
}

func cmdShell(args []string, resumeCmd bool) int {
	var (
		o        runOptions
		resume   bool
		resumeID string
		autoEdit bool
		yolo     bool
		prompt   string
		help     bool
	)
	f := newFlags("")
	o.register(f)
	f.Optional(&resume, &resumeID, "id", "Continue a session (latest, or the given id)", "resume", "r")
	f.Bool(&autoEdit, "Start in auto-edit mode", "auto-edit")
	f.Bool(&yolo, "Start in yolo mode", "yolo")
	f.String(&prompt, "<text>", "Run one prompt and exit (same as exec)", "p", "prompt")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print(mainHelp + f.Usage() + envHelp)
		return exitOK
	}
	if prompt != "" {
		return cmdExec(append([]string{"-p", prompt}, args...))
	}
	if resumeCmd {
		resume = true
		if len(f.args) > 0 {
			resumeID = f.args[0]
		}
	} else if len(f.args) > 0 {
		return usageFail(fmt.Errorf("unknown command %q (see kalonide --help; to send a prompt use kalonide exec)", f.args[0]))
	}
	switch {
	case yolo:
		o.mode = config.ModeYolo
	case autoEdit:
		o.mode = config.ModeAutoEdit
	}
	return runShell(o, resumeID, resume)
}

// ---- models ----

func cmdModels(args []string) int {
	var provider string
	var asJSON, help bool
	f := newFlags("models")
	f.String(&provider, "<name>", "Provider to ask (default: active)", "provider")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Usage:\n  kalonide models [--provider name] [--json]\n  kalonide models use <id>\n\n" + f.Usage())
		return exitOK
	}
	s, err := config.Load()
	if err != nil {
		return fail(err)
	}
	p, err := s.Resolve(provider)
	if err != nil {
		return fail(err)
	}
	if len(f.args) >= 1 && f.args[0] == "use" {
		if len(f.args) < 2 {
			return usageFail(fmt.Errorf("usage: kalonide models use <id>"))
		}
		prov := s.Providers[p.Name]
		prov.Model = f.args[1]
		s.Providers[p.Name] = prov
		if err := config.Save(s); err != nil {
			return fail(err)
		}
		fmt.Printf("%s %s now uses %s\n", ui.Green("✔"), p.Name, f.args[1])
		return exitOK
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := newClient(p).ListModels(ctx)
	if err != nil {
		return fail(connectHint(err, p))
	}
	if asJSON {
		out := make([]map[string]string, len(models))
		for i, m := range models {
			out[i] = map[string]string{"id": m.ID, "detail": m.Detail}
		}
		return printJSON(map[string]any{"provider": p.Name, "current": p.Model, "models": out})
	}
	if len(models) == 0 {
		fmt.Println(p.Name + " offers no models.")
		return exitOK
	}
	for _, m := range models {
		mark := "  "
		if m.ID == p.Model {
			mark = ui.Accent("❯ ")
		}
		fmt.Printf("%s%s  %s\n", mark, m.ID, ui.Dim(m.Detail))
	}
	return exitOK
}

// ---- providers ----

const providersHelp = `Usage:
  kalonide providers                       list profiles
  kalonide providers add <name> --base-url <url> [--model m] [--api-key-env VAR] [--use]
  kalonide providers use <name>            make a profile active
  kalonide providers remove <name>

API keys are never stored. Name the environment variable that holds the key
with --api-key-env, e.g. --api-key-env OPENROUTER_API_KEY.

Flags:
`

func cmdProviders(args []string) int {
	var baseURL, model, keyEnv string
	var use, asJSON, help bool
	var headers []string
	f := newFlags("providers")
	f.String(&baseURL, "<url>", "Endpoint, e.g. http://localhost:11434/v1", "base-url")
	f.String(&model, "<model>", "Default model", "model", "m")
	f.String(&keyEnv, "<VAR>", "Environment variable holding the API key", "api-key-env")
	f.List(&headers, "<key=value>", "Extra request header (repeatable)", "header")
	f.Bool(&use, "Make it the active provider", "use")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print(providersHelp + f.Usage())
		return exitOK
	}
	s, err := config.Load()
	if err != nil {
		return fail(err)
	}
	sub := "list"
	if len(f.args) > 0 {
		sub = f.args[0]
	}
	name := ""
	if len(f.args) > 1 {
		name = f.args[1]
	}
	switch sub {
	case "list", "ls":
		if asJSON {
			return printJSON(map[string]any{"active": s.Provider, "providers": s.Providers})
		}
		for _, n := range s.ProviderNames() {
			p := s.Providers[n]
			mark := "  "
			if n == s.Provider {
				mark = ui.Accent("❯ ")
			}
			extra := ""
			if p.APIKeyEnv != "" {
				state := ui.Green("set")
				if os.Getenv(p.APIKeyEnv) == "" {
					state = ui.Red("not set")
				}
				extra = fmt.Sprintf(" · key $%s %s", p.APIKeyEnv, state)
			}
			m := p.Model
			if m == "" {
				m = "(first available)"
			}
			fmt.Printf("%s%s  %s  %s%s\n", mark, ui.Bold(n), p.BaseURL, ui.Dim(m), extra)
		}
		return exitOK
	case "add":
		if name == "" || baseURL == "" {
			return usageFail(fmt.Errorf("usage: kalonide providers add <name> --base-url <url>"))
		}
		if strings.ContainsAny(name, " /\\") {
			return usageFail(fmt.Errorf("provider names cannot contain spaces or slashes"))
		}
		p := config.Provider{BaseURL: config.NormalizeBaseURL(baseURL), Model: model, APIKeyEnv: keyEnv}
		for _, h := range headers {
			k, v, ok := strings.Cut(h, "=")
			if !ok {
				return usageFail(fmt.Errorf("--header takes key=value"))
			}
			if p.Headers == nil {
				p.Headers = map[string]string{}
			}
			p.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		s.Providers[name] = p
		if use {
			s.Provider = name
		}
		if err := config.Save(s); err != nil {
			return fail(err)
		}
		fmt.Printf("%s saved provider %s (%s)\n", ui.Green("✔"), name, p.BaseURL)
		return exitOK
	case "use":
		if _, ok := s.Providers[name]; !ok {
			return fail(fmt.Errorf("no provider named %q", name))
		}
		s.Provider = name
		if err := config.Save(s); err != nil {
			return fail(err)
		}
		fmt.Printf("%s active provider is %s\n", ui.Green("✔"), name)
		return exitOK
	case "remove", "rm":
		if _, ok := s.Providers[name]; !ok {
			return fail(fmt.Errorf("no provider named %q", name))
		}
		if len(s.Providers) == 1 {
			return fail(fmt.Errorf("cannot remove the only provider"))
		}
		delete(s.Providers, name)
		if s.Provider == name {
			s.Provider = s.ProviderNames()[0]
		}
		if err := config.Save(s); err != nil {
			return fail(err)
		}
		fmt.Printf("%s removed %s\n", ui.Green("✔"), name)
		return exitOK
	}
	return usageFail(fmt.Errorf("unknown providers command %q", sub))
}

// ---- config ----

func cmdConfig(args []string) int {
	var asJSON, help bool
	f := newFlags("config")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help || (len(f.args) > 0 && f.args[0] == "help") {
		fmt.Print(`Usage:
  kalonide config                  show settings
  kalonide config path             print the settings file path
  kalonide config set <key> <value>

Keys: provider, model, base-url, api-key-env (these three apply to the
active provider), permission-mode (ask|auto-edit|yolo), max-turns,
context-window, fallback-models (comma separated).
`)
		return exitOK
	}
	if len(f.args) == 0 {
		return printConfig(asJSON)
	}
	switch f.args[0] {
	case "path":
		fmt.Println(config.Path())
		return exitOK
	case "set":
		if len(f.args) < 3 {
			return usageFail(fmt.Errorf("usage: kalonide config set <key> <value>"))
		}
		if err := configSet(f.args[1], strings.Join(f.args[2:], " ")); err != nil {
			return fail(err)
		}
		fmt.Printf("%s %s = %s\n", ui.Green("✔"), f.args[1], strings.Join(f.args[2:], " "))
		return exitOK
	}
	return usageFail(fmt.Errorf("unknown config command %q", f.args[0]))
}

func configSet(key, val string) error {
	s, err := config.Load()
	if err != nil {
		return err
	}
	p := s.Providers[s.Provider]
	switch key {
	case "provider":
		if _, ok := s.Providers[val]; !ok {
			return fmt.Errorf("no provider named %q", val)
		}
		s.Provider = val
	case "model":
		p.Model = val
	case "base-url", "baseURL", "base_url":
		p.BaseURL = config.NormalizeBaseURL(val)
	case "api-key-env":
		p.APIKeyEnv = val
	case "permission-mode", "mode":
		switch val {
		case config.ModeAsk, config.ModeAutoEdit, config.ModeYolo:
			s.PermissionMode = val
		default:
			return fmt.Errorf("permission-mode must be ask, auto-edit or yolo")
		}
	case "max-turns":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("max-turns must be a positive number")
		}
		s.MaxTurns = n
	case "context-window":
		n, err := strconv.Atoi(val)
		if err != nil || n < 2048 {
			return fmt.Errorf("context-window must be a number of tokens, at least 2048")
		}
		s.ContextWindow = n
	case "fallback-models":
		s.FallbackModels = splitAll([]string{val})
	default:
		return fmt.Errorf("unknown key %q (see kalonide config help)", key)
	}
	if key == "model" || strings.HasPrefix(key, "base") || key == "api-key-env" {
		s.Providers[s.Provider] = p
	}
	return config.Save(s)
}

func printConfig(asJSON bool) int {
	s, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return printJSON(s)
	}
	fmt.Printf("%s %s\n", ui.Dim("settings"), config.Path())
	fmt.Printf("%s %s\n", ui.Dim("provider"), ui.Bold(s.Provider))
	p := s.Providers[s.Provider]
	fmt.Printf("%s %s\n", ui.Dim("endpoint"), p.BaseURL)
	m := p.Model
	if m == "" {
		m = "(first available)"
	}
	fmt.Printf("%s %s\n", ui.Dim("model   "), m)
	fmt.Printf("%s %s\n", ui.Dim("mode    "), s.PermissionMode)
	fmt.Printf("%s %d model calls per prompt · context window %d tokens\n", ui.Dim("limits  "), s.MaxTurns, s.ContextWindow)
	if len(s.FallbackModels) > 0 {
		fmt.Printf("%s %s\n", ui.Dim("fallback"), strings.Join(s.FallbackModels, ", "))
	}
	return exitOK
}

// ---- doctor ----

type doctorOptions struct {
	provider string
	asJSON   bool
}

func cmdDoctor(args []string) int {
	var o doctorOptions
	var help bool
	f := newFlags("doctor")
	f.String(&o.provider, "<name>", "Provider to check (default: active)", "provider")
	f.Bool(&o.asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Usage:\n  kalonide doctor [--provider name] [--json]\n\n" + f.Usage())
		return exitOK
	}
	return runDoctor(o)
}

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
}

func runDoctor(o doctorOptions) int {
	var checks []check
	add := func(c check) {
		checks = append(checks, c)
		if !o.asJSON {
			mark := ui.Green("✔")
			switch {
			case !c.OK:
				mark = ui.Red("✘")
			case c.Warn:
				mark = ui.Yellow("!")
			}
			fmt.Printf("%s %s %s\n", mark, ui.Bold(c.Name), ui.Dim(c.Detail))
		}
	}

	s, err := config.Load()
	if err != nil {
		add(check{Name: "settings", Detail: err.Error()})
		return finishDoctor(o, checks)
	}
	add(check{Name: "settings", OK: true, Detail: config.Path()})
	p, err := s.Resolve(o.provider)
	if err != nil {
		add(check{Name: "provider", Detail: err.Error()})
		return finishDoctor(o, checks)
	}
	add(check{Name: "provider", OK: true, Detail: p.Name + " · " + p.BaseURL})
	if p.APIKeyEnv != "" {
		if p.APIKey == "" {
			add(check{Name: "api key", Detail: "$" + p.APIKeyEnv + " is not set"})
		} else {
			add(check{Name: "api key", OK: true, Detail: "$" + p.APIKeyEnv + " is set"})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	start := time.Now()
	models, err := newClient(p).ListModels(ctx)
	cancel()
	reachable := err == nil
	if err != nil {
		add(check{Name: "connection", Detail: connectHint(err, p).Error()})
	} else {
		add(check{Name: "connection", OK: true, Detail: fmt.Sprintf("answered in %dms with %d model(s)", time.Since(start).Milliseconds(), len(models))})
	}
	if reachable {
		switch {
		case len(models) == 0:
			add(check{Name: "model", Detail: "the server offers no models; load one first"})
		case p.Model == "":
			add(check{Name: "model", OK: true, Warn: true, Detail: "none set; will use " + models[0].ID + " (set one with kalonide models use <id>)"})
		default:
			found := false
			for _, m := range models {
				if m.ID == p.Model {
					found = true
				}
			}
			if found {
				add(check{Name: "model", OK: true, Detail: p.Model})
			} else {
				add(check{Name: "model", OK: true, Warn: true, Detail: p.Model + " is not in the server's list; run kalonide models"})
			}
		}
	}

	// Look for model servers on this machine; useful when Kalon moved port.
	if discover.IsLocal(p.BaseURL) || !reachable {
		var lines []string
		for _, f := range discover.Scan(context.Background()) {
			if strings.TrimSuffix(f.URL, "/v1") == strings.TrimSuffix(p.BaseURL, "/v1") {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s (%d models)", f.URL, len(f.Models)))
		}
		if len(lines) > 0 {
			detail := "also found " + strings.Join(lines, ", ")
			if !reachable {
				detail += ". Point at one with: kalonide providers add local --base-url <url> --use"
			}
			add(check{Name: "local servers", OK: true, Warn: !reachable, Detail: detail})
		} else if !reachable {
			add(check{Name: "local servers", OK: true, Warn: true, Detail: "no model server found on ports " + strings.Trim(fmt.Sprint(discover.Ports), "[]")})
		}
	}

	if wd, err := os.Getwd(); err == nil {
		ctxFiles := []string{}
		for _, n := range []string{"KALONIDE.md", "AGENTS.md"} {
			if _, err := os.Stat(n); err == nil {
				ctxFiles = append(ctxFiles, n)
			}
		}
		mems := prompt.LoadMemories(prompt.ProjectMemoryDir(wd), config.UserMemoryDir())
		detail := fmt.Sprintf("%d saved memories", len(mems))
		if len(ctxFiles) > 0 {
			detail = strings.Join(ctxFiles, ", ") + " · " + detail
		} else {
			detail = "no KALONIDE.md (run kalonide init) · " + detail
		}
		add(check{Name: "project", OK: true, Warn: len(ctxFiles) == 0, Detail: detail})
	}
	if ok, tool := sandbox.Available(); ok {
		add(check{Name: "sandbox", OK: true, Detail: tool + " confines the agent's commands (setting: " + s.Sandbox + ")"})
	} else {
		add(check{Name: "sandbox", OK: true, Warn: true, Detail: "not available on this system; approvals are the safeguard"})
	}
	if discover.IsLocal(p.BaseURL) {
		add(check{Name: "privacy", OK: true, Detail: "model requests stay on this machine; web_fetch reaches the internet only when you allow it"})
	} else if u, err := url.Parse(p.BaseURL); err == nil {
		add(check{Name: "privacy", OK: true, Warn: true, Detail: "model requests (your prompts and the code the agent reads) go to " + u.Host})
	}
	return finishDoctor(o, checks)
}

func finishDoctor(o doctorOptions, checks []check) int {
	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	if o.asJSON {
		printJSON(map[string]any{"ok": ok, "checks": checks})
	}
	if ok {
		return exitOK
	}
	return exitError
}

// ---- sessions ----

func cmdSessions(args []string) int {
	var all, asJSON, help bool
	f := newFlags("sessions")
	f.Bool(&all, "Sessions from every folder, not just this one", "all", "a")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Usage:\n  kalonide sessions [--all] [--json]\n  kalonide sessions show <id>\n  kalonide sessions audit <id>   every tool call and what was decided\n  kalonide sessions rm <id>\n  kalonide resume [id]\n\n" + f.Usage())
		return exitOK
	}
	store := newStore()
	sub := "list"
	if len(f.args) > 0 {
		sub = f.args[0]
	}
	switch sub {
	case "list", "ls":
		ws := ""
		if !all {
			wd, _ := os.Getwd()
			if real, err := realDir(wd); err == nil {
				ws = real
			}
		}
		list, err := store.List(ws)
		if err != nil {
			return fail(err)
		}
		if asJSON {
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("No saved sessions" + map[bool]string{true: ".", false: " for this folder (try --all)."}[all])
			return exitOK
		}
		for _, s := range list {
			where := ""
			if all {
				where = " · " + s.Workspace
			}
			fmt.Printf("%s  %s  %s\n", ui.Dim(s.ID), s.Title, ui.Dim(fmt.Sprintf("%s · %d turns · %s%s", ago(s.Updated), s.Turns, s.Model, where)))
		}
		return exitOK
	case "show":
		if len(f.args) < 2 {
			return usageFail(fmt.Errorf("usage: kalonide sessions show <id>"))
		}
		s, err := store.Load(f.args[1])
		if err != nil {
			return fail(err)
		}
		if asJSON {
			return printJSON(s)
		}
		fmt.Printf("%s %s\n%s\n\n", ui.Bold(s.Title), ui.Dim(s.ID), ui.Dim(s.Workspace+" · "+s.Model))
		for _, m := range s.Messages {
			switch m.Role {
			case llm.RoleUser:
				fmt.Println(ui.Accent("❯ ") + ui.Sanitize(m.Content))
			case llm.RoleAssistant:
				if strings.TrimSpace(m.Content) != "" {
					fmt.Println(ui.Sanitize(m.Content))
				}
				for _, c := range m.ToolCalls {
					fmt.Println(ui.Dim(ui.Sanitize("● " + c.Function.Name + " " + clipText(c.Function.Arguments, 100))))
				}
			}
			fmt.Println()
		}
		return exitOK
	case "audit":
		if len(f.args) < 2 {
			return usageFail(fmt.Errorf("usage: kalonide sessions audit <id>"))
		}
		sess, err := store.Load(f.args[1])
		if err != nil {
			return fail(err)
		}
		data, err := os.ReadFile(store.AuditPath(sess.ID))
		if err != nil {
			fmt.Println("No tool calls recorded for " + sess.ID + ".")
			return exitOK
		}
		if asJSON {
			fmt.Print(string(data))
			return exitOK
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var e struct {
				Time     time.Time `json:"time"`
				Tool     string    `json:"tool"`
				Summary  string    `json:"summary"`
				Decision string    `json:"decision"`
				Failed   bool      `json:"failed"`
				Ms       int64     `json:"ms"`
			}
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			mark := ui.Green("✔")
			switch {
			case e.Decision != "ran":
				mark = ui.Yellow("⊘")
			case e.Failed:
				mark = ui.Red("✘")
			}
			fmt.Printf("%s %s %-14s %-20s %s\n", e.Time.Format("15:04:05"), mark, ui.Sanitize(e.Tool), ui.Dim(ui.Sanitize(e.Decision)), ui.Sanitize(clipText(e.Summary, 80)))
		}
		return exitOK
	case "rm", "delete":
		if len(f.args) < 2 {
			return usageFail(fmt.Errorf("usage: kalonide sessions rm <id>"))
		}
		if s, err := store.Load(f.args[1]); err == nil {
			os.Remove(store.AuditPath(s.ID))
		}
		if err := store.Delete(f.args[1]); err != nil {
			return fail(err)
		}
		fmt.Println(ui.Green("✔") + " deleted")
		return exitOK
	}
	return usageFail(fmt.Errorf("unknown sessions command %q", sub))
}

// ---- init ----

func cmdInit(args []string) int {
	full := append([]string{"--auto-edit", "--disabled-tools", "bash,web_fetch", "-p", prompt.InitPrompt}, args...)
	return cmdExec(full)
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(err)
	}
	return exitOK
}
