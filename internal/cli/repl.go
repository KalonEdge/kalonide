package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KalonEdge/kalonide/internal/agent"
	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/prompt"
	"github.com/KalonEdge/kalonide/internal/session"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/ui"
	"github.com/KalonEdge/kalonide/internal/version"
)

// shell is the interactive session.
type shell struct {
	rt        *runtime
	term      *ui.Terminal
	editor    *ui.Editor
	render    *ui.Renderer
	gate      *gate
	models    []string // cached for completion
	modelInfo []llm.ModelInfo
	modelsMu  sync.Mutex
}

type slashCmd struct {
	name string
	args string
	help string
	run  func(sh *shell, arg string) (prompt string, quit bool)
}

var slashCmds []slashCmd

func init() {
	slashCmds = []slashCmd{
		{"help", "", "Show commands and keys", (*shell).cmdHelp},
		{"model", "[name]", "Pick or set the model (live list from the server)", (*shell).cmdModel},
		{"models", "", "List the models the server offers", (*shell).cmdModels},
		{"provider", "[name]", "Pick or switch the provider", (*shell).cmdProvider},
		{"mode", "[ask|auto-edit|yolo]", "Show or set the permission mode (Shift+Tab cycles)", (*shell).cmdMode},
		{"new", "", "Start a fresh conversation", (*shell).cmdNew},
		{"clear", "", "Same as /new, and clear the screen", (*shell).cmdClear},
		{"resume", "[id]", "Continue a saved session", (*shell).cmdResume},
		{"sessions", "", "List saved sessions for this folder", (*shell).cmdSessions},
		{"compact", "", "Summarize the conversation to free context", (*shell).cmdCompact},
		{"context", "", "Show context usage", (*shell).cmdContext},
		{"tools", "", "List tools and whether they ask first", (*shell).cmdTools},
		{"add-dir", "<path>", "Allow writes in another folder", (*shell).cmdAddDir},
		{"memory", "", "List saved memories", (*shell).cmdMemory},
		{"init", "", "Write a KALONIDE.md guide for this project", (*shell).cmdInit},
		{"thinking", "", "Show or hide model thinking", (*shell).cmdThinking},
		{"doctor", "", "Check the setup", (*shell).cmdDoctor},
		{"config", "", "Show settings", (*shell).cmdConfig},
		{"exit", "", "Leave (also /quit, Ctrl-D)", (*shell).cmdExit},
		{"quit", "", "", (*shell).cmdExit},
	}
}

func runShell(o runOptions, resumeID string, resume bool) int {
	term := ui.NewTerminal()
	if term == nil || !ui.IsTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "kalonide: the interactive shell needs a terminal; use `kalonide exec` for scripts")
		return exitUsage
	}
	rt, err := newRuntime(o)
	if err != nil {
		return fail(err)
	}
	sh := &shell{rt: rt, term: term, render: ui.NewRenderer()}
	sh.gate = newGate(rt.mode, true, term, sh.render)
	sh.editor = ui.NewEditor(term, config.HistoryPath())
	sh.editor.Complete = sh.complete
	sh.editor.ShiftTab = func() { sh.gate.Cycle() }

	rt.agent.Gate = sh.gate
	rt.agent.Sink = sh.render
	rt.env.OnPlan = sh.render.Plan
	rt.env.Ask = sh.ask

	if resume {
		s, err := rt.store.Load(resumeID)
		if err != nil {
			return fail(err)
		}
		rt.loadSession(s)
	}

	sh.banner()
	if rt.sess.Title != "" && len(rt.agent.Messages) > 0 {
		fmt.Printf("%s %s %s\n\n", ui.Dim("resumed"), ui.Bold(rt.sess.Title), ui.Dim(fmt.Sprintf("(%d messages)", len(rt.agent.Messages))))
	}
	ctx := context.Background()
	if notice, err := rt.ensureModel(ctx); err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		fmt.Println(ui.Dim("  You can still use /provider, /model and /doctor.") + "\n")
	} else if notice != "" {
		fmt.Println(ui.Yellow("! ") + ui.Dim(notice) + "\n")
	}
	go sh.refreshModels()

	lastInterrupt := time.Time{}
	for {
		line, err := sh.editor.ReadLine(sh.prompt)
		switch {
		case errors.Is(err, ui.ErrInterrupt):
			if time.Since(lastInterrupt) < 2*time.Second {
				return exitOK
			}
			lastInterrupt = time.Now()
			fmt.Println(ui.Dim("(press Ctrl-C again to exit, or type /exit)"))
			continue
		case errors.Is(err, io.EOF):
			return exitOK
		case err != nil:
			return fail(err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//") {
			p, quit := sh.slash(line)
			if quit {
				return exitOK
			}
			if p == "" {
				continue
			}
			line = p
		} else if strings.HasPrefix(line, "!") {
			sh.shellEscape(strings.TrimSpace(line[1:]))
			continue
		}
		sh.turn(line)
	}
}

