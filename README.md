# Kalonide

Kalonide is a coding agent for your terminal. It reads your code, makes
changes, and runs your commands and tests, asking before anything you
haven't allowed.

It works with Kalon out of the box, and with any OpenAI-compatible model
server, including Ollama, LM Studio, OpenRouter and OpenAI.

![Kalonide in a terminal](docs/startup-screen.png)

## Install

**Homebrew** (macOS and Linux)

```bash
brew install kalonedge/tap/kalonide
```

**npm**

```bash
npm install -g kalonide
```

**Install script**

```bash
curl -fsSL https://raw.githubusercontent.com/KalonEdge/kalonide/main/install.sh | sh
```

Builds are also on the
[releases page](https://github.com/KalonEdge/kalonide/releases). Kalonide
runs on macOS (Apple silicon and Intel) and Linux (arm64 and x86-64). The
macOS builds are signed and notarized by Apple.

## Get started

1. Open the Kalon app and make sure its local API is on.
2. In your project folder, run:

   ```bash
   kalonide
   ```

3. Ask for what you need, for example *"add a dark mode toggle to the
   settings page"* or *"why does the login test fail?"*

There is nothing to configure. Kalonide connects to Kalon and picks the best
model available for coding. Type `/model` to choose another.

### Other model servers

To use another server, add it once:

```bash
kalonide providers add openrouter --base-url https://openrouter.ai/api/v1 --api-key-env OPENROUTER_API_KEY --use
```

API keys are read from the environment variable you name and never stored.
`kalonide doctor` checks your setup and finds model servers running on your
machine.

## Usage

### Approval modes

Press **Shift+Tab** to switch modes:

| Mode | Asks before |
|---|---|
| **Ask** (default) | Every file change, shell command and web request |
| **Auto-edit** | Shell commands and web requests |
| **Yolo** | Commands that could destroy data |

You see the exact diff before a file changes and the exact command before
it runs. `/undo` reverts the file changes from your last request; `/rewind`
returns your files and the conversation to an earlier point.

### Keyboard

| Key | Action |
|---|---|
| Enter | Send |
| Alt+Enter, or `\` then Enter | New line |
| ↑ ↓ | Previous prompts |
| Tab | Complete a command |
| Shift+Tab | Switch approval mode |
| Ctrl+C | Stop the current reply; twice on an empty line to quit |
| `!` + command | Run a shell command yourself |

### Commands

Type `/` for the full list.

| Command | Action |
|---|---|
| `/model` | Choose a model |
| `/new` | Start a new conversation |
| `/undo`, `/rewind` | Revert changes |
| `/resume` | Continue an earlier session |
| `/context` | Show how much of the context window is in use |
| `/compact` | Summarize the conversation to free up context |
| `/init` | Create a `KALONIDE.md` for this project |
| `/help` | All commands and keys |

### Project context

- **`KALONIDE.md`** describes how your project builds, its conventions and
  what to avoid. `kalonide init` creates one. `AGENTS.md` is read too.
- **Memories** are facts Kalonide saves about a project, in
  `.kalonide/memory/`, or in `~/.config/kalonide/memory/` for all projects.
  In Ask mode, it checks with you first.
- **Images** can be attached with `/image` when you use a vision model.

A folder's notes, memories, commands and skills are used only after you
trust the folder. Kalonide asks the first time you open it.

## Scripts and CI

`kalonide exec` runs one request without the interactive shell:

```bash
kalonide exec "fix the failing test"
```

By default it only reads. `--auto-edit` lets it edit files, and `--yolo`
also lets it run commands. Commands that could destroy data are always
refused.

| Option | Purpose |
|---|---|
| `-o json`, `-o stream-json` | Machine-readable output |
| `-f prompt.md` | Read the request from a file (or from stdin) |
| `--resume` | Continue the latest session |
| `--self-check` | Run the project's tests before finishing, and fix failures |

Exit codes: `0` done, `1` error, `2` invalid usage, `3` turn limit reached,
`130` cancelled.

## Safety and privacy

- **Workspace only.** Kalonide works inside the project folder and any
  folder you add with `--add-dir`. Symbolic links can't reach outside it.
- **Destructive commands always ask.** Recursive deletes, force pushes,
  hard resets, `sudo` and similar commands need your approval in every
  mode, even when quoted or written with a full path.
- **Sandbox on macOS.** In Auto-edit and Yolo modes, commands can write only
  to your project, temporary folders and build caches, and can connect only
  to your own machine. Git history and hooks are protected. On Linux,
  approvals are the safeguard. `/sandbox` shows the current state.
- **Credentials are protected.** API keys and tokens are hidden from
  command output and never written to disk.
- **Your code goes only to the model server you choose.** With Kalon,
  nothing leaves your machine. Sessions are stored locally, and
  `kalonide sessions audit <id>` lists every action in one.

## Extending

- **MCP servers** add tools such as a browser, a database or an issue
  tracker:

  ```bash
  kalonide mcp add playwright -- npx -y @playwright/mcp
  ```

  Their tools ask before running unless you approve them.
- **Commands.** A Markdown file in `.kalonide/commands/` becomes a slash
  command: `review.md` becomes `/review`.
- **Skills.** Instructions in `.kalonide/skills/<name>/SKILL.md` are used
  when relevant.
- **Hooks** run your scripts around Kalonide's work, for example formatting
  every file it edits. See `kalonide hooks --help`.
- **Editors and other apps.** `kalonide serve --mcp` offers Kalonide's tools
  over MCP, and `kalonide acp` connects to editors such as Zed
  (experimental).

Commands and skills in `~/.config/kalonide/` apply to every project.

## Settings

Settings are stored in `~/.config/kalonide/settings.json`. `kalonide config`
shows them and `kalonide config set <key> <value>` changes one. Environment
variables take precedence:

| Variable | Sets |
|---|---|
| `KALONIDE_PROVIDER` | Provider |
| `KALONIDE_MODEL` | Model |
| `KALONIDE_BASE_URL` | Server address |
| `KALONIDE_API_KEY` | API key |
| `KALONIDE_PERMISSION_MODE` | `ask`, `auto-edit` or `yolo` |
| `KALONIDE_HOME` | Settings folder |
| `NO_COLOR` | Turns off colors |

`kalonide --help` lists every command and option.

## Build from source

Requires Go 1.25 or later.

```bash
git clone https://github.com/KalonEdge/kalonide.git
cd kalonide
make build
```

Run the tests with `make test`.

## License

MIT. See [LICENSE](LICENSE).
