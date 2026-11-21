package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KalonEdge/kalonide/internal/agent"
	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/discover"
	"github.com/KalonEdge/kalonide/internal/extend"
	"github.com/KalonEdge/kalonide/internal/hooks"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/mcp"
	"github.com/KalonEdge/kalonide/internal/prompt"
	"github.com/KalonEdge/kalonide/internal/sandbox"
	"github.com/KalonEdge/kalonide/internal/session"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/verify"
)

// runOptions are the flags shared by the shell and exec.
type runOptions struct {
	cwd       string
	addDirs   []string
	provider  string
	model     string
	enable    []string
	disable   []string
	noTools   bool
	fallbacks []string
	maxTurns  int
	mode      string
	selfCheck bool
	trust     bool
}

func (o *runOptions) register(f *flagSet) {
	f.String(&o.cwd, "<dir>", "Workspace folder (default: current folder)", "C", "cwd")
	f.List(&o.addDirs, "<dir>", "Also allow writes in this folder (repeatable)", "add-dir")
	f.String(&o.provider, "<name>", "Provider profile to use", "provider")
	f.String(&o.model, "<model>", "Model to use", "m", "model")
	f.List(&o.enable, "<tools>", "Only offer these tools (comma separated)", "enabled-tools")
	f.List(&o.disable, "<tools>", "Hide these tools (comma separated)", "disabled-tools")
	f.Bool(&o.noTools, "Plain chat: offer no tools", "no-tools")
	f.List(&o.fallbacks, "<models>", "Fall back to these models when the model is overloaded", "fallback-models")
	f.Int(&o.maxTurns, "<n>", "Maximum model calls per prompt", "max-turns")
	f.Bool(&o.selfCheck, "Run the project's checks before finishing, and fix failures", "self-check")
	f.Bool(&o.trust, "Use this folder's notes, memories, skills and commands even if not trusted", "trust")
}

// runtime is everything a session needs, wired together.
type runtime struct {
	settings config.Settings
	provider config.Resolved
	client   *llm.Client
	env      *tools.Env
	reg      *tools.Registry
	agent    *agent.Agent
	store    *session.Store
	sess     *session.Session
	mode     string
	mcp      []*mcp.Client // connected MCP servers
	warnings []string      // problems to show the user once, e.g. an MCP server that failed
	hooks    *hooks.Runner // nil when no hooks are configured

	interactive bool     // a person can approve actions (the shell)
	trusted     bool     // the workspace's own notes, memories, skills and commands may be used
	extras      []string // what the workspace has of those (for the trust question)
	skills      []extend.Item
	modeNow     func() string // the current permission mode; the shell's can change
}

