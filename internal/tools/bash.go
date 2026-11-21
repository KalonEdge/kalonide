package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KalonEdge/kalonide/internal/sandbox"
)

// Bash runs a shell command in the workspace.
type Bash struct{}

func (Bash) Name() string { return "bash" }
func (Bash) Kind() Kind   { return KindShell }
func (Bash) Description() string {
	return "Run a shell command in the workspace and return its output and exit code. No interactive input; commands that wait for input get end-of-file. Default timeout 120s, max 600s. For servers and other long-running commands set background=true, then use bash_output and bash_stop."
}
func (Bash) Schema() json.RawMessage {
	return schema([]string{"command"},
		[3]string{"command", "string", "Shell command"},
		[3]string{"timeout", "integer", "Timeout in seconds (default 120, max 600)"},
		[3]string{"workdir", "string", "Folder to run in (default: workspace root)"},
		[3]string{"background", "boolean", "Start it in the background and return an id at once (default false)"},
	)
}

type bashArgs struct {
	Command    string `json:"command"`
	Timeout    int    `json:"timeout"`
	Workdir    string `json:"workdir"`
	Background bool   `json:"background"`
}

func (Bash) Summary(args json.RawMessage) string {
	var a bashArgs
	_ = json.Unmarshal(args, &a)
	s := strings.TrimSpace(a.Command)
	if a.Workdir != "" && a.Workdir != "." {
		s = "(in " + a.Workdir + ") " + s
	}
	if a.Background {
		s += " (in the background)"
	}
	return s
}

// dangerRules flag commands that destroy data, rewrite history, escalate
// privileges or run remote code. Such commands always need approval.
// cmdWord matches the start of a command word: start of text or after a
// separator, optionally with a path such as /bin/ or ./ before the name.
const cmdWord = `(?:^|[^\w-])(?:[\w.~${}-]*/)*`

// dangerRules flag commands that destroy data, rewrite history, escalate
// privileges or run code built at run time. This is a safety net that
// catches common forms, not a sandbox: a determined command can evade any
// pattern list, which is why ask mode exists.
var dangerRules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(cmdWord + `rm\s+(?:\S+\s+)*?(?:-[a-zA-Z]*[rRfF][a-zA-Z]*|--recursive|--force)\b`), "recursive or forced delete"},
	{regexp.MustCompile(cmdWord + `(?:find)\s.*\s-delete\b`), "deletes the files find matches"},
	{regexp.MustCompile(cmdWord + `xargs\s+(?:\S+\s+)*?(?:[\w./-]*/)?rm\b`), "bulk delete"},
	{regexp.MustCompile(cmdWord + `(?:shred|wipefs|srm)\b`), "destroys file contents"},
	{regexp.MustCompile(cmdWord + `(?:sudo|doas)\s`), "runs as root"},
	{regexp.MustCompile(cmdWord + `su(?:\s|$)`), "switches user"},
	{regexp.MustCompile(cmdWord + `eval\s`), "runs code built at run time"},
	{regexp.MustCompile(`\|\s*(?:sudo\s+)?(?:[\w./-]*/)?(?:ba|z|da|k|fi)?sh\b`), "pipes text into a shell"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+push\b.*(?:\s--force\b|\s-f\b|\s--force-with-lease\b|\s--mirror\b|\s--delete\b|\s\+\S|\s:\S)`), "force push or remote delete"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+reset\s+.*--hard`), "discards local changes"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+clean\s+(?:\S+\s+)*?-[a-zA-Z]*[fdx]`), "deletes untracked files"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+(?:checkout|restore)\s+(?:\S+\s+)*?(?:--\s+)?\.(?:\s|$)`), "discards local changes"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+branch\s+(?:\S+\s+)*?-D\b`), "force-deletes a branch"},
	{regexp.MustCompile(cmdWord + `git(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+stash\s+(?:drop|clear)`), "deletes stashed work"},
	{regexp.MustCompile(cmdWord + `(?:mkfs\S*|fdisk|diskutil\s+(?:erase\S*|partition\S*)|dd\s+.*\bof=)`), "writes to disks"},
	{regexp.MustCompile(`>\s*/dev/(?:sd|disk|nvme|rdisk)`), "writes to a disk device"},
	{regexp.MustCompile(cmdWord + `chmod\s+(?:-[Rr]\s+)?[0-7]*777\b`), "makes files world-writable"},
	{regexp.MustCompile(cmdWord + `chown\s+-R\b`), "recursive ownership change"},
	{regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\};:`), "fork bomb"},
	{regexp.MustCompile(cmdWord + `(?:shutdown|reboot|halt|poweroff)(?:[^\w-]|$)`), "shuts the machine down"},
	{regexp.MustCompile(cmdWord + `kill(?:all)?\s+-9\s+(?:-1|1)\b`), "kills every process"},
	{regexp.MustCompile(cmdWord + `(?:(?:npm|pnpm|yarn|cargo|twine|gem)\s+publish|gh\s+release\s+create|gh\s+repo\s+delete)\b`), "publishes or deletes a package, release or repo"},
	{regexp.MustCompile(`(?i)\bdrop\s+(?:table|database|schema)\b`), "drops database objects"},
}