func (sh *shell) banner() {
	rt := sh.rt
	model := rt.agent.Model
	if model == "" {
		model = "no model yet"
	}
	home, _ := os.UserHomeDir()
	ws := rt.env.Root
	if home != "" && strings.HasPrefix(ws, home) {
		ws = "~" + ws[len(home):]
	}
	fmt.Println()
	if hero := ui.Hero(ui.Width()); hero != nil {
		for _, line := range hero {
			fmt.Println("  " + line)
		}
		fmt.Println()
	}
	fmt.Printf("  %s %s\n\n", ui.Bold("Your code, your models, your machine."), ui.Dim(version.Version))
	fmt.Printf("  %s  %s  %s %s %s\n\n", ui.Dim(ws), ui.Dim("·"), ui.Bold(model), ui.Dim("on"), ui.Accent(rt.provider.Name))
	fmt.Printf("  %s %s %s %s %s %s\n", ui.Dim("Try"), ui.Accent("\"explain this project\""), ui.Dim("·"), ui.Accent("\"find and fix the failing test\""), ui.Dim("·"), ui.Accent("/model"))
	fmt.Printf("  %s\n\n", ui.Dim("/help commands · Shift+Tab mode · Ctrl-C stops a reply · \\ then Enter for a new line"))
}

func (sh *shell) prompt() string {
	switch sh.gate.Mode() {
	case config.ModeAutoEdit:
		return ui.Yellow("auto-edit") + " " + ui.Accent("❯ ")
	case config.ModeYolo:
		return ui.Red("yolo") + " " + ui.Accent("❯ ")
	}
	return ui.Accent("❯ ")
}