func newRuntime(o runOptions) (*runtime, error) {
	s, err := config.Load()
	if err != nil {
		return nil, err
	}
	prov, err := s.Resolve(o.provider)
	if err != nil {
		return nil, err
	}
	if o.model != "" {
		prov.Model = o.model
	}

	root := o.cwd
	if root == "" {
		root, _ = os.Getwd()
	}
	realRoot, err := tools.RealPath(root)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", root, err)
	}
	if st, err := os.Stat(realRoot); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("workspace %s is not a folder", root)
	}
	env := &tools.Env{
		Procs:            &tools.Procs{},
		Root:             realRoot,
		ProjectMemoryDir: prompt.ProjectMemoryDir(realRoot),
		UserMemoryDir:    config.UserMemoryDir(),
	}
	for _, d := range o.addDirs {
		for _, part := range tools.SplitList(d) {
			if err := addRoot(env, part); err != nil {
				return nil, err
			}
		}
	}

	reg := tools.Default()
	reg.Add(tools.Verify{Root: realRoot}) // replaces the default, naming real commands
	task := &agent.TaskTool{}
	reg.Add(task)
	extras := prompt.ProjectExtras(realRoot)
	// Trust is explicit only: saved, --trust, or the user's yes this session.
	// A folder with nothing to trust is not trusted either, so files that
	// appear later (even ones the agent wrote) need approval first.
	trusted := s.Trusts(realRoot) || o.trust || os.Getenv("KALONIDE_TRUST") == "1"
	var mcpClients []*mcp.Client
	var mcpWarnings []string
	if !o.noTools {
		mcpClients, mcpWarnings = connectMCP(s.MCPServers, reg)
	}
	if err := reg.Filter(splitAll(o.enable), splitAll(o.disable)); err != nil {
		closeMCP(mcpClients)
		return nil, err
	}
	if o.noTools {
		reg = tools.NewRegistry()
	}

	rt := &runtime{
		settings: s,
		provider: prov,
		env:      env,
		reg:      reg,
		store:    &session.Store{Dir: config.SessionsDir()},
		mode:     s.PermissionMode,
		mcp:      mcpClients,
		warnings: mcpWarnings,
		trusted:  trusted,
		extras:   extras,
	}
	rt.loadSkills()
	if v := os.Getenv("KALONIDE_PERMISSION_MODE"); v != "" {
		rt.mode = v
	}
	if o.mode != "" {
		rt.mode = o.mode
	}
	rt.client = newClient(prov)

	maxTurns := s.MaxTurns
	if n, ok := config.EnvInt("KALONIDE_MAX_TURNS"); ok {
		maxTurns = n
	}
	if o.maxTurns > 0 {
		maxTurns = o.maxTurns
	}
	fallbacks := append([]string(nil), s.FallbackModels...)
	if len(o.fallbacks) > 0 {
		fallbacks = splitAll(o.fallbacks)
	}
	rt.agent = &agent.Agent{
		Client:        rt.client,
		Model:         prov.Model,
		Fallbacks:     fallbacks,
		Tools:         reg,
		Env:           env,
		MaxTurns:      maxTurns,
		ContextWindow: s.ContextWindow,
	}
	task.Parent = rt.agent
	rt.modeNow = func() string { return rt.mode }
	rt.agent.Audit = func(e agent.AuditEntry) {
		if data, err := json.Marshal(e); err == nil {
			_ = rt.store.AppendAudit(rt.sess.ID, data)
		}
	}
	env.Sandbox = rt.sandboxPolicy
	if ok, _ := sandbox.Available(); !ok && s.Sandbox == "on" {
		rt.warnings = append(rt.warnings, "sandbox is on in settings, but this system has none (macOS sandbox-exec, or bubblewrap on Linux); commands run unconfined")
	}
	if s.SelfCheck || o.selfCheck {
		rt.setSelfCheck(true)
	}
	var valid []hooks.Hook
	for i, h := range s.Hooks {
		if problem := h.Valid(); problem != "" {
			rt.warnings = append(rt.warnings, fmt.Sprintf("hook %d in settings is ignored: %s", i+1, problem))
			continue
		}
		valid = append(valid, h)
	}
	if len(valid) > 0 {
		rt.hooks = &hooks.Runner{Hooks: valid, Root: env.Root, Resolve: env.Resolve}
		rt.agent.Hooks = rt.hooks
	}
	rt.agent.SystemPrompt = func(model string, toolsOn bool) string {
		return prompt.Build(prompt.Options{
			Root: env.Root, ExtraRoots: env.ExtraRoots, Model: model,
			ToolsEnabled: toolsOn, UserMemoryDir: env.UserMemoryDir, Skills: rt.skills,
			IncludeProject: rt.trusted,
		})
	}
	rt.hookRetry()
	rt.sess = session.New(env.Root, prov.Name, prov.Model)
	return rt, nil
}

func newClient(p config.Resolved) *llm.Client {
	c := llm.New(p.BaseURL, p.APIKey, p.Headers)
	if d, ok := config.EnvInt("KALONIDE_STREAM_IDLE_TIMEOUT"); ok {
		c.IdleTimeout = time.Duration(d) * time.Second
	}
	return c
}

