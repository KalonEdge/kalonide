# Kalonide

A coding agent for your terminal. It works with **Kalon** out of the box,
and with any OpenAI-compatible model server: Ollama, LM Studio, OpenRouter,
OpenAI, or your own.

```bash
make install        # builds ./kalonide and links it into /usr/local/bin
kalonide            # interactive shell in the current folder
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
`/clear`, `/resume`, `/sessions`, `/compact`, `/context`, `/tools`,
`/add-dir`, `/memory`, `/init`, `/thinking`, `/doctor`, `/config`, `/exit`.

**One-shot, for scripts and CI:** `kalonide exec "fix the failing test"`

- `-o text|json|stream-json` (schemas `kalonide.exec.v1` / `kalonide.stream.v1`)
- `--auto-edit` lets it change files; `--yolo` also allows shell and web.
  Without them, nobody is there to approve, so those actions are refused.
- `--resume[=id]`, `--fork <id>`, `-f prompt.md`, or pipe the prompt on stdin.
- Exit codes: 0 done · 1 error · 2 bad usage · 3 turn limit · 130 cancelled.

**Admin:** `kalonide doctor` (also finds model servers on this machine),
`models` / `models use <id>`, `providers add|use|remove`, `config set`,
`sessions`, `resume`, `init`.

## Safety

- Reads stay inside the workspace (plus folders you add with `--add-dir`);
  symlinks cannot escape it.
- File changes, shell commands and web access ask first, unless you
  choose auto-edit or yolo.
- Destructive commands (recursive deletes, force push, hard resets, `sudo`,
  piping downloads into a shell…) always need a person's yes, in every mode,
  and are refused in `exec`.
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
```

Layout: `cmd/kalonide` (entry) · `internal/llm` (model client) ·
`internal/agent` (loop, compaction) · `internal/tools` (tools, sandboxing,
redaction) · `internal/cli` (commands, shell, permissions) · `internal/ui`
(terminal, editor, picker, renderer) · `internal/session`,
`internal/config`, `internal/prompt`.

The feature roadmap is [docs/FEATURE_INDEX.md](docs/FEATURE_INDEX.md).
Kalonide has one dependency outside the Go standard library:
`golang.org/x/sys`, for terminal raw mode.
