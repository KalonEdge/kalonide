# Kalonide

![Kalonide starting up](docs/startup-screen.png)

A coding agent for your terminal. It works with **Kalon** out of the box,
and with any OpenAI-compatible model server: Ollama, LM Studio, OpenRouter,
OpenAI, or your own.

## Install

With Homebrew (macOS or Linux):

```bash
brew install kalonedge/tap/kalonide
```

With the installer (checks the download's SHA-256, never uses sudo):

```bash
curl -fsSL https://raw.githubusercontent.com/KalonEdge/kalonide/main/install.sh | sh
```

Or download an archive from the
[releases page](https://github.com/KalonEdge/kalonide/releases), or build
from source with Go 1.25+ (`make install`). The macOS binaries are signed
and notarized by Apple.

Then, in any project folder:

```bash
kalonide
```

Start the Kalon app with its local API on (port 11434) and that's it: no
setup step. Kalonide picks the best tool-capable model Kalon offers; change
it any time with `/model`.

## Using it

**Interactive shell:** `kalonide`

| Key | Does |
|---|---|
| Enter | send |
| `\` then Enter, or Alt+Enter | new line |
| ↑ ↓ | history |
| Tab | complete `/commands` |
| Shift+Tab | cycle mode: ask → auto-edit → yolo |
| Ctrl-C | stop the reply; twice on an empty line to exit |
| `!cmd` | run a shell command yourself |

Commands: `/model` (live picker), `/models`, `/provider`, `/mode`, `/new`,
`/clear`, `/undo`, `/rewind`, `/resume`, `/sessions`, `/search`, `/compact`,
`/context`, `/tools`, `/add-dir`, `/image`, `/memory`, `/mcp`, `/hooks`,
`/ps`, `/sandbox`, `/selfcheck`, `/init`, `/thinking`, `/doctor`, `/config`,
`/exit`, plus your own commands from `.kalonide/commands/`.

**One-shot, for scripts and CI:** `kalonide exec "fix the failing test"`

- `-o text|json|stream-json` (schemas `kalonide.exec.v1` / `kalonide.stream.v1`)
- `--auto-edit` lets it change files; `--yolo` also allows shell and web.
  Without them, nobody is there to approve, so those actions are refused.
- `--resume[=id]`, `--fork <id>`, `-f prompt.md`, or pipe the prompt on stdin.
- Exit codes: 0 done · 1 error · 2 bad usage · 3 turn limit · 130 cancelled.

**Admin:** `kalonide doctor` (also finds model servers on this machine),
`models` / `models use <id>`, `providers add|use|remove`, `config set`,
`sessions` (and `sessions audit <id>`), `resume`, `init`, `trust`.

**More:** `verify` (the project's checks), `repo-map`, `changes` (and
`changes commit --auto` with a model-written message), `search`, `usage`,
`mcp` (add outside tools), `hooks`, `serve --mcp` (offer Kalonide's tools
to other apps), `acp` (editors such as Zed; experimental).

## Safety

- Reads stay inside the workspace (plus folders you add with `--add-dir`);
  symlinks cannot escape it.
- File changes, shell commands and web access ask first, unless you
  choose auto-edit or yolo.
- A guard flags destructive commands (recursive deletes, `find -delete`,
  force push, hard resets, `sudo`, `eval`, piping into a shell…), including
  quoted, escaped or full-path forms. Flagged commands need a person's yes
  in every mode and are refused in `exec`. It is a safety net, not a
  sandbox: use ask mode for untrusted work.
- Saving a memory counts as a file change, so ask mode asks first.
- `KALONIDE.md`, `AGENTS.md` and memories are given to the model as project
  notes, never as instructions that outrank you.
- Model and tool output is stripped of terminal control codes before it is
  shown, and approval prompts show the exact command.
- On macOS, in auto-edit, yolo and `exec`, the agent's shell commands run
  in a sandbox (`sandbox-exec`): writes only in
  the workspace, temp folders and build caches; network only to this
  machine (`sandboxNetwork` to allow more); no AppleScript or `open`; no
  Docker or SSH-agent sockets; `.git` cannot be created, replaced or have
  its hooks or config changed (so `git init` is yours to run; commits
  work). Other systems have no sandbox yet; there, approvals are the
  safeguard (`/sandbox` and `doctor` say so).
  Package caches stay writable for builds (Go and npm verify their
  contents). `/sandbox` shows the current state.
- A folder's own `KALONIDE.md`, `AGENTS.md`, memories, skills and commands
  are used only once you trust the folder (asked on first use;
  `kalonide trust`, `--trust`).
- `web_fetch` refuses localhost and private networks.
- Credentials are redacted from tool output. API keys are never written to
  disk; providers name the environment variable that holds them.
- Sessions stay on this machine.

## Robustness

Retries with backoff and `Retry-After`, a stall watchdog, fast failure when
the server is down, fallback models on overload, automatic plain-chat mode
for models without tool calling (read from the server's capability list
when it publishes one), recovery of tool calls that small models write as
text, repair of cut-off JSON arguments, automatic continuation of answers
cut off at the output limit, and a three-step context ladder (clear old
tool output → summarize → shorten the largest output).

## Project files

- `KALONIDE.md` (and `AGENTS.md`) in the project root are read into every
  session. `kalonide init` writes one.
- `.kalonide/memory/` holds project memories the agent saves with its
  `remember` tool; `~/.config/kalonide/memory/` holds personal ones.

## Settings

`~/.config/kalonide/settings.json` (or `$KALONIDE_HOME`). Environment
overrides: `KALONIDE_PROVIDER`, `KALONIDE_BASE_URL`, `KALONIDE_MODEL`,
`KALONIDE_API_KEY`, `KALONIDE_PERMISSION_MODE`, `KALONIDE_MAX_TURNS`,
`KALONIDE_STREAM_IDLE_TIMEOUT`, `NO_COLOR`.

## Development

```bash
make test     # unit + end-to-end tests (the e2e suite builds the binary)
make race     # with the race detector
make vet      # vet + gofmt check
make shell-check  # 23 checks typed into the shell through a real terminal
make release VERSION=0.1.0  # archives and checksums in dist/
make bench   # the scoreboard: 20 real tasks on Kalon (BENCH_MODEL=gemma4:4b)
```

Layout: `cmd/kalonide` (entry) · `internal/llm` (model client) ·
`internal/agent` (loop, compaction) · `internal/tools` (tools, sandboxing,
redaction) · `internal/cli` (commands, shell, permissions) · `internal/ui`
(terminal, editor, picker, renderer) · `internal/session`,
`internal/config`, `internal/prompt`.

Kalonide has one dependency outside the Go standard library:
`golang.org/x/sys`, for terminal raw mode.

## License

MIT. See [LICENSE](LICENSE).