func addRoot(env *tools.Env, dir string) error {
	real, err := tools.RealPath(dir)
	if err != nil {
		return fmt.Errorf("--add-dir %s: %w", dir, err)
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return fmt.Errorf("--add-dir %s: not a folder", dir)
	}
	for _, r := range env.ExtraRoots {
		if r == real {
			return nil
		}
	}
	env.ExtraRoots = append(env.ExtraRoots, real)
	return nil
}

func splitAll(list []string) []string {
	var out []string
	for _, l := range list {
		out = append(out, tools.SplitList(l)...)
	}
	return out
}

// ensureModel picks a model when none is configured: the first one the
// endpoint serves. It returns a notice for the user, or an error when the
// endpoint has nothing to offer.
func (rt *runtime) ensureModel(ctx context.Context) (string, error) {
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	models, err := rt.client.ListModels(lctx)
	cancel()
	if rt.agent.Model != "" {
		// A server without a model list is fine when the model is known.
		if err == nil {
			return rt.applyCaps(models), nil
		}
		return "", nil
	}
	if err != nil {
		return "", connectHint(err, rt.provider)
	}
	if len(models) == 0 {
		return "", fmt.Errorf("%s serves no models; load one in Kalon (or your model server) first", rt.provider.BaseURL)
	}
	best := llm.BestModel(models)
	rt.setModel(best)
	notice := fmt.Sprintf("no model configured; using %s (change it with /model, --model or kalonide models use <id>)", best)
	if n := rt.applyCaps(models); n != "" {
		notice += "; " + n
	}
	return notice, nil
}

// applyCaps turns tools off up front for a model the server says cannot
// call them, and returns a notice when it does.
func (rt *runtime) applyCaps(models []llm.ModelInfo) string {
	for _, m := range models {
		if m.ID == rt.agent.Model && !m.Supports("tools") && rt.agent.ToolsOn() {
			rt.agent.DisableTools(m.ID)
			return m.ID + " cannot call tools, so it can chat but not read or change files"
		}
	}
	return ""
}

func (rt *runtime) setModel(m string) {
	rt.agent.Model = m
	rt.provider.Model = m
	rt.sess.Model = m
}

// saveModel stores the model as the provider's default.
func (rt *runtime) saveModel(m string) error {
	s, err := config.Load()
	if err != nil {
		return err
	}
	p, ok := s.Providers[rt.provider.Name]
	if !ok {
		return nil
	}
	p.Model = m
	s.Providers[rt.provider.Name] = p
	return config.Save(s)
}

// switchProvider points the runtime at another provider profile.
func (rt *runtime) switchProvider(name string) error {
	s, err := config.Load()
	if err != nil {
		return err
	}
	prov, err := s.Resolve(name)
	if err != nil {
		return err
	}
	rt.settings = s
	rt.provider = prov
	rt.client = newClient(prov)
	rt.agent.Client = rt.client
	rt.hookRetry()
	rt.agent.Model = prov.Model
	rt.sess.Provider = prov.Name
	rt.sess.Model = prov.Model
	s.Provider = name
	return config.Save(s)
}

func (rt *runtime) saveSession() error {
	if len(rt.agent.Messages) == 0 {
		return nil
	}
	rt.sess.Messages = rt.agent.Messages
	rt.sess.Usage = rt.agent.Usage
	rt.sess.Model = rt.agent.Model
	return rt.store.Save(rt.sess)
}

// loadSession continues a saved session in this runtime.
func (rt *runtime) loadSession(s *session.Session) {
	rt.sess = s
	rt.agent.Messages = append([]llm.Message(nil), s.Messages...)
	rt.agent.Usage = s.Usage
	if s.Model != "" && rt.agent.Model == "" {
		rt.setModel(s.Model)
	}
}

