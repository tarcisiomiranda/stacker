# Stacker

A terminal process supervisor for local development. Define your project's
long-running services (API, frontend, workers) in one YAML file and start,
stop, restart, and read their logs from a single TUI — or from a browser, or
from the CLI that AI coding agents can drive.

Stacker exists so you (and your agents) stop juggling half a dozen terminal
tabs and stray `mise run` / `npm run dev` processes that leave ports bound.

## Features

- **YAML-defined processes** — one entry per service; graceful start, stop, and restart.
- **Automatic port freeing** — an optional `port:` is cleared before every start/restart, killing the whole supervisor tree (`npm → node`, `mise → uvicorn`) so restarts don't fail with "address already in use". Works on Linux, macOS, and Windows.
- **Split log capture** — separate stdout/stderr, scrollable, with a configurable per-process memory cap.
- **CLI control plane** — `list`/`start`/`stop`/`restart`/`run` a running instance from scripts and agents, instead of spawning services in parallel.
- **On-demand web viewer** — press `w` for a browser UI on `127.0.0.1` (random high port): live logs, start/stop/restart, drag-to-reorder, and more.
- **One-shot tasks** — named commands (migrations, seeds, deploys) that run once and exit, either scoped to a process or standalone.
- **Error highlighting** — opt-in orange badge when output looks like a traceback/panic/error, even while the service keeps running.
- **Word wrap, per-process color dots, log marks, and a help overlay** — all toggleable at runtime; color and order changes are written back to the YAML.
- **Clipboard-friendly** — drag to select, copy through native tools (`pbcopy`/`wl-copy`/`xclip`) with an OSC 52 fallback for SSH.

## Requirements

- Linux or macOS (Windows is supported for `free-port`; process-group signaling there is best-effort). The TUI targets Unix shells.
- Nothing to install at runtime — the release binary is static. Building from source needs Go 1.24+.

## Quick installation

```bash
curl -fsSL https://raw.githubusercontent.com/tarcisiomiranda/stacker/main/install.sh | bash
```

The installer detects the operating system and architecture, downloads the latest release, and verifies its SHA-256 checksum. When run as `root`, it installs Stacker in `/usr/local/bin`. For other users, it falls back to `~/.local/bin` when `/usr/local/bin` is not writable.

**Agent skills are off by default.** To also detect AI tools on the machine (Claude Code, Codex, OpenCode, Cursor, Grok, …) and install the Stacker `SKILL.md` for them:

```bash
curl -fsSL https://raw.githubusercontent.com/tarcisiomiranda/stacker/main/install.sh \
  | STACKER_INSTALL_SKILLS=1 bash
```

Accepted truthy values for `STACKER_INSTALL_SKILLS`: `1`, `true`, `yes`, `on`.

To choose an installation directory or a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/tarcisiomiranda/stacker/main/install.sh \
  | STACKER_INSTALL_DIR="$HOME/.local/bin" STACKER_VERSION=v0.1.0 bash
```

## Quick start

Create a `stacker.yml` next to your project:

```yaml
version: 1

processes:
  backend:
    command: mise run back:dev
    port: 8000
    autostart: true
  frontend:
    command: mise run front:dev
    port: 3000
```

Then run:

```bash
stacker
```

Without `-config`, Stacker looks for `stacker.yml` in the current directory; pass `-config path/to/file.yml` to point elsewhere. Autostart processes (like `backend` above) come up immediately; others are listed and start on `Enter` or via the CLI. Press `?` at any time for the full key list.

## Configuration

Full-featured example:

```yaml
version: 1

ui:
  wheel_lines: 3          # lines scrolled per mouse-wheel notch
  copy_on_release: true   # copy the selection when the mouse button is released
  max_log_lines: 10000    # per-process log memory cap
  word_wrap: false        # initial wrap state (toggle at runtime with W / web checkbox)
  highlight_errors: false # opt-in orange badge on error-looking output

processes:
  backend:
    command: mise run back:dev
    cwd: .                 # resolved relative to this file; must exist
    autostart: false       # registered but not started until Enter/CLI
    graceful_timeout: 8s   # SIGTERM grace before SIGKILL
    port: 8000             # freed before every start/restart
    color: "#38bdf8"       # dot for visual grouping (quote hex — # starts a comment)
    tasks:                 # per-process one-shot commands (stream into this log)
      migrate: mise run migrate
      seed: python manage.py seed

# Standalone one-shot tasks: their own entry (▶) with their own log, tied to
# no single process. Run once and return to "idle".
tasks:
  deploy:
    command: ./deploy.sh
    cwd: ./infra
  backup-db:
    command: pg_dump app > backup.sql
