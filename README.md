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
- **CLI control plane** — `list`/`logs`/`start`/`stop`/`restart`/`run` a running instance from scripts and agents, instead of spawning services in parallel.
- **Logs over a pipe** — `stacker logs <name>` tails a process without a TUI, with `--since` for incremental reads and `--supervisor` for the daemon's own log.
- **Shell completion** — bash, zsh and fish; `stacker logs <TAB>` lists the live processes with their status, so finding a log needs no memory.
- **Serve + attach** — `stacker serve -d` runs headless in the background; `stacker attach` (or plain `stacker`) opens the TUI; `q` detaches without killing services; `stacker down` shuts everything down.
- **Instance picker** — with several projects supervised at once, a bare `stacker` lists what is running (label, config path, ports, port collisions) and lets you pick, start the local config, or stop one. An explicit `--config` is always honoured and never swapped for another instance.
- **Live YAML reload** — add, remove, or reorder processes in `stacker.yml` while Stacker is running; no restart required.
- **On-demand web viewer** — press `w` for a browser UI on `0.0.0.0:52911` by default (reachable from other machines; override with `ui.web_host` / `ui.web_port`), in a session TUI or attached to a headless `serve`. The announced URL targets whoever holds the browser: the SSH address you connected through, or loopback on a local desktop. On SSH/headless hosts the browser is not launched; the URL is copied/shown instead.
- **One-shot tasks** — named commands (migrations, seeds, deploys) that run once and exit, either scoped to a process or standalone.
- **Process groups** — group services and standalone tasks into ordered, foldable sections with group actions in the TUI, web viewer, and CLI.
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

**Agent skills are off by default.** To also detect AI tools on the machine (Claude Code, Codex, OpenCode, Cursor, Grok, Kiro, Hermes, …) and install the Stacker `SKILL.md` for them:

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
    group: application
    autostart: true
  frontend:
    command: mise run front:dev
    port: 3000
    group: application
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
  max_log_lines: 10000    # per-process log line cap
  max_log_bytes: 256MB    # per-process log memory cap
  word_wrap: false        # initial wrap state (toggle at runtime with W / web checkbox)
  highlight_errors: false # opt-in orange badge on error-looking output

processes:
  backend:
    command: mise run back:dev
    cwd: .                 # relative to this file; missing → this entry is disabled
    autostart: false       # registered but not started until Enter/CLI
    graceful_timeout: 8s   # SIGTERM grace before SIGKILL
    port: 8000             # freed before every start/restart
    color: "#38bdf8"       # dot for visual grouping (quote hex — # starts a comment)
    group: application
    tasks:                 # per-process one-shot commands (stream into this log)
      migrate: mise run migrate
      seed: python manage.py seed

# Standalone one-shot tasks: their own entry (▶) with their own log, tied to
# no single process. Run once and return to "idle".
tasks:
  deploy:
    command: ./deploy.sh
    cwd: ./infra
    group: operations
  backup-db:
    command: pg_dump app > backup.sql