// turn sends one prompt to the agent. Ctrl-C cancels the reply.
func (sh *shell) turn(input string) {
	rt := sh.rt
	if rt.agent.Model == "" {
		if _, err := rt.ensureModel(context.Background()); err != nil {
			fmt.Println(ui.Red("! ") + err.Error())
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()

	_ = sh.term.Quiet()
	start := time.Now()
	res, err := rt.agent.Run(ctx, input)
	_ = sh.term.Restore()
	sh.render.End()

	switch {
	case err == nil:
	case ctx.Err() != nil:
		fmt.Println(ui.Dim("(stopped)"))
	case errors.Is(err, agent.ErrMaxTurns):
		fmt.Println(ui.Yellow("! ") + fmt.Sprintf("stopped after %d model calls (the turn limit). Say \"continue\" to keep going.", res.Turns))
	default:
		fmt.Println(ui.Red("! ") + connectHint(err, rt.provider).Error())
	}
	if res != nil {
		sh.footer(res, time.Since(start))
	}
	if err := rt.saveSession(); err != nil {
		fmt.Println(ui.Red("! could not save session: ") + err.Error())
	}
	fmt.Println()
}

func (sh *shell) footer(res *agent.Result, el time.Duration) {
	parts := []string{fmt.Sprintf("%.1fs", el.Seconds())}
	if res.Usage.TotalTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s tokens", compactNum(res.Usage.TotalTokens)))
	}
	used := sh.rt.agent.ContextTokens() * 100 / max(sh.rt.agent.ContextWindow, 1)
	if used >= 50 {
		parts = append(parts, fmt.Sprintf("context %d%%", used))
	}
	if sh.rt.agent.Model != sh.rt.provider.Model && sh.rt.agent.Model != "" {
		parts = append(parts, "now using "+sh.rt.agent.Model)
	}
	fmt.Println(ui.Dim("  " + strings.Join(parts, " · ")))
}

func compactNum(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// ask answers the ask_user tool.
func (sh *shell) ask(ctx context.Context, question string, options []string) (string, error) {
	sh.render.End()
	defer sh.term.Quiet()
	fmt.Println(ui.Accent("? ") + ui.Bold(ui.Sanitize(question)))
	if len(options) > 0 {
		items := make([]ui.PickItem, 0, len(options)+1)
		for _, o := range options {
			items = append(items, ui.PickItem{Label: ui.Sanitize(o)})
		}
		items = append(items, ui.PickItem{Label: "Type my own answer…"})
		i, err := ui.Pick(sh.term, "Choose an answer", items, -1)
		if err != nil {
			return "", err
		}
		if i >= 0 && i < len(options) {
			fmt.Println(ui.Dim("  → " + options[i]))
			return options[i], nil
		}
		if i < 0 {
			return "", nil
		}
	}
	ed := ui.NewEditor(sh.term, "")
	ans, err := ed.ReadLine(func() string { return ui.Accent("  answer › ") })
	if errors.Is(err, ui.ErrInterrupt) || errors.Is(err, io.EOF) {
		return "", nil
	}
	return ans, err
}

// shellEscape runs a command the user typed after "!". The user typed it,
// so it runs without a permission prompt.
func (sh *shell) shellEscape(cmd string) {
	if cmd == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	out, err := tools.RunCommand(ctx, sh.rt.env.Root, cmd, 10*time.Minute)
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return
	}
	fmt.Println(out)
	fmt.Println()
}

// slash runs a /command. It may return a prompt to send to the agent.
func (sh *shell) slash(line string) (string, bool) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	arg = strings.TrimSpace(arg)
	name = strings.ToLower(name)
	for _, c := range slashCmds {
		if c.name == name {
			return c.run(sh, arg)
		}
	}
	// Unique prefix match: /mo → /model is ambiguous with /models, /mode.
	var matches []slashCmd
	for _, c := range slashCmds {
		if strings.HasPrefix(c.name, name) && c.help != "" {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0].run(sh, arg)
	}
	fmt.Printf("%s unknown command /%s. Type /help for the list.\n", ui.Yellow("!"), name)
	return "", false
}

func (sh *shell) complete(prefix string) []string {
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	if rest, ok := strings.CutPrefix(prefix, "/model "); ok {
		sh.modelsMu.Lock()
		defer sh.modelsMu.Unlock()
		var out []string
		for _, m := range sh.models {
			if strings.HasPrefix(m, rest) {
				out = append(out, "/model "+m)
			}
		}
		return out
	}
	if rest, ok := strings.CutPrefix(prefix, "/mode "); ok {
		var out []string
		for _, m := range []string{config.ModeAsk, config.ModeAutoEdit, config.ModeYolo} {
			if strings.HasPrefix(m, rest) {
				out = append(out, "/mode "+m)
			}
		}
		return out
	}
	if strings.Contains(prefix, " ") {
		return nil
	}
	var out []string
	for _, c := range slashCmds {
		if strings.HasPrefix("/"+c.name, prefix) && c.help != "" {
			s := "/" + c.name
			if c.args != "" {
				s += " "
			}
			out = append(out, s)
		}
	}
	return out
}

func (sh *shell) refreshModels() []llm.ModelInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := sh.rt.client.ListModels(ctx)
	if err != nil {
		return nil
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	sh.modelsMu.Lock()
	sh.models = ids
	sh.modelInfo = models
	sh.modelsMu.Unlock()
	return models
}

// ---- commands ----

