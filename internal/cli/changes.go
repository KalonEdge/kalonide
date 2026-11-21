package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/tools"
	"github.com/KalonEdge/kalonide/internal/ui"
)

const changesHelp = `Show the working tree's changes and commit them.

Usage:
  kalonide changes                      what changed (git status and diff stats)
  kalonide changes commit -m "message"  commit everything with your message
  kalonide changes commit --auto        let the model write the message from the diff;
                                        you see it and confirm (--yes skips that)

Flags:
`

func cmdChanges(args []string) int {
	var cwd, message, provider, model string
	var auto, yes, help bool
	f := newFlags("changes")
	f.String(&cwd, "<dir>", "Repository folder (default: current)", "C", "cwd")
	f.String(&message, "<text>", "Commit message", "m", "message")
	f.Bool(&auto, "Write the commit message with the model", "auto", "a")
	f.Bool(&yes, "Commit without asking to confirm the message", "yes", "y")
	f.String(&provider, "<name>", "Provider for --auto", "provider")
	f.String(&model, "<model>", "Model for --auto", "model")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print(changesHelp + f.Usage())
		return exitOK
	}
	dir := cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	git := func(a ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput()
		return strings.TrimRight(string(out), "\n"), err
	}
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return fail(errors.New(dir + " is not a git repository"))
	}
	status, _ := git("status", "--short")
	sub := ""
	if len(f.args) > 0 {
		sub = f.args[0]
	}
	switch sub {
	case "", "show", "inspect":
		if status == "" {
			fmt.Println("No changes.")
			return exitOK
		}
		fmt.Println(status)
		if stat, _ := git("diff", "HEAD", "--stat"); stat != "" {
			fmt.Println("\n" + ui.Dim(stat))
		}
		return exitOK
	case "commit":
	default:
		return usageFail(fmt.Errorf("unknown changes command %q", sub))
	}

	if status == "" {
		fmt.Println("Nothing to commit.")
		return exitOK
	}
	if message == "" && !auto {
		return usageFail(errors.New("give a message with -m, or use --auto to have the model write one"))
	}
	if _, err := git("add", "-A"); err != nil {
		return fail(err)
	}
	if auto {
		diff, _ := git("diff", "--cached", "--stat", "--patch", "--no-color")
		var err error
		message, err = writeCommitMessage(provider, model, diff)
		if err != nil {
			git("reset", "-q")
			return fail(err)
		}
		fmt.Println(ui.Bold("Commit message:") + "\n\n" + ui.Sanitize(message) + "\n")
		if !yes {
			if !ui.IsTerminal(os.Stdin) {
				git("reset", "-q")
				return usageFail(errors.New("not a terminal: pass --yes to commit with this message"))
			}
			term := ui.NewTerminal()
			key, _ := ui.Confirm(term, "Commit with this message?", []ui.Choice{{Key: 'y', Label: "yes"}, {Key: 'n', Label: "no"}})
			if key != 'y' {
				git("reset", "-q")
				fmt.Println("Not committed; your changes are as they were.")
				return exitOK
			}
		}
	}
	out, err := git("commit", "-q", "-m", message)
	if err != nil {
		return fail(fmt.Errorf("git commit failed: %s", out))
	}
	head, _ := git("log", "-1", "--format=%h %s")
	fmt.Println(ui.Green("✔ ") + "committed " + head)
	return exitOK
}

const commitPrompt = `Write a git commit message for the staged changes below.
Format: a subject line of at most 72 characters in the imperative mood ("Add…", "Fix…"), a blank line, then a short body that says what changed and why, as plain sentences or "- " bullets.
Reply with the message only: no code fences, no quotes, no preamble.`

// writeCommitMessage asks the model for a commit message for diff.
func writeCommitMessage(providerName, model, diff string) (string, error) {
	rt, err := newRuntime(runOptions{provider: providerName, model: model, noTools: true})
	if err != nil {
		return "", err
	}
	defer rt.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := rt.ensureModel(ctx); err != nil {
		return "", err
	}
	resp, err := rt.client.Chat(ctx, llm.Request{
		Model: rt.agent.Model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: commitPrompt},
			{Role: llm.RoleUser, Content: tools.Truncate(tools.Redact(diff), 24000)},
		},
	}, nil)
	if err != nil {
		return "", connectHint(err, rt.provider)
	}
	msg := strings.TrimSpace(resp.Content)
	msg = strings.TrimPrefix(strings.TrimSuffix(msg, "```"), "```")
	msg = strings.Trim(strings.TrimSpace(msg), `"`)
	if msg == "" {
		return "", errors.New("the model returned an empty commit message")
	}
	return msg, nil
}
