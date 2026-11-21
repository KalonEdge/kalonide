package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KalonEdge/kalonide/internal/sandbox"
)

// Procs tracks commands started in the background, such as dev servers,
// so the model can read their output later and stop them. All of them are
// stopped when Kalonide exits.
type Procs struct {
	mu    sync.Mutex
	next  int
	procs map[string]*proc
}

type proc struct {
	id      string
	command string
	cmd     *exec.Cmd
	started time.Time

	mu      sync.Mutex
	buf     []byte // the most recent output
	base    int64  // total bytes dropped from the front of buf
	read    int64  // how far the model has read, in total bytes
	done    bool
	code    int
	changed chan struct{} // closed and replaced whenever output arrives or the process ends
}

const procKeep = 1 << 20 // output kept per process

func (p *proc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	if over := len(p.buf) - procKeep; over > 0 {
		p.buf = p.buf[over:]
		p.base += int64(over)
	}
	close(p.changed)
	p.changed = make(chan struct{})
	return len(b), nil
}

// unread returns output since the last read, and whether some was lost.
func (p *proc) unread() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	lost := p.read < p.base
	from := max(p.read-p.base, 0)
	out := string(p.buf[from:])
	p.read = p.base + int64(len(p.buf))
	return out, lost
}

func (p *proc) status() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.code
}

// Start runs command in dir in the background and returns its id.
func (ps *Procs) Start(dir, command string, policy *sandbox.Policy) (string, error) {
	ps.mu.Lock()
	if ps.procs == nil {
		ps.procs = map[string]*proc{}
	}
	running := 0
	for _, p := range ps.procs {
		if done, _ := p.status(); !done {
			running++
		}
	}
	if running >= 8 {
		ps.mu.Unlock()
		return "", errors.New("8 background commands are already running; stop one with bash_stop first")
	}
	ps.next++
	id := fmt.Sprintf("bg%d", ps.next)
	ps.mu.Unlock()

	argv := shellArgv(command, policy)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "KALONIDE=1", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := &proc{id: id, command: command, cmd: cmd, started: time.Now(), changed: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p, p
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.done = true
		p.code = cmd.ProcessState.ExitCode()
		if err != nil && p.code == 0 {
			p.code = -1
		}
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
	}()
	ps.mu.Lock()
	ps.procs[id] = p
	ps.mu.Unlock()
	return id, nil
}

func (ps *Procs) get(id string) (*proc, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.procs[id]
	if !ok {
		var ids []string
		for k := range ps.procs {
			ids = append(ids, k)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("no background command %q (known: %s)", id, strings.Join(ids, ", "))
	}
	return p, nil
}

// Output waits up to wait for new output (or the end of the process), then
// returns everything new since the last call.
func (ps *Procs) Output(ctx context.Context, id string, wait time.Duration) (string, error) {
	p, err := ps.get(id)
	if err != nil {
		return "", err
	}
	if wait > 0 {
		p.mu.Lock()
		pending := int64(len(p.buf))+p.base > p.read
		ch, done := p.changed, p.done
		p.mu.Unlock()
		if !pending && !done {
			select {
			case <-ch:
			case <-time.After(wait):
			case <-ctx.Done():
			}
		}
	}
	out, lost := p.unread()
	var b strings.Builder
	if lost {
		b.WriteString("[earlier output was dropped]\n")
	}
	if out != "" {
		b.WriteString(Truncate(strings.TrimRight(out, "\n"), MaxOutput-200) + "\n")
	}
	if done, code := p.status(); done {
		fmt.Fprintf(&b, "[%s exited with code %d]", id, code)
	} else {
		fmt.Fprintf(&b, "[%s still running, %s]", id, time.Since(p.started).Round(time.Second))
	}
	return b.String(), nil
}

// Stop ends a background command and its children.
func (ps *Procs) Stop(id string) (string, error) {
	p, err := ps.get(id)
	if err != nil {
		return "", err
	}
	if done, code := p.status(); done {
		return fmt.Sprintf("%s had already exited (code %d).", id, code), nil
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-waitDone(p):
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	return "Stopped " + id + ".", nil
}

func waitDone(p *proc) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			p.mu.Lock()
			done, changed := p.done, p.changed
			p.mu.Unlock()
			if done {
				close(ch)
				return
			}
			<-changed
		}
	}()
	return ch
}

// List describes the background commands, for /ps.
func (ps *Procs) List() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []string
	for _, p := range ps.procs {
		state := "running"
		if done, code := p.status(); done {
			state = fmt.Sprintf("exited %d", code)
		}
		out = append(out, fmt.Sprintf("%s  %-10s %s", p.id, state, p.command))
	}
	sort.Strings(out)
	return out
}

// StopAll ends every background command (on exit).
func (ps *Procs) StopAll() {
	ps.mu.Lock()
	var ids []string
	for id, p := range ps.procs {
		if done, _ := p.status(); !done {
			ids = append(ids, id)
		}
	}
	ps.mu.Unlock()
	for _, id := range ids {
		ps.Stop(id)
	}
}

// ---- tools ----

// BashOutput reads new output from a background command.
type BashOutput struct{}

func (BashOutput) Name() string { return "bash_output" }
func (BashOutput) Kind() Kind   { return KindInteract }
func (BashOutput) Description() string {
	return "Read new output from a command started with bash background=true. wait (seconds, up to 60) waits for output to arrive."
}
func (BashOutput) Schema() json.RawMessage {
	return schema([]string{"id"}, [3]string{"id", "string", "The id bash returned, e.g. bg1"}, [3]string{"wait", "integer", "Seconds to wait for new output (default 2, max 60)"})
}
func (BashOutput) Summary(args json.RawMessage) string {
	var a struct{ ID string }
	_ = json.Unmarshal(args, &a)
	return a.ID
}
func (BashOutput) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a struct {
		ID   string `json:"id"`
		Wait int    `json:"wait"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if env.Procs == nil {
		return "", errors.New("background commands are not available here")
	}
	wait := 2
	if a.Wait > 0 {
		wait = min(a.Wait, 60)
	}
	return env.Procs.Output(ctx, a.ID, time.Duration(wait)*time.Second)
}

// BashStop stops a background command.
type BashStop struct{}

func (BashStop) Name() string { return "bash_stop" }
func (BashStop) Kind() Kind   { return KindInteract }
func (BashStop) Description() string {
	return "Stop a command started with bash background=true."
}
func (BashStop) Schema() json.RawMessage {
	return schema([]string{"id"}, [3]string{"id", "string", "The id, e.g. bg1"})
}
func (BashStop) Summary(args json.RawMessage) string {
	var a struct{ ID string }
	_ = json.Unmarshal(args, &a)
	return a.ID
}
func (BashStop) Run(_ context.Context, env *Env, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if env.Procs == nil {
		return "", errors.New("background commands are not available here")
	}
	return env.Procs.Stop(a.ID)
}
