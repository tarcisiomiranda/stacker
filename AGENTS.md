# Instructions for AI agents

## Editing `stacker.yml`

Use this document as the canonical contract whenever creating or changing `stacker.yml`. The application rejects unknown fields, unsupported versions, empty commands, and invalid timeouts. A working directory that does not exist is not a config error: only that entry is disabled (see `cwd` below).

### Required structure

```yaml
version: 1

ui:
  wheel_lines: 3
  copy_on_release: true
  max_log_lines: 10000
  max_log_bytes: 256MB
  word_wrap: false
  highlight_errors: false
  web_host: "0.0.0.0"
  web_port: 52911

processes:
  process-name:
    command: mise run task-name
    cwd: ./relative/directory
    autostart: false
    graceful_timeout: 8s
    port: 8000
    color: "#0af"
    group: application
    tasks:
      migrate: mise run migrate
      seed: python manage.py seed
```

### Root fields

- `version` is required and must be the integer `1`.
- `ui` is optional. Use only the fields documented below.
- `processes` is required and must contain at least one process.
- Do not add any other root fields.

### UI fields

- `wheel_lines`: non-negative integer. Zero or omission uses the default of `3`.
- `copy_on_release`: boolean. When `true`, releasing a mouse selection copies it through OSC 52.
- `max_log_lines`: non-negative integer per process. Zero or omission uses the default of `10000`.
- `max_log_bytes`: memory budget for **each** process's retained log. Accepts a plain byte count (`1048576`) or a human size (`256MB`, `512kb`, `1G`, binary units). Zero or omission uses the default of `256MB`; values below `4KB` are rejected. Both caps apply and the tighter one wins: appending a line drops the oldest lines until the log fits the line count *and* the byte budget. The newest line is always kept, even when it alone exceeds the budget. Raise this before raising `max_log_lines` a lot, since the line cap alone does not bound memory when a service emits huge lines.
- `word_wrap`: boolean. Initial word-wrap state for log lines in both the TUI and the web viewer. Either can toggle it at runtime (TUI key `W`, web `wrap` checkbox); the toggle is not written back to the file. Omission means `false` (long lines are truncated in the TUI, horizontally scrolled in the web viewer).
- `highlight_errors`: boolean. When `true`, each captured output line is matched against built-in error patterns (Python tracebacks/exceptions, Go `panic:`/`fatal error:`, JS/TS `Error:`, `npm ERR!`, Rust `error[`, `ERROR`/`FATAL`/`CRITICAL` levels). A match turns the process status orange with a `!` badge in the TUI list and the web sidebar even while the process keeps running, and the log title shows the count. The badge clears on restart or when the user inserts a mark (`space`/`m`, or the web Mark buttons). Cost is one regex match per log line, so it is safe to enable on modest machines; omission means `false` (no matching at all). The web viewer's `error badge` checkbox toggles this at runtime and rewrites the value in this file.
- `web_host`: optional bind address for the on-demand web log viewer (TUI key `w`). Omission means `0.0.0.0` so a Stacker on a remote server is reachable from other hosts. Use `"127.0.0.1"` to keep it local-only. Quote the value if it starts with digits only is not needed; prefer a plain string. Hostnames and IPv4/IPv6 addresses are accepted. With a wildcard bind, the announced URL is not `0.0.0.0` and is never the machine hostname (those routinely fail to resolve): over SSH it is the server address the client connected to (`SSH_CONNECTION`), on a local desktop it is `127.0.0.1`, and on a headless daemon it is the default-route address. Setting an explicit value here always wins.
- `web_port`: optional TCP port (`1`–`65535`) for the web viewer. Omission or `0` means `52911`. If that port is busy, Stacker falls back to an ephemeral free port on the same host and shows the actual URL in the status line.
- Do not add fields other than `wheel_lines`, `copy_on_release`, `max_log_lines`, `max_log_bytes`, `word_wrap`, `highlight_errors`, `web_host`, and `web_port`.

### Process fields

