# Changelog

## 0.1.0 — first release

Kalonide is a coding agent for the terminal, built for Kalon and any
OpenAI-compatible model server.

**Working with Kalon**
- Works with Kalon out of the box (`localhost:11434`), no setup step.
- Picks the best tool-capable model Kalon offers, from Kalon's own
  capability list; live `/model` picker with type-to-filter.
- Follows Kalon across restarts and port changes; warms the model while
  you type; image input for vision models.
- Recovers tool calls Kalon drops (a leaked `<channel|>` token that ends
  the reply), and nudges small models that stop mid-task.
- Benchmark: 62% of 20 real coding tasks on `gemma4:4b`, up from 25% for
  the first build (`docs/BENCH.md`, `make bench`).

**The shell**
- KALONIDE wordmark with the Kalon logo; a rule naming the model and
  a status line with the mode, context use and sandbox state.
- Replies stream live and are formatted (headings, lists, bold, code).
- Every file change shows its red/green diff, before approval in ask mode.
- `/undo` and `/rewind` restore files and the conversation.
- Multi-line input, history, bracketed paste, Tab completion, Shift+Tab
  modes (ask, auto-edit, yolo), `!` shell escape.

**Tools**
- read_file, list_directory, glob, grep, repo_map, write_file, edit_file,
  apply_patch, bash (also in the background, with bash_output and
  bash_stop), verify, web_fetch, update_plan, ask_user, remember, skill,
  task (a read-only helper agent with a fresh context).
- Self-check: run the project's own checks before finishing and fix
  failures (`--self-check`, `/selfcheck`).

**Extending**
- MCP servers (stdio and HTTP) with `kalonide mcp`; only tools you approve
  run without asking.
- Your own slash commands and skills in `.kalonide/` or your home folder.
- Hooks before and after tool calls and at the end of a request.

**Safety**
- Workspace confinement, symlinks included; secrets redacted from output.
- Destructive commands always need a person's yes, and are refused in
  exec; detection sees through quoting, paths and variables.
- macOS sandbox for the agent's commands in auto-edit, yolo and exec:
  writes only in the workspace, temp and build caches; network only to
  this machine; no AppleScript or `open`; `.git` locked in any spelling
  except what commits need.
- Workspace trust before a folder's own notes, memories, skills or
  commands are used.
- Audit log of every tool call (`kalonide sessions audit`).

**Everywhere else**
- `kalonide exec` for scripts and CI: text, JSON and stream-JSON output,
  resume and fork, meaningful exit codes.
- `kalonide changes` with model-written commit messages, `search`,
  `usage`, `doctor`, `verify`, `repo-map`.
- `kalonide serve --mcp` offers Kalonide's tools to other apps.
- `kalonide acp` for editors such as Zed (experimental).

**Known limits**
- The sandbox is macOS only; on other systems approvals are the safeguard.
- `kalonide acp` has not yet been tried in an editor.
- Kalon reports no token counts, so usage shows estimates.