// unshell removes quoting and escaping and turns substitutions and groups
// into separators, so `'r'm -rf`, `r\m -rf`, `bash -c "rm -rf x"` and
// `$(rm -rf x)` are all seen as `rm -rf`.
func unshell(cmd string) string {
	r := strings.NewReplacer(
		"\\\n", " ", "\\", "", "'", "", `"`, "",
		"$(", " ; ", "`", " ; ", "${", " ; ", "<(", " ; ", ">(", " ; ",
		"\n", " ; ", "&&", " ; ", "||", " ; ",
		"}", " ", ")", " ", "(", " ", ":-", " ", ":=", " ",
	)
	return r.Replace(cmd)
}

// CommandDanger reports why a shell command is destructive, or "".
func CommandDanger(cmd string) string {
	for _, form := range []string{cmd, unshell(cmd)} {
		for _, r := range dangerRules {
			if r.re.MatchString(form) {
				return r.reason
			}
		}
	}
	return ""
}

// Danger reports why a command needs explicit approval, or "".
func (Bash) Danger(args json.RawMessage) string {
	var a bashArgs
	_ = json.Unmarshal(args, &a)
	return CommandDanger(a.Command)
}

func shellPath() string {
	for _, p := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "sh"
}

func (Bash) Run(ctx context.Context, env *Env, args json.RawMessage) (string, error) {
	var a bashArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("command is required")
	}
	timeout := 120 * time.Second
	if a.Timeout > 0 {
		timeout = time.Duration(min(a.Timeout, 600)) * time.Second
	}
	dir := env.Root
	if a.Workdir != "" {
		d, err := env.Resolve(a.Workdir)
		if err != nil {
			return "", err
		}
		dir = d
	}
	if a.Background {
		if env.Procs == nil {
			return "", errors.New("background commands are not available here")
		}
		id, err := env.Procs.Start(dir, a.Command, env.sandboxPolicy())
		if err != nil {
			return "", err
		}
		// Give it a moment, so early output or an immediate failure shows.
		first, _ := env.Procs.Output(ctx, id, 1500*time.Millisecond)
		return fmt.Sprintf("Started %s in the background. Read more with bash_output, stop it with bash_stop.\n%s", id, first), nil
	}
	return RunCommand(ctx, dir, a.Command, timeout, env.sandboxPolicy())
}

// RunCommand runs command with a shell in dir, inside the sandbox when
// policy is set. Output (stdout and stderr interleaved) is capped; on
// timeout or cancellation the whole process group is killed so background
// children do not linger.
func RunCommand(ctx context.Context, dir, command string, timeout time.Duration, policy *sandbox.Policy) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := shellArgv(command, policy)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = nil // /dev/null: nothing can block waiting for input
	cmd.Env = append(os.Environ(),
		"KALONIDE=1", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true", "EDITOR=true", "DEBIAN_FRONTEND=noninteractive",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &capBuffer{max: 200 << 10}
	cmd.Stdout = out
	cmd.Stderr = out

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	killed := ""
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		killed = "timed out after " + timeout.String()
		if errors.Is(ctx.Err(), context.Canceled) {
			killed = "cancelled"
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case waitErr = <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr = <-done
		}
	}

	text := strings.TrimRight(out.String(), "\n")
	var b strings.Builder
	if text != "" {
		b.WriteString(Truncate(text, MaxOutput-200))
		b.WriteString("\n")
	}
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	switch {
	case killed != "":
		fmt.Fprintf(&b, "[command %s; process killed]", killed)
	case waitErr != nil && exit < 0:
		fmt.Fprintf(&b, "[command failed: %v]", waitErr)
	default:
		fmt.Fprintf(&b, "[exit code %d, %.1fs]", exit, time.Since(start).Seconds())
	}
	if text == "" && killed == "" && exit == 0 {
		b.Reset()
		fmt.Fprintf(&b, "[no output; exit code 0, %.1fs]", time.Since(start).Seconds())
	}
	return b.String(), nil
}

// capBuffer keeps the first and last part of a large output stream.
type capBuffer struct {
	mu      sync.Mutex
	max     int
	head    bytes.Buffer
	tail    []byte
	dropped int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := c.max/2 - c.head.Len(); room > 0 {
		k := min(room, len(p))
		c.head.Write(p[:k])
		p = p[k:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if over := len(c.tail) - c.max/2; over > 0 {
			c.dropped += over
			c.tail = append(c.tail[:0], c.tail[over:]...)
		}
	}
	return n, nil
}

func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return c.head.String() + string(c.tail)
	}
	return c.head.String() + fmt.Sprintf("\n[… %d bytes omitted …]\n", c.dropped) + string(c.tail)
}