Each key below `processes` is the process name displayed in the TUI. Names must be non-empty and unique. **The key order is the display order** in the TUI list and web sidebar; the user can reorder at runtime (TUI `shift+↑/↓`, web drag-and-drop), which rewrites this file, so do not assume alphabetical order and do not "tidy" the process order when editing.

- `command`: required, non-empty string executed with `/bin/sh -c` (Unix) or `cmd /C` (Windows). It inherits the environment, including `PATH`, from the user who launched Stacker. Prefer a project task such as `mise run back:dev` instead of duplicating a long command.
- `cwd`: optional directory. Relative paths are resolved from the directory containing `stacker.yml`. Omission means `.`. A `cwd` that does not exist (or is not a usable directory) does **not** fail the config: that entry alone loads with status `disabled` and the reason in its log, while every other process still works — a shared `stacker.yml` covering repos that only some machines have cloned is a supported setup. A disabled entry never autostarts and never frees its `port`. Starting it re-checks the directory, so cloning the repo and pressing start is enough; a YAML edit (hot reload) also re-evaluates it.
- `autostart`: optional boolean. Omission means `false`. When `false`, the process is registered in the TUI but does not start until the user (or CLI) starts it.
- `graceful_timeout`: optional positive Go duration such as `500ms`, `8s`, `2m`, or `1m30s`. Omission means `8s`.
- `port`: optional TCP port (`1`–`65535`). When set, Stacker frees that port (terminates listeners) before every start/restart so a stray process left by an IDE/AI agent does not block the bind. Omission means no automatic free-port.
- `color`: optional visual group marker rendered as a colored dot next to the process name in the TUI list and the web sidebar. Hex (`"#0af"`, `"#00aaff"`, quoted — `#` starts a YAML comment) or a CSS color name (`red`). Purely cosmetic; omission renders no dot. The running app can rewrite this field (TUI key `c` cycles a preset palette; the web viewer has a color selector); both persist the change to this YAML file preserving comments, so do not assume the file is static while Stacker runs.
- `group`: optional trimmed label that groups this process with other services and standalone tasks. Omission or an empty value places it in the implicit `Other` section.
- `tasks`: optional map of `name: command` one-shot commands (migrations, seeds, cache clears) **scoped to this process** — run on demand in the process's `cwd` with its inherited environment. Output streams into the process's log prefixed `[task <name>]`; the run does **not** change the process status, so a long-running `--reload` server keeps serving. Names and commands must be non-empty. Trigger a task from the TUI (`t` opens a picker), the web viewer (More ▾ → Tasks), or the CLI (`stacker run <process> <task>`). Use these for "run once, process, exit" commands tied to one service, instead of adding a second always-on process.
- Do not add process fields other than `command`, `cwd`, `autostart`, `graceful_timeout`, `port`, `color`, `group`, and `tasks`.

### Standalone tasks (root `tasks:`)