func (sh *shell) cmdHelp(string) (string, bool) {
	fmt.Println(ui.Bold("Commands"))
	for _, c := range slashCmds {
		if c.help == "" {
			continue
		}
		left := "/" + c.name
		if c.args != "" {
			left += " " + c.args
		}
		fmt.Printf("  %s  %s\n", ui.Accent(fmt.Sprintf("%-30s", left)), ui.Dim(c.help))
	}
	fmt.Println()
	fmt.Println(ui.Bold("Keys"))
	keys := [][2]string{
		{"Enter", "send"},
		{"\\ then Enter, or Alt+Enter", "new line"},
		{"↑ ↓", "history"},
		{"Tab", "complete commands"},
		{"Shift+Tab", "cycle mode: ask → auto-edit → yolo"},
		{"Ctrl-C", "stop the reply; twice on an empty line to exit"},
		{"!<command>", "run a shell command yourself"},
	}
	for _, k := range keys {
		fmt.Printf("  %s  %s\n", ui.Accent(fmt.Sprintf("%-30s", k[0])), ui.Dim(k[1]))
	}
	fmt.Println()
	return "", false
}

func (sh *shell) cmdModel(arg string) (string, bool) {
	rt := sh.rt
	if arg == "" {
		fmt.Println(ui.Dim("Asking " + rt.provider.Name + " which models it has…"))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models, err := rt.client.ListModels(ctx)
		cancel()
		fmt.Print("\x1b[1A\r\x1b[K")
		if err != nil {
			fmt.Println(ui.Red("! ") + connectHint(err, rt.provider).Error())
			return "", false
		}
		if len(models) == 0 {
			fmt.Println(ui.Yellow("! ") + rt.provider.Name + " offers no models.")
			return "", false
		}
		items := make([]ui.PickItem, len(models))
		cur := -1
		for i, m := range models {
			items[i] = ui.PickItem{Label: m.ID, Detail: m.Detail}
			if m.ID == rt.agent.Model {
				cur = i
			}
		}
		i, err := ui.Pick(sh.term, "Model on "+rt.provider.Name, items, cur)
		sh.modelsMu.Lock()
		sh.modelInfo = models
		sh.modelsMu.Unlock()
		if err != nil || i < 0 {
			return "", false
		}
		arg = models[i].ID
	}
	rt.setModel(arg)
	if n := rt.applyCaps(sh.cachedInfo()); n != "" {
		fmt.Println(ui.Yellow("! ") + ui.Dim(n))
	}
	if err := rt.saveModel(arg); err != nil {
		fmt.Println(ui.Red("! could not save: ") + err.Error())
	}
	fmt.Printf("%s model is now %s\n\n", ui.Green("✔"), ui.Bold(arg))
	return "", false
}

func (sh *shell) cmdModels(string) (string, bool) {
	models := sh.refreshModels()
	if models == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := sh.rt.client.ListModels(ctx); err != nil {
			fmt.Println(ui.Red("! ") + connectHint(err, sh.rt.provider).Error())
			return "", false
		}
	}
	for _, m := range models {
		mark := "  "
		if m.ID == sh.rt.agent.Model {
			mark = ui.Accent("❯ ")
		}
		fmt.Printf("%s%s  %s\n", mark, m.ID, ui.Dim(m.Detail))
	}
	fmt.Println()
	return "", false
}

func (sh *shell) cmdProvider(arg string) (string, bool) {
	s, err := config.Load()
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return "", false
	}
	if arg == "" {
		names := s.ProviderNames()
		items := make([]ui.PickItem, len(names))
		cur := -1
		for i, n := range names {
			p := s.Providers[n]
			detail := p.BaseURL
			if p.Model != "" {
				detail += " · " + p.Model
			}
			items[i] = ui.PickItem{Label: n, Detail: detail}
			if n == sh.rt.provider.Name {
				cur = i
			}
		}
		i, err := ui.Pick(sh.term, "Provider", items, cur)
		if err != nil || i < 0 {
			return "", false
		}
		arg = names[i]
	}
	if err := sh.rt.switchProvider(arg); err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return "", false
	}
	fmt.Printf("%s provider is now %s\n", ui.Green("✔"), ui.Bold(arg))
	if notice, err := sh.rt.ensureModel(context.Background()); err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
	} else if notice != "" {
		fmt.Println(ui.Yellow("! ") + ui.Dim(notice))
	} else {
		fmt.Printf("  model %s\n", ui.Bold(sh.rt.agent.Model))
	}
	go sh.refreshModels()
	fmt.Println()
	return "", false
}