```

Unknown YAML fields, empty commands, invalid timeouts/ports/colors, and duplicate names are rejected before the TUI opens. A missing `cwd` is the exception: it disables that one entry (see below) instead of blocking the whole file. Each command inherits the environment (including `PATH`) from the user who started Stacker and runs through `/bin/sh -c`; if `mise` already works in your terminal, `command: mise run task-name` works with no extra setup.

### `ui` fields

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `wheel_lines` | int ≥ 0 | `3` | Lines scrolled per mouse-wheel notch. |
| `copy_on_release` | bool | `false` | Copy the selection through OSC 52 when the mouse button is released. |
| `max_log_lines` | int ≥ 0 | `10000` | Per-process retained log lines. |
| `max_log_bytes` | size ≥ 4KB | `256MB` | Per-process log memory budget: `256MB`, `512kb`, or a byte count. Both caps apply; the tighter one trims. |
| `word_wrap` | bool | `false` | Initial log wrap state; toggle at runtime (`W`, or the web `wrap` box). |
| `highlight_errors` | bool | `false` | Match each line against error patterns and badge the process. |

### `processes` fields

Each key under `processes:` is a service name (non-empty, unique). **Key order is the display order** in the TUI and web sidebar.

| Field | Required | Meaning |
|-------|----------|---------|
| `command` | yes | Shell command run with `/bin/sh -c`. Prefer a project task like `mise run back:dev`. |
| `cwd` | no | Working directory, relative to the config file. Defaults to `.`. Missing on this machine → the entry loads `disabled` (see below). |
| `autostart` | no | Start when Stacker opens. Defaults to `false`. |
| `graceful_timeout` | no | Go duration (`500ms`, `8s`, `1m30s`) to wait after SIGTERM before SIGKILL. Defaults to `8s`. |
| `port` | no | TCP port (1–65535) freed before every start/restart. |
| `color` | no | Hex (`"#0af"`, quoted) or CSS name; draws a colored dot. Editable at runtime. |
| `group` | no | Trimmed label shared with services and standalone tasks. Missing or empty entries are grouped under `Other`. |
| `tasks` | no | Map of `name: command` one-shot commands scoped to this process (see below). |

### Tasks

Two kinds of one-shot commands — for the "run once, process, exit" work you'd
otherwise type in a second terminal:

- **Per-process tasks** (`tasks:` nested under a process) run in that process's `cwd`, stream into its log prefixed `[task <name>]`, and **do not** change its status — the `--reload` server keeps serving. Trigger from the TUI (`t`), web (More ▾ → Tasks), or `stacker run <process> <task>`.
- **Standalone tasks** (root-level `tasks:`) are their own list entry (marked `▶`) with their own log, tied to no process. Fields: `command` (required), optional `cwd`, `color`, and `group`. Running one (TUI `Enter`, web `▶ Run`, or `stacker start <task>`) executes it and returns to `idle` — a clean exit is not a failure. Names must not clash with process names. They appear after services within each section, and task-only sections follow sections containing services; task key order is preserved within each section.

Standalone task fields:

| Field | Required | Meaning |
|-------|----------|---------|
| `command` | yes | Shell command to run once. |
| `cwd` | no | Working directory relative to the config file; missing on this machine disables only this task. |
| `color` | no | Hex or CSS name for the sidebar dot; not editable at runtime. |
| `group` | no | Trimmed label shared with service groups; missing or empty entries use `Other`. |

### Runtime behavior worth knowing

- **Missing `cwd` disables one entry, not the file.** A `stacker.yml` shared across a team usually lists more repos than any single machine has cloned. Entries whose `cwd` is absent load with status `disabled` (dimmed, with the reason as the first log line) and are inert: no autostart, no free-port, and `start` fails with the reason instead of a bind error. Starting one re-checks the directory, so after `git clone` you just press start — no restart of Stacker, and a YAML edit re-evaluates it too. If the directory disappears while the service is up, the service keeps running and is only marked disabled once stopped.
- **Free-port** targets the listener's whole process group, so supervisor trees (`npm → node`, `mise → uvicorn`) go down together instead of respawning the server; it retries a few rounds before reporting the port as still busy.
- **Color** changes (TUI `c`, web selector) and **order** changes (TUI `Shift+↑/↓`, web drag) are written back to `stacker.yml`, preserving comments and formatting — so the file is not static while Stacker runs.
- **Groups** use trimmed `group:` labels shared by services and standalone tasks. Sections retain first-appearance order among service-bearing groups, followed by task-only groups; `Other` is always last. Within a section, services precede standalone tasks and each YAML mapping's key order is preserved. Missing or empty groups share `Other`; its header is hidden only when every entry is ungrouped, while an explicit `group: Other` makes the shared section visible.
- **Section folds and actions** are available in the session and attach TUIs (`←`/`h` folds, `→`/`l` unfolds, click a header to toggle). Fold state persists per config in Stacker's user cache; the web sidebar remembers folds in browser local storage per config. On a TUI header, `Enter` starts, `s` stops, `r` restarts, and `Space` marks the section. Start/stop/restart affect service members; marking affects active services and standalone tasks. The web sidebar provides group Start/Stop/Restart buttons.
- **Group assignment and reordering** use session TUI `g` to assign or remove a configured member's group (the current group or `none` is highlighted; `↑/k` and `↓/j` navigate all picker choices, `enter` selects, digits `1`–`9` shortcut the first nine groups, and `0` removes the group), or web drag-and-drop to move entries between sections; both update `stacker.yml`. Session TUI `Shift+↑/↓` moves a header within its service-bearing or task-only tier, or a member within the same-kind entries in its section. `Other` stays last.
- **Error highlighting** (`highlight_errors: true`) matches every captured line against built-in patterns (Python tracebacks, Go panics, JS/TS `Error:`, `npm ERR!`, Rust `error[`, `ERROR`/`FATAL` levels). On a match the status turns orange with a `!` badge and the log title shows the count, even while running. Restart or a mark (`space`) clears it. It's one regex per line and only runs when enabled. The web `error badge` checkbox toggles it and persists the choice.

## TUI controls

The footer stays minimal (`? help • q quit`); press `?` for the full overlay.

| Key | Action |
|-----|--------|
| `↑`/`↓` or `k`/`j` | Select a section header or member |
| `←`/`h` / `→`/`l` | Fold / unfold the selected or enclosing section |
| `Shift+↑`/`Shift+↓` | In the session TUI, move a section within its tier or a member within its same-kind section entries (saved to YAML) |
| `g` | Open the session group picker (`↑/k` / `↓/j`, `enter`, `1`–`9`, `0`) for a configured member (saved to YAML) |
| `Enter` | Start the selected process, or start service members when on a section header (▶ standalone task: run once) |
| `s` | Stop |
| `r` | Restart |
| `f` | Free the configured `port` for the selected process |
| `Space` | Insert a timestamped mark in the selected log, or mark active section members |
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

Press `w` in the TUI to toggle a browser UI on `0.0.0.0:52911` by default (off by default, no restart needed; override with `ui.web_host` / `ui.web_port`). It works the same in an attached TUI, so a headless `stacker serve` can expose the viewer without being restarted with `--web`. Turning it on copies the URL; on a desktop it also opens your browser, while on SSH/headless hosts it only shows/copies the URL. Press `w` again to shut it down.

Because a wildcard bind is not a destination, Stacker has to pick the host for that URL. It never advertises the machine hostname — `MacBook-Pro-de-x.local` and corporate DHCP names frequently do not resolve, producing a link nobody can open. Instead:

| Where Stacker runs | Announced host |
|---|---|
| Over SSH | The server address from `SSH_CONNECTION` — the one your client reached, so it is routable by construction |
| Local desktop | `127.0.0.1` |
| Headless daemon, no SSH | The default-route address |
| `ui.web_host` set explicitly | Exactly what you configured |

The page has a sidebar of processes (drag to reorder) and standalone tasks (`▶`). When any entry has an explicit `group`, the sidebar displays foldable sections in the same group order as the TUI; missing groups appear under `Other`, and the header is hidden only when every entry is ungrouped. Section headers offer Start, Stop, and Restart for their service members. Fold state is stored in browser local storage per config. Dragging an entry between sections changes its group; reordering and group changes are saved to `stacker.yml`.

The group APIs are `POST /api/{name}/group` with `{"group":"name"}` (send an empty string to remove the assignment; the response returns the updated process in `process`) and `POST /api/groups/{start|stop|restart}` with `{"group":"name"}` (the response includes the group and action plus affected member names in `affected`, including on action errors).

Per-process, the log page provides:

- live, auto-refreshing logs with a **Copy all** button and **word-wrap** toggle;
- **Start / Stop / Restart**, plus **Free port** when the process has one;
- a **More ▾** menu: marks, the **error badge** toggle, a **color selector**, and buttons to run that process's tasks;
- freezing with `Space` to select text without the log moving.

It has **no authentication**, and the default bind is `0.0.0.0` — anyone who can reach the port can read logs and start/stop processes. That is the point on a trusted dev network, but set `ui.web_host: "127.0.0.1"` when the machine is exposed.

## CLI (for scripts and AI agents)

Start the TUI once per project config. While it runs, drive it through the CLI instead of launching services yourself:

```bash
stacker -config stacker.yml     # session mode: TUI + control plane
stacker serve                   # headless supervisor (Ctrl+C / stacker down to stop)
stacker serve -d                # same, daemonized in the background
stacker attach                  # TUI against a running serve instance (q detaches)
stacker down                    # stop all processes and shut down the supervisor
stacker ping                    # exit 0 if an instance runs for this config
stacker list --json             # process names, status, ports, groups
stacker start --group application
stacker stop -g application
stacker restart --group=application
stacker status backend --json   # one process
stacker logs backend            # last 200 lines of that process
stacker logs backend -n 20 -f   # tail and follow (Ctrl+C to stop)
stacker logs backend --json     # lines + "next" index to resume from
stacker logs backend --since 90 # only what was logged after index 90
stacker logs --supervisor       # the daemon's own log, even if it died
stacker completion zsh          # completion script for bash, zsh or fish
stacker start backend
stacker stop backend
stacker restart backend
stacker free-port 8000          # works even without a running instance
stacker tasks                   # list one-shot tasks per process
stacker run backend migrate     # run a task; output goes to the process log
stacker instances               # every running Stacker on this machine
stacker version                 # -v / --version also work
```

Only one Stacker instance is allowed per absolute config path; the control plane listens on `127.0.0.1` and writes a state file under `$XDG_RUNTIME_DIR/stacker/` (or the user cache dir).

### Shell completion

```bash
stacker completion            # per-shell install instructions
stacker completion zsh > "${fpath[1]}/_stacker"
stacker completion fish > ~/.config/fish/completions/stacker.fish
stacker completion bash > ~/.local/share/bash-completion/completions/stacker
```

`mise run build:install` writes all three for the shells it finds, into the
directories they already load — no edit to your rc files.

Completion is dynamic: candidates come from the running supervisor, so

```
$ stacker logs <TAB>
mfe-back    -- running · :3001
tc-back     -- disabled · cwd unavailable
tools-worker -- stopped · :3060
```

zsh and fish show the status next to each name; bash lists the names. It also
completes commands, the flags of the command you are on, `--config` paths, and
`stacker run <process> <TAB>` for that process's tasks. `stacker start --group <TAB>`
and `-g <TAB>` offer the live group names, including `Other` when ungrouped or
explicit `Other` entries exist; `stacker __complete groups` prints the same
candidates. With no supervisor running it completes nothing and stays silent
rather than erroring.

**Where the logs are.** Process output is kept in the supervisor's memory (bounded by `ui.max_log_lines` and `ui.max_log_bytes`), never written to disk. Three ways to read it: the TUI, the web viewer (`w`), and `stacker logs` — the only one that works over a pipe, in a script, or from an AI agent. Each `--json` reply carries a `next` index, so `--since <next>` returns only what was added since the last read; that is the incremental pattern to prefer over `-f`, which never returns. `stacker logs --supervisor` is different: it reads the daemon's own log **file**, so it answers even when the supervisor failed to start, and holds supervisor notices rather than process output. A bare `stacker` with no TTY prints the running instances plus the exact log command for each.

**Session vs serve:** `stacker` (no subcommand) is session mode — the TUI owns the process; `q` stops everything. `stacker serve` is headless; processes keep running until `stacker down` or SIGTERM. `stacker attach` (or plain `stacker` while serve is up) opens a TUI that **detaches** on `q` without killing services.

## Working on several projects at once

Two projects can be supervised simultaneously — instances are keyed by the absolute config path, so only an identical config conflicts. What used to be confusing was *which* instance a command reached; the rules are now explicit.

**An explicit `--config` always wins.** A config that exists on disk is never swapped for another running instance:

| You run | Target config running? | Result |
|---|---|---|
| `stacker --config X` | yes | attach to X |
| `stacker --config X` | no | **start X** — never another instance |
| `stacker` | `./stacker.yml` running | attach to it |
| `stacker` | not running, others alive | **picker** |
| `stacker` | not running, nothing alive | start `./stacker.yml` |
| `stacker a` | — | picker (attach never starts anything) |

The picker (`↑↓` move · `enter` open · `n` start the config here · `d` stop · `r` refresh · `q` quit) shows each instance with its label, config path, mode, how many processes are up, its ports, and a warning when a port is shared. The last row (and `n`) starts `./stacker.yml` when it exists; without a local config those controls are omitted. Without a TTY it prints the same list and exits 1, so scripts and agents never hang on a prompt.

`stacker instances` (alias `ins`, plus `--json`) is the non-interactive version.

For subcommands, whether another instance can be adopted depends on what the command does:

| Class | Commands | Adopts the single live instance? |
|---|---|---|
| read-only | `list`, `status`, `tasks` | only when the target config does not exist on disk, and it warns |
| state-changing | `start`, `stop`, `restart`, `run`, `down` | **never** — errors and lists what is running |

That split exists because a wrong `list` merely misinforms, while a wrong `down` takes another project's stack with it.

### Shared ports

`free-port` terminates whatever holds the port — including a process belonging to another instance. It still does: the port is free when the call returns. What changed is that the loss is no longer silent. The claiming side logs

```
[stacker] port 8080 reclaimed from frontend @ bunker-orchestrator (pid 4510)
```

and the victim's own log receives

```
[stacker] frontend terminated: port 8080 reclaimed by /srv/_cyber_/stacker.yml
```

Starting a supervisor while another one declares the same port also prints a `notice:` naming the shared port, and the picker flags it up front.

**Live config reload:** while any instance runs, edits to `stacker.yml` (add/remove/reorder processes, field changes) are picked up within ~500ms. New processes with `autostart: true` start automatically. Removing a **running** process keeps it listed (⚠) until you stop it, then it disappears. Invalid YAML is rejected and the previous config stays active.

Agents should:

1. `stacker ping` (or `list`) for the project `stacker.yml`;
2. if running, `restart` / `start` / `stop` / `run` via the CLI;
3. never start the same service with `mise run …` or `go run …` in parallel.

## Agent skills (Claude Code, Codex, OpenCode, Cursor, Grok, Kiro, Hermes, BMAD, …)

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
| Kiro | `.kiro/skills/stacker/` | `~/.kiro/skills/stacker/` |
| Hermes Agent | `.hermes/skills/stacker/` | `~/.hermes/skills/stacker/` |
| BMAD | `_bmad/custom/skills/stacker/` (if `_bmad/` exists) | — |
| Generic | `.agents/skills/stacker/` | `~/.agents/skills/stacker/` |

See `skills/README.md` for details. `AGENTS.md` is the canonical YAML contract for agents editing `stacker.yml`.

## Building from source

```bash
mise install          # pin Go and other tools
mise run dev          # run against ./stacker.yml (demo process autostarts)
mise run build        # build ./bin/stacker
mise run test         # go test ./...
mise run build:install # build for this machine and install it into PATH
mise run install      # go install into $GOBIN or $(go env GOPATH)/bin
```

The bundled `stacker.yml` has a self-contained `demo` process so you can try log handling without configuring a project.

**Testing your working tree.** `mise run build:install` (`scripts/install_local.py`) reads `go env` for the real target, builds with the release flags, and **replaces the `stacker` that PATH already resolves** — installing anywhere else would leave `stacker` running the old build. It writes a sibling file and renames it into place, so a supervisor currently executing that binary is undisturbed instead of failing with "text file busy". The version stamp carries `-dirty` while the tree has uncommitted changes, which is how you tell a test build from a release. Options: `--dry-run`, `--dir <path>`, `--skip-build`, or `STACKER_INSTALL_DIR`.

Replacing the binary does not restart anything: supervisors already running keep the previous build until `stacker --config <cfg> down` and a fresh start. The installer lists them for you. To go back to a release, re-run `install.sh`.

## Releases

`.github/workflows/release.yml` publishes releases for SemVer tags. It runs tests and `go vet`, builds CGO-disabled binaries for Linux and macOS on AMD64 and ARM64, generates `checksums.txt`, builds release notes from `releases/<tag>.yaml` (or commit messages), and attaches all artifacts to the GitHub Release.

```bash
# 1. Commit release notes (optional but preferred)
#    releases/v0.11.0.yaml

# 2. Tag the commit and push
git tag -a v0.11.0 -m "v0.11.0"
git push origin main
git push origin v0.11.0
```

If the tag is on GitHub but **Actions never starts** (tag webhook missed), open **Actions → Release → Run workflow**, set the tag (e.g. `v0.11.0`), and run it on `main`. Manual dispatch uses the same `RELEASE_TAG` for binary version stamping and the GitHub Release name — it does not invent a new tag.

The pipeline creates no commits and does not modify `main`.

## Current limitations

- Process-group signaling is best-effort on Windows (`taskkill /T`); Unix uses process groups via `setpgid`.
- Selection operates on whole lines, not individual columns.
- Clipboard uses `pbcopy` / `wl-copy` / `xclip` when available, otherwise OSC 52 (the terminal must allow it).
- No process health checks or inter-process dependencies yet.
- Shared ports across instances are reported, not prevented: the port is still taken (by design), with a log line on both sides.
- The web viewer is unauthenticated and binds `0.0.0.0` by default; use `ui.web_host: "127.0.0.1"` to restrict it to loopback.