Root-level `tasks:` are one-shot commands **not tied to any process** — for commands that belong to no single service or span several (deploys, backups, ad-hoc scripts). Each becomes its own entry in the TUI list and web sidebar (marked `▶`), with its own log. Running one (TUI `enter`, web `▶ Run`, or `stacker start <task>`) executes the command once; a clean exit shows `idle`, not a crashed service. This is distinct from per-process `tasks:` (nested under a process, which stream into that process's log).

```yaml
tasks:
  deploy:
    command: ./deploy.sh
    cwd: ./infra
    group: operations
  backup-db:
    command: pg_dump app > backup.sql
```

- Each standalone task has `command` (required, non-empty), optional `cwd` (resolved from the config dir; a missing one disables just this task, same as a process), optional `color`, and optional `group`.
- A standalone task name must not clash with a process name.
- Standalone tasks appear after services in each section; task-only sections follow sections containing services, and `Other` remains last. Task key order is preserved within each section. TUI `shift+↑/↓` reorders a task within its same-kind section, while web drag-and-drop can reorder it or move it between sections. Their color is not runtime-editable.
- Do not add standalone-task fields other than `command`, `cwd`, `color`, and `group`.

### Process groups

Group names are trimmed labels shared by process entries and root-level standalone tasks. Entries with no group share the implicit `Other` section. Sections retain first-appearance order within two tiers: sections containing services first, then task-only sections; `Other` is always last. Within a section, services appear before standalone tasks, preserving their order from their respective YAML mappings. The `Other` header is hidden only when every entry is ungrouped; an explicit `group: Other` makes the section visible and shares it with ungrouped entries.

In the session and attach TUIs, `←`/`h` folds the selected or enclosing section and `→`/`l` unfolds it; clicking a header toggles it. Fold state persists per config in Stacker's user cache. The web sidebar folds by section and remembers folds in browser local storage for that config.

On a TUI section header, `Enter` starts, `s` stops, `r` restarts, and `Space` marks the section. Start/stop/restart apply to service members; marking applies to active services and standalone tasks. The web sidebar offers group Start/Stop/Restart buttons. Session TUI `g` opens a group picker with the current group or `none` highlighted; `↑/k` and `↓/j` navigate all choices, `enter` selects, `1`–`9` are shortcuts for the first nine groups, and `0` removes the group. The picker cannot create group names; web drag-and-drop can reorder entries or move them between sections. Session TUI section reordering stays within the service-bearing or task-only tier, keeps `Other` last, and member reordering stays within its same-kind section. Group and order edits are saved to `stacker.yml`.

Group assignment routes are `POST /v1/processes/{name}/group` and `POST /api/{name}/group`, each with `{"group":"name"}` (an empty string removes the group). Group actions use `POST /v1/groups/{start|stop|restart|mark}` on the control plane and `POST /api/groups/{start|stop|restart}` in the web viewer, with `{"group":"name"}`; responses list affected members in `affected`.

The web group-action route is selected for body-bearing `POST /api/groups/{action}` requests. A configured process named `groups` retains bodyless actions, `GET /api/groups/tail`, and its body-bearing `group`, `color`, and `task` actions.

The CLI supports `stacker start --group <name>`, `stacker stop --group <name>`, and `stacker restart --group <name>` (also `-g` and `--group=<name>`). Group mode does not take a process name. `stacker list` includes a `GROUP` column (`-` for unassigned entries), and `list --json` includes `group` in records when assigned. Shell completion provides live group candidates after `--group`/`-g`; `stacker __complete groups` prints those names.

### Command formatting

- Use a plain scalar for short commands: `command: mise run api:dev`.
- Quote commands containing YAML-sensitive characters such as `#`, `{`, `}`, `[`, `]`, `,`, `&`, `*`, `?`, `|`, `>`, `!`, `%`, `@`, or backticks.
- Use a folded block for long shell commands:

  ```yaml
  command: >-
    first-command &&
    second-command
  ```

- Environment variables may be placed before the command: `command: APP_ENV=development mise run api:dev`.
- When `mise` is available in the launching user's `PATH`, use `mise run task-name` directly. Do not hardcode user-specific tool directories and do not run `mise activate` inside a process command.
- Before generating the YAML, verify required executables with commands such as `command -v mise`. If Stacker is launched by a service manager, configure its `PATH` in that service rather than embedding a personal path in `stacker.yml`.
- The current implementation targets Linux and macOS because it uses `sh` and Unix process groups.

### Recommended workflow

1. Read `mise.toml` and run `mise tasks` to discover the real task names.
2. Confirm that every configured `cwd` exists — entries pointing at a directory this machine does not have load as `disabled` instead of running.
3. Add one process entry per independently managed long-running service.
4. Enable `autostart` only for services that should start whenever Stacker opens.
5. Set `port` when the service binds a fixed TCP port so start/restart can reclaim it.
6. Keep `version: 1` and remove all undocumented fields.
7. Run `mise run test` and `mise run build` after changing application code. For configuration-only changes, launch `./bin/stacker -config stacker.yml` and confirm the processes start and stop correctly. To exercise a change through the real `stacker` command, `mise run build:install` builds for this machine and replaces the binary PATH resolves (`--dry-run` shows the plan first); supervisors already running keep the old build until restarted.

### CLI (control plane)

While a Stacker instance is running for a given config, AI agents and scripts should use the CLI instead of starting services in parallel:

```bash
stacker -config stacker.yml              # session: TUI + control plane
stacker serve                            # headless supervisor
stacker serve -d                         # daemonize headless supervisor
stacker attach                           # TUI attach to serve (q detaches)
stacker down                             # stop all processes + supervisor
stacker ping                             # is an instance running?
stacker list --json                      # process names and status
stacker logs backend -n 100              # tail one process log (default 200)
stacker logs backend --json              # lines + "next" index to resume from
stacker logs backend --since 1200        # only the lines added after index 1200
stacker logs backend -f                  # follow (humans only; never returns)
stacker logs --supervisor                # the daemon's own log file
stacker completion zsh                   # completion script (bash, zsh, fish)
stacker __complete processes             # names + status, one per line, no jq
stacker start backend
stacker stop backend
stacker restart backend
stacker free-port 8000                   # works even without an instance
stacker tasks                            # list one-shot tasks per process
stacker run backend migrate              # run a one-shot task
stacker version                          # print binary version (-v, --version)
```

**Shell completion.** `stacker completion <bash|zsh|fish>` prints the script; the scripts are embedded in the binary, so they always match its commands. `mise run build:install` installs them for every shell it detects, into directories those shells already load (Homebrew's `site-functions` / `bash_completion.d` when present, `~/.local/share/...` otherwise), never editing an rc file. Candidates are dynamic: the scripts call the hidden `stacker __complete <processes|tasks|commands|flags|shells>`, which prints `value<TAB>description` lines — so `stacker logs <TAB>` lists the live processes with their status, and zsh/fish show that status beside each name. `__complete` prints nothing and exits 0 on any failure, because a TAB press must not error or block. Agents can use `__complete processes` as a jq-free way to list names with status.

**Reading logs.** Process output lives in the supervisor's memory (capped by `ui.max_log_lines` and `ui.max_log_bytes`), not on disk. `stacker logs <name>` is how a script or an agent reads it — the TUI and web viewer are for humans. Each `--json` response carries a `next` index; pass it back as `--since <next>` to get only the lines added since the previous read, which is the incremental pattern to use instead of `-f`. `stacker logs --supervisor` reads the daemon's own log file directly and therefore still works when the supervisor is dead or never started; it holds supervisor notices, not process output. A bare `stacker` with no TTY prints the running instances together with the log command for each, so an agent that lands there has a next step instead of a dead end.

Only one instance is allowed per absolute config path (`session` or `serve`). The control plane listens on `127.0.0.1` and writes a state file under `$XDG_RUNTIME_DIR/stacker/` (or the user cache dir). Edits to `stacker.yml` while running are hot-reloaded (add/remove/reorder); running processes removed from YAML stay until stopped.

### Which instance a command talks to

**Agents must always pass `--config`.** An explicit `--config` is honoured unconditionally: a config that exists on disk is never swapped for another running instance. Without it, resolution is:

| Invocation | Target config running? | Result |
|---|---|---|
| `stacker --config X` | yes | attach to X |
| `stacker --config X` | no | start X (session mode), never another instance |
| `stacker` | `./stacker.yml` running | attach to it |
| `stacker` | not running, other instances alive | interactive picker (no TTY: prints the list, exits 1) |
| `stacker` | not running, nothing alive | start `./stacker.yml` |
| `stacker a --config X` | no | error — attach never starts anything |
| `stacker a` | — | interactive picker |

For subcommands, adopting a different instance depends on whether the command mutates state:

| Class | Commands | Adopts the single live instance? |
|---|---|---|
| read-only | `list`, `status`, `tasks`, `logs` | only when the target config does not exist on disk, with a warning on stderr |
| state-changing | `start`, `stop`, `restart`, `run`, `down` | never — errors and lists the live instances |

`stacker instances` (alias `ins`, supports `--json`) lists every running supervisor with its label, config path, pid, mode, process counts, ports, and port collisions. `stacker ping` always refers to the exact config, never another.

### Port collisions between instances

`free-port` terminates whatever holds the port, including a process belonging to another instance. That still happens — the port is free when the call returns — but both sides get a log line, so the death is auditable: the claiming process logs `[stacker] port 8080 reclaimed from <proc> @ <label> (pid N)`, and the victim receives `[stacker] <proc> terminated: port 8080 reclaimed by <config>` through `POST /v1/processes/{name}/note`. Starting a supervisor while another one declares the same port also prints a `notice:` naming the shared port.

Pressing `w` toggles a separate web log viewer on `0.0.0.0:52911` by default (overridable via `ui.web_host` / `ui.web_port`; off by default, no restart needed). This works both in a session TUI and in an attached TUI, where the key goes through `POST /v1/web` so a headless `serve` can expose the viewer on demand. On headless/SSH sessions Stacker skips `xdg-open` and only copies/shows the URL. The control plane returns the raw listen address rather than a URL, and each client renders it in its own network context — an attached TUI knows the SSH address it was reached through, while a daemonized supervisor only has the environment of whichever session started it. The log page header shows only the main actions (Start, Stop, Restart, and Free port when the process has `port:` set); everything else (copy, marks, auto-refresh, wrap, error badge, color selector, raw link) lives in the `More ▾` menu. Routes: `GET /` (process index), `GET /logs/{name}` (HTML page), `GET /logs/{name}/raw` (plain text), `GET /api/{name}/tail?from=N` (incremental logs + all process statuses, colors, and error counts; `nolines=1` for statuses only), `POST /api/{name}/{start|stop|restart}`, `POST /api/{name}/free-port` (kill listeners on the configured port; 400 when the process has no `port:`), `POST /api/{name}/task` with body `{"name": "migrate"}` (run a one-shot task; output streams into the log; 404 for an unknown task), `POST /api/{name}/mark` (append a timestamped separator to the logs), `POST /api/{name}/color` with body `{"color": "#38bdf8"}` (set the process dot color and rewrite it in `stacker.yml`; empty string removes it), `POST /api/order` with body `{"names": [...]}` (reorder the process list — must be a permutation of all names; rewrites the mapping order in `stacker.yml` and the TUI follows), `POST /api/highlight-errors` with body `{"enabled": true}` (toggle error detection and persist `ui.highlight_errors`), and `POST /api/mark-all` (separator on every running process). On the control plane, `GET /v1/web` reports `{"enabled": bool, "addr": "host:port"}` and `POST /v1/web` sets it with body `{"enabled": bool}` or toggles when the body is omitted. `POST /v1/processes/{name}/note` with body `{"text": "..."}` appends one `[stacker]`-prefixed line to that process's log; it is how one instance explains itself in another's log. `ProcessInfo` carries `pid` while a process is alive. Turning it on copies the URL of the selected process's log page and opens it in the default browser; pressing `w` again shuts it down. In the TUI, `space` appends the same separator to the selected process's logs, `m` marks every running process, `t` opens the one-shot task picker, `W` toggles word wrap, `c` cycles the selected process's color, `shift+↑/↓` moves the selected process in the list (saved to the YAML), and `?` opens the key help overlay.

### Agent skills (multi-tool)

Canonical skill: `skills/stacker/SKILL.md` (Agent Skills / `SKILL.md` standard).

Detect AIs on the machine and install into their skill dirs:

```bash
python scripts/install_skills.py --list   # what is installed on this PC
python scripts/install_skills.py          # install for detected tools
python scripts/install_skills.py --all    # every known tool path
mise run skills:install
```

When editing the skill body, change `skills/stacker/SKILL.md` only, then re-run the installer so tool-specific copies stay in sync.

### Complete example

```yaml
version: 1

ui:
  wheel_lines: 3
  copy_on_release: true
  max_log_lines: 10000

processes:
  demo:
    command: >-
      i=1; while true; do echo "demo log $i - $(date +%T)";
      i=$((i+1)); sleep 1; done
    cwd: .
    autostart: true
    graceful_timeout: 3s
```

Do not invent process names, task names, directories, or commands. Derive them from files that exist in the repository.