func (sh *shell) cmdMode(arg string) (string, bool) {
	switch arg {
	case "":
	case config.ModeAsk, config.ModeAutoEdit, config.ModeYolo:
		sh.gate.SetMode(arg)
	default:
		fmt.Println(ui.Yellow("! ") + "modes are: ask, auto-edit, yolo")
		return "", false
	}
	desc := map[string]string{
		config.ModeAsk:      "asks before file changes, shell commands and web access",
		config.ModeAutoEdit: "changes files without asking; asks before shell and web",
		config.ModeYolo:     "runs everything without asking, except destructive commands",
	}
	m := sh.gate.Mode()
	fmt.Printf("mode %s: %s\n\n", ui.Bold(m), ui.Dim(desc[m]))
	return "", false
}

func (sh *shell) cmdNew(string) (string, bool) {
	if err := sh.rt.saveSession(); err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
	}
	sh.rt.agent.Reset()
	sh.rt.env.Plan = nil
	sh.rt.sess = session.New(sh.rt.env.Root, sh.rt.provider.Name, sh.rt.agent.Model)
	fmt.Println(ui.Dim("New conversation.") + "\n")
	return "", false
}

func (sh *shell) cmdClear(arg string) (string, bool) {
	fmt.Print("\x1b[H\x1b[2J\x1b[3J")
	sh.cmdNew(arg)
	sh.banner()
	return "", false
}

func (sh *shell) cmdResume(arg string) (string, bool) {
	rt := sh.rt
	if arg == "" {
		list, err := rt.store.List(rt.env.Root)
		if err != nil || len(list) == 0 {
			fmt.Println(ui.Dim("No saved sessions for this folder yet.") + "\n")
			return "", false
		}
		items := make([]ui.PickItem, 0, len(list))
		for _, s := range list {
			if s.ID == rt.sess.ID {
				continue
			}
			items = append(items, ui.PickItem{Label: ui.Sanitize(s.Title), Detail: fmt.Sprintf("%s · %d turns · %s", ago(s.Updated), s.Turns, s.ID)})
		}
		if len(items) == 0 {
			fmt.Println(ui.Dim("No other sessions for this folder.") + "\n")
			return "", false
		}
		i, err := ui.Pick(sh.term, "Resume", items, -1)
		if err != nil || i < 0 {
			return "", false
		}
		d := items[i].Detail
		arg = d[strings.LastIndex(d, " ")+1:]
	}
	s, err := rt.store.Load(arg)
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return "", false
	}
	_ = rt.saveSession()
	rt.loadSession(s)
	fmt.Printf("%s resumed %s %s\n\n", ui.Green("✔"), ui.Bold(s.Title), ui.Dim(fmt.Sprintf("(%d messages)", len(s.Messages))))
	return "", false
}

func (sh *shell) cmdSessions(string) (string, bool) {
	list, err := sh.rt.store.List(sh.rt.env.Root)
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return "", false
	}
	if len(list) == 0 {
		fmt.Println(ui.Dim("No saved sessions for this folder yet.") + "\n")
		return "", false
	}
	for i, s := range list {
		if i == 20 {
			fmt.Println(ui.Dim(fmt.Sprintf("  …and %d more (kalonide sessions)", len(list)-20)))
			break
		}
		fmt.Printf("  %s  %s  %s\n", ui.Dim(s.ID), s.Title, ui.Dim(fmt.Sprintf("%s · %d turns", ago(s.Updated), s.Turns)))
	}
	fmt.Println(ui.Dim("\n  /resume <id> to continue one") + "\n")
	return "", false
}

func (sh *shell) cmdCompact(string) (string, bool) {
	if len(sh.rt.agent.Messages) < 4 {
		fmt.Println(ui.Dim("Not enough conversation to compact yet.") + "\n")
		return "", false
	}
	sh.render.Waiting("summarizing")
	err := sh.rt.agent.Compact(context.Background())
	sh.render.End()
	if err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
	}
	_ = sh.rt.saveSession()
	fmt.Println()
	return "", false
}