// connectHint turns a client error into advice.
func connectHint(err error, p config.Resolved) error {
	switch llm.KindOf(err) {
	case llm.KindUnreachable:
		msg := fmt.Sprintf("cannot reach %s at %s", p.Name, p.BaseURL)
		if p.Name == config.DefaultProvider {
			msg += ". Is the Kalon app running with its local API on? Run `kalonide doctor` to look for model servers on this machine"
		}
		return errors.New(msg)
	case llm.KindAuth:
		if p.APIKey == "" && p.APIKeyEnv != "" {
			return fmt.Errorf("%s rejected the request: set %s to your API key", p.Name, p.APIKeyEnv)
		}
		return fmt.Errorf("%s rejected the API key: %v", p.Name, err)
	case llm.KindNotFound:
		return fmt.Errorf("%v. The model %q may not exist on %s; run `kalonide models` to see what it serves", err, p.Model, p.Name)
	}
	if strings.Contains(err.Error(), "model") && p.Model != "" && llm.KindOf(err) == llm.KindBadRequest {
		return fmt.Errorf("%v (model %q on %s; run `kalonide models` to check)", err, p.Model, p.Name)
	}
	return err
}

func newStore() *session.Store { return &session.Store{Dir: config.SessionsDir()} }

func realDir(p string) (string, error) { return tools.RealPath(p) }

// hookRetry reports each retry to whoever is watching, so a slow or
// restarting server never looks like a hang.
func (rt *runtime) hookRetry() {
	notice := func(s string) {
		if rt.agent != nil && rt.agent.Sink != nil {
			rt.agent.Sink.Notice(s)
		}
	}
	rt.client.OnRetry = func(attempt int, wait time.Duration, err error) {
		notice(fmt.Sprintf("%s: %v; retrying in %s (attempt %d)", rt.provider.Name, err, wait.Round(100*time.Millisecond), attempt+1))
	}
	// A local server that stops answering is usually restarting (or moved
	// port): wait for it instead of failing the request.
	wait := reconnectWait()
	if wait <= 0 {
		rt.client.Reconnect = nil
		return
	}
	rt.client.Reconnect = func(ctx context.Context) string {
		base := rt.client.Base()
		if !discover.IsLocal(base) {
			return ""
		}
		notice(fmt.Sprintf("%s is not answering at %s; waiting up to %s for it to come back", rt.provider.Name, base, wait))
		model := ""
		if rt.agent != nil {
			model = rt.agent.Model
		}
		found := discover.Reconnect(ctx, base, model, wait)
		switch {
		case found == "":
			return ""
		case found == base:
			notice(rt.provider.Name + " is back; continuing")
		default:
			notice(fmt.Sprintf("found %s at %s; continuing there for this session", model, found))
			rt.provider.BaseURL = found
		}
		return found
	}
}

// reconnectWait is how long to wait for a local model server to return:
// 20 seconds, or KALONIDE_RECONNECT_WAIT seconds (0 turns waiting off).
func reconnectWait() time.Duration {
	if v := os.Getenv("KALONIDE_RECONNECT_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 20 * time.Second
}

// warmUp loads the model on a local server in the background with a
// one-token request, so the first real reply does not wait for a cold
// start. It is silent and best-effort.
func (rt *runtime) warmUp() {
	base, model := rt.client.Base(), rt.agent.Model
	if model == "" || !discover.IsLocal(base) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := llm.New(base, rt.provider.APIKey, rt.provider.Headers)
	c.MaxAttempts = 1
	_, _ = c.Chat(ctx, llm.Request{Model: model, MaxTokens: 1,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}, nil)
}

// connectMCP connects to the enabled MCP servers at once and registers
// their tools. A server that fails is reported, never fatal.
func connectMCP(servers map[string]mcp.ServerConfig, reg *tools.Registry) ([]*mcp.Client, []string) {
	type result struct {
		name   string
		client *mcp.Client
		tools  []mcp.Tool
		err    error
	}
	var names []string
	for name, cfg := range servers {
		if !cfg.Disabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			r := result{name: name}
			r.client, r.err = mcp.Connect(ctx, name, servers[name])
			if r.err == nil {
				if r.tools, r.err = r.client.Tools(ctx); r.err != nil {
					r.client.Close()
					r.client = nil
				}
			}
			results[i] = r
		}(i, name)
	}
	wg.Wait()
	var clients []*mcp.Client
	var warnings []string
	for _, r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("MCP server %s is unavailable: %v", r.name, r.err))
			continue
		}
		clients = append(clients, r.client)
		cfg := servers[r.name]
		for _, t := range r.tools {
			taken := func(n string) bool { _, ok := reg.Get(n); return ok }
			reg.Add(tools.MCPTool{
				Client: r.client, Server: r.name, Tool: t,
				ToolName: tools.MCPToolName(r.name, t.Name, taken),
				Trusted:  cfg.Approved(t),
			})
		}
	}
	return clients, warnings
}