```

Unknown YAML fields, empty commands, missing directories, invalid timeouts/ports/colors, and duplicate names are rejected before the TUI opens. Each command inherits the environment (including `PATH`) from the user who started Stacker and runs through `/bin/sh -c`; if `mise` already works in your terminal, `command: mise run task-name` works with no extra setup.

### `ui` fields

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `wheel_lines` | int ≥ 0 | `3` | Lines scrolled per mouse-wheel notch. |
| `copy_on_release` | bool | `false` | Copy the selection through OSC 52 when the mouse button is released. |
| `max_log_lines` | int ≥ 0 | `10000` | Per-process in-memory log cap. |
| `word_wrap` | bool | `false` | Initial log wrap state; toggle at runtime (`W`, or the web `wrap` box). |
| `highlight_errors` | bool | `false` | Match each line against error patterns and badge the process. |

### `processes` fields

Each key under `processes:` is a service name (non-empty, unique). **Key order is the display order** in the TUI and web sidebar.

| Field | Required | Meaning |
|-------|----------|---------|
| `command` | yes | Shell command run with `/bin/sh -c`. Prefer a project task like `mise run back:dev`. |
| `cwd` | no | Working directory, relative to the config file. Must exist. Defaults to `.`. |
| `autostart` | no | Start when Stacker opens. Defaults to `false`. |
| `graceful_timeout` | no | Go duration (`500ms`, `8s`, `1m30s`) to wait after SIGTERM before SIGKILL. Defaults to `8s`. |
| `port` | no | TCP port (1–65535) freed before every start/restart. |
| `color` | no | Hex (`"#0af"`, quoted) or CSS name; draws a colored dot. Editable at runtime. |
| `tasks` | no | Map of `name: command` one-shot commands scoped to this process (see below). |

### Tasks

Two kinds of one-shot commands — for the "run once, process, exit" work you'd
otherwise type in a second terminal:

- **Per-process tasks** (`tasks:` nested under a process) run in that process's `cwd`, stream into its log prefixed `[task <name>]`, and **do not** change its status — the `--reload` server keeps serving. Trigger from the TUI (`t`), web (More ▾ → Tasks), or `stacker run <process> <task>`.
- **Standalone tasks** (root-level `tasks:`) are their own list entry (marked `▶`) with their own log, tied to no process. Fields: `command` (required), optional `cwd` and `color`. Running one (TUI `Enter`, web `▶ Run`, or `stacker start <task>`) executes it and returns to `idle` — a clean exit is not a failure. Names must not clash with process names; they stay pinned after the processes and are excluded from reordering.

### Runtime behavior worth knowing

- **Free-port** targets the listener's whole process group, so supervisor trees (`npm → node`, `mise → uvicorn`) go down together instead of respawning the server; it retries a few rounds before reporting the port as still busy.
- **Color** changes (TUI `c`, web selector) and **order** changes (TUI `Shift+↑/↓`, web drag) are written back to `stacker.yml`, preserving comments and formatting — so the file is not static while Stacker runs.
- **Error highlighting** (`highlight_errors: true`) matches every captured line against built-in patterns (Python tracebacks, Go panics, JS/TS `Error:`, `npm ERR!`, Rust `error[`, `ERROR`/`FATAL` levels). On a match the status turns orange with a `!` badge and the log title shows the count, even while running. Restart or a mark (`space`) clears it. It's one regex per line and only runs when enabled. The web `error badge` checkbox toggles it and persists the choice.

## TUI controls

The footer stays minimal (`? help • q quit`); press `?` for the full overlay.

| Key | Action |
|-----|--------|
| `↑`/`↓` or `k`/`j` | Select a process |
| `Shift+↑`/`Shift+↓` | Move the selected process in the list (saved to YAML) |
| `Enter` | Start the process (▶ standalone task: run once) |
| `s` | Stop |
| `r` | Restart |
| `f` | Free the configured `port` for the selected process |
| `Space` | Insert a timestamped mark in the selected log |
| `m` | Mark every running process |
| `t` | Open the one-shot task picker (`1`–`9` to run) |
| `W` | Toggle word wrap |
| `c` | Cycle the process's color dot (saved to YAML) |
| `w` | Toggle the web log viewer |
| `?` | Help overlay |
| wheel / `PgUp` / `PgDn` | Scroll logs |
| drag / release | Select lines / copy (or `Ctrl+C` while selected) |
| `G` or `End` | Jump to the bottom |
| `Esc` | Clear the selection |
| `q` | Quit |

### Copy on macOS / iTerm2

Stacker prefers the native clipboard (`pbcopy` on macOS), falling back to OSC 52. In **iTerm2**, if copy fails over OSC 52 (e.g. over SSH), enable **Settings → General → Selection → Applications in terminal may access clipboard**. A successful copy shows `Copied N line(s)` in the footer.

## Web log viewer

Press `w` in the TUI to toggle a browser UI on `127.0.0.1` with a random high port (off by default, no restart needed). Turning it on copies the URL and opens your default browser; press `w` again to shut it down.

The page has a sidebar of processes (drag to reorder) and standalone tasks (`▶`), and per-process:

- live, auto-refreshing logs with a **Copy all** button and **word-wrap** toggle;
- **Start / Stop / Restart**, plus **Free port** when the process has one;
- a **More ▾** menu: marks, the **error badge** toggle, a **color selector**, and buttons to run that process's tasks;
- freezing with `Space` to select text without the log moving.

It's read-and-control only over loopback — no auth, meant for local dev.

## CLI (for scripts and AI agents)

Start the TUI once per project config. While it runs, drive it through the CLI instead of launching services yourself:

```bash
stacker -config stacker.yml     # start the TUI + local control plane
stacker ping                    # exit 0 if an instance runs for this config
stacker list --json             # process names, status, ports
stacker status backend --json   # one process
stacker start backend
stacker stop backend
stacker restart backend
stacker free-port 8000          # works even without a running TUI
stacker tasks                   # list one-shot tasks per process
stacker run backend migrate     # run a task; output goes to the process log
stacker version                 # -v / --version also work
```

Only one Stacker TUI is allowed per absolute config path; the control plane listens on `127.0.0.1` and writes a state file under `$XDG_RUNTIME_DIR/stacker/` (or the user cache dir). Agents should:

1. `stacker ping` (or `list`) for the project `stacker.yml`;
2. if running, `restart` / `start` / `stop` / `run` via the CLI;
3. never start the same service with `mise run …` or `go run …` in parallel.

## Agent skills (Claude Code, Codex, OpenCode, Cursor, Grok, …)

Stacker ships an [Agent Skills](https://agentskills.io/specification)-compatible skill so coding agents use the CLI instead of starting services in parallel.

Canonical source: `skills/stacker/SKILL.md`

```bash
# Detect which AI agents exist on this machine, then install the skill
mise run skills:list
mise run skills:install

# Or call the script directly
python scripts/install_skills.py --list
python scripts/install_skills.py              # detected tools only
python scripts/install_skills.py --all        # every known path
python scripts/install_skills.py --dry-run
```

| Tool | Project path | Global path |
|------|--------------|-------------|
| Claude Code | `.claude/skills/stacker/` | `~/.claude/skills/stacker/` |
| OpenAI Codex | `.codex/skills/stacker/` | `~/.codex/skills/stacker/` |
| OpenCode | `.opencode/skills/stacker/` | `~/.config/opencode/skills/stacker/` |
| Cursor | `.cursor/skills/stacker/` | `~/.cursor/skills/stacker/` |
| Grok | `.grok/skills/stacker/` | `~/.grok/skills/stacker/` |
| Generic | `.agents/skills/stacker/` | `~/.agents/skills/stacker/` |

See `skills/README.md` for details. `AGENTS.md` is the canonical YAML contract for agents editing `stacker.yml`.

## Building from source

```bash
mise install          # pin Go and other tools
mise run dev          # run against ./stacker.yml (demo process autostarts)
mise run build        # build ./bin/stacker
mise run test         # go test ./...
mise run install      # go install into $GOBIN or $(go env GOPATH)/bin
```

The bundled `stacker.yml` has a self-contained `demo` process so you can try log handling without configuring a project.

## Releases

`.github/workflows/release.yml` publishes releases for SemVer tags. It runs tests and `go vet`, builds CGO-disabled binaries for Linux and macOS on AMD64 and ARM64, generates `checksums.txt`, builds release notes from the commit messages since the previous tag, and attaches all artifacts to the GitHub Release.

```bash
git tag v0.1.0
git push origin v0.1.0
```

You can also trigger the workflow manually from the Actions tab with a tag. The pipeline creates no commits and does not modify `main`.

## Current limitations

- Process-group signaling is best-effort on Windows (`taskkill /T`); Unix uses process groups via `setpgid`.
- Selection operates on whole lines, not individual columns.
- Clipboard uses `pbcopy` / `wl-copy` / `xclip` when available, otherwise OSC 52 (the terminal must allow it).
- No process health checks or inter-process dependencies yet.
- The web viewer is unauthenticated loopback-only, intended for local development.