func (sh *shell) cmdContext(string) (string, bool) {
	a := sh.rt.agent
	used := a.ContextTokens()
	win := max(a.ContextWindow, 1)
	pct := used * 100 / win
	bar := 30
	filled := min(bar, pct*bar/100)
	fmt.Printf("context  %s%s  ~%s of %s tokens (%d%%)\n", ui.Accent(strings.Repeat("█", filled)), ui.Dim(strings.Repeat("░", bar-filled)), compactNum(used), compactNum(win), pct)
	fmt.Printf("%s\n", ui.Dim(fmt.Sprintf("%d messages · session %s · %s tokens used so far", len(a.Messages), sh.rt.sess.ID, compactNum(a.Usage.TotalTokens))))
	fmt.Println(ui.Dim("The window size is set by contextWindow in settings (kalonide config set context-window N).") + "\n")
	return "", false
}

func (sh *shell) cmdTools(string) (string, bool) {
	mode := sh.gate.Mode()
	for _, t := range sh.rt.reg.List() {
		policy := "runs freely"
		switch t.Kind() {
		case tools.KindWrite:
			if mode == config.ModeAsk {
				policy = "asks first"
			}
		case tools.KindShell, tools.KindNetwork:
			if mode != config.ModeYolo {
				policy = "asks first"
			}
		}
		if sh.gate.always[t.Name()] {
			policy = "approved for this session"
		}
		fmt.Printf("  %s %s %s\n", ui.Accent(fmt.Sprintf("%-15s", t.Name())), ui.Dim(fmt.Sprintf("%-8s", t.Kind())), policy)
	}
	fmt.Println(ui.Dim("\n  Destructive shell commands (rm -rf, force push, sudo…) always ask.") + "\n")
	return "", false
}

func (sh *shell) cmdAddDir(arg string) (string, bool) {
	if arg == "" {
		fmt.Println(ui.Yellow("! ") + "usage: /add-dir <path>")
		return "", false
	}
	if strings.HasPrefix(arg, "~/") {
		home, _ := os.UserHomeDir()
		arg = filepath.Join(home, arg[2:])
	}
	if err := addRoot(sh.rt.env, arg); err != nil {
		fmt.Println(ui.Red("! ") + err.Error())
		return "", false
	}
	fmt.Printf("%s writes now also allowed in %s\n\n", ui.Green("✔"), sh.rt.env.ExtraRoots[len(sh.rt.env.ExtraRoots)-1])
	return "", false
}

func (sh *shell) cmdMemory(string) (string, bool) {
	mems := prompt.LoadMemories(sh.rt.env.ProjectMemoryDir, sh.rt.env.UserMemoryDir)
	if len(mems) == 0 {
		fmt.Println(ui.Dim("No saved memories. The agent saves them with its remember tool; ask it to remember something.") + "\n")
		return "", false
	}
	for _, m := range mems {
		body := strings.Join(strings.Fields(m.Body), " ")
		fmt.Printf("  %s %s  %s\n", ui.Dim("["+m.Scope+"]"), ui.Bold(m.Name), ui.Dim(ui.Sanitize(clipText(body, 80))))
	}
	fmt.Println()
	return "", false
}

func (sh *shell) cmdInit(string) (string, bool) {
	return prompt.InitPrompt, false
}

func (sh *shell) cmdThinking(string) (string, bool) {
	sh.render.ShowThinking = !sh.render.ShowThinking
	state := "hidden"
	if sh.render.ShowThinking {
		state = "shown"
	}
	fmt.Println(ui.Dim("Model thinking is now "+state+".") + "\n")
	return "", false
}

func (sh *shell) cmdDoctor(string) (string, bool) {
	runDoctor(doctorOptions{provider: sh.rt.provider.Name})
	fmt.Println()
	return "", false
}

func (sh *shell) cmdConfig(string) (string, bool) {
	printConfig(false)
	fmt.Println()
	return "", false
}

func (sh *shell) cmdExit(string) (string, bool) { return "", true }

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2 Jan 2006")
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (sh *shell) cachedInfo() []llm.ModelInfo {
	sh.modelsMu.Lock()
	defer sh.modelsMu.Unlock()
	return sh.modelInfo
}