func closeMCP(clients []*mcp.Client) {
	for _, c := range clients {
		c.Close()
	}
}

// close releases what the runtime started: background commands and MCP
// servers.
func (rt *runtime) close() {
	rt.env.Procs.StopAll()
	closeMCP(rt.mcp)
}

// projectDir is the workspace's .kalonide folder, or "" when the folder
// is not trusted.
func (rt *runtime) projectDir() string {
	if !rt.trusted {
		return ""
	}
	return filepath.Join(rt.env.Root, ".kalonide")
}

// commands returns the user's and (if trusted) the project's commands.
func (rt *runtime) commands() []extend.Item {
	return extend.Commands(rt.projectDir(), config.Home())
}

// loadSkills (re)registers the skill tool for the current trust.
func (rt *runtime) loadSkills() {
	rt.skills = extend.Skills(rt.projectDir(), config.Home())
	if len(rt.skills) > 0 {
		rt.reg.Add(tools.Skill{Skills: rt.skills})
	} else {
		rt.reg.Remove("skill")
	}
}

// setTrusted changes whether the workspace's own files are used, and
// optionally remembers the choice.
func (rt *runtime) setTrusted(trusted, remember bool) error {
	rt.trusted = trusted
	rt.loadSkills()
	if !remember {
		return nil
	}
	s, err := config.Load()
	if err != nil {
		return err
	}
	s.SetTrust(rt.env.Root, trusted)
	return config.Save(s)
}

// setSelfCheck turns the check-before-finishing loop on or off.
func (rt *runtime) setSelfCheck(on bool) {
	if !on {
		rt.agent.SelfCheck = nil
		return
	}
	rt.agent.SelfCheck = selfChecker{env: rt.env}
}

// selfChecker runs the project's checks for the agent's self-check, in
// the sandbox when one applies.
type selfChecker struct{ env *tools.Env }

func (c selfChecker) Summary() string { return tools.CheckSummary(c.env.Root) }

func (c selfChecker) Run(ctx context.Context) (bool, string) {
	checks := verify.Detect(c.env.Root)
	if len(checks) == 0 {
		return true, ""
	}
	results := verify.Run(ctx, c.env.Root, checks, 10*time.Minute, false, c.env.SandboxWrapper())
	return verify.AllPassed(results), verify.Summary(results, len(checks)-len(results))
}

// sandboxPolicy decides, per command, whether the agent's shell commands
// run confined: always with "on", never with "off", and with "auto" when
// nobody approves each command (auto-edit, yolo, or a headless run).
func (rt *runtime) sandboxPolicy() *sandbox.Policy {
	if ok, _ := sandbox.Available(); !ok {
		return nil
	}
	switch rt.settings.Sandbox {
	case "off":
		return nil
	case "auto", "":
		if rt.interactive && rt.modeNow() == config.ModeAsk {
			return nil
		}
	}
	writable := append([]string{rt.env.Root}, rt.env.ExtraRoots...)
	writable = append(writable, rt.settings.SandboxWritable...)
	return &sandbox.Policy{Writable: writable, Network: rt.settings.SandboxNetwork}
}
