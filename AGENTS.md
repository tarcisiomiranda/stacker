# Instructions for AI agents

## Editing `stacker.yml`

Use this document as the canonical contract whenever creating or changing `stacker.yml`. The application rejects unknown fields, unsupported versions, empty commands, invalid timeouts, and working directories that do not exist.

### Required structure

```yaml
version: 1

ui:
  wheel_lines: 3
  copy_on_release: true
  max_log_lines: 10000
  word_wrap: false
  highlight_errors: false

processes:
  process-name:
    command: mise run task-name
    cwd: ./relative/directory
    autostart: false
    graceful_timeout: 8s
    port: 8000
    color: "#0af"
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
- `word_wrap`: boolean. Initial word-wrap state for log lines in both the TUI and the web viewer. Either can toggle it at runtime (TUI key `W`, web `wrap` checkbox); the toggle is not written back to the file. Omission means `false` (long lines are truncated in the TUI, horizontally scrolled in the web viewer).
- `highlight_errors`: boolean. When `true`, each captured output line is matched against built-in error patterns (Python tracebacks/exceptions, Go `panic:`/`fatal error:`, JS/TS `Error:`, `npm ERR!`, Rust `error[`, `ERROR`/`FATAL`/`CRITICAL` levels). A match turns the process status orange with a `!` badge in the TUI list and the web sidebar even while the process keeps running, and the log title shows the count. The badge clears on restart or when the user inserts a mark (`space`/`m`, or the web Mark buttons). Cost is one regex match per log line, so it is safe to enable on modest machines; omission means `false` (no matching at all). The web viewer's `error badge` checkbox toggles this at runtime and rewrites the value in this file.
- Do not add fields other than `wheel_lines`, `copy_on_release`, `max_log_lines`, `word_wrap`, and `highlight_errors`.

### Process fields

Each key below `processes` is the process name displayed in the TUI. Names must be non-empty and unique. **The key order is the display order** in the TUI list and web sidebar; the user can reorder at runtime (TUI `shift+↑/↓`, web drag-and-drop), which rewrites this file, so do not assume alphabetical order and do not "tidy" the process order when editing.

- `command`: required, non-empty string executed with `/bin/sh -c` (Unix) or `cmd /C` (Windows). It inherits the environment, including `PATH`, from the user who launched Stacker. Prefer a project task such as `mise run back:dev` instead of duplicating a long command.
- `cwd`: optional directory. Relative paths are resolved from the directory containing `stacker.yml`. The directory must already exist. Omission means `.`.
- `autostart`: optional boolean. Omission means `false`. When `false`, the process is registered in the TUI but does not start until the user (or CLI) starts it.
- `graceful_timeout`: optional positive Go duration such as `500ms`, `8s`, `2m`, or `1m30s`. Omission means `8s`.
- `port`: optional TCP port (`1`–`65535`). When set, Stacker frees that port (terminates listeners) before every start/restart so a stray process left by an IDE/AI agent does not block the bind. Omission means no automatic free-port.
- `color`: optional visual group marker rendered as a colored dot next to the process name in the TUI list and the web sidebar. Hex (`"#0af"`, `"#00aaff"`, quoted — `#` starts a YAML comment) or a CSS color name (`red`). Purely cosmetic; omission renders no dot. The running app can rewrite this field (TUI key `c` cycles a preset palette; the web viewer has a color selector); both persist the change to this YAML file preserving comments, so do not assume the file is static while Stacker runs.
- `tasks`: optional map of `name: command` one-shot commands (migrations, seeds, cache clears) **scoped to this process** — run on demand in the process's `cwd` with its inherited environment. Output streams into the process's log prefixed `[task <name>]`; the run does **not** change the process status, so a long-running `--reload` server keeps serving. Names and commands must be non-empty. Trigger a task from the TUI (`t` opens a picker), the web viewer (More ▾ → Tasks), or the CLI (`stacker run <process> <task>`). Use these for "run once, process, exit" commands tied to one service, instead of adding a second always-on process.
- Do not add any other process fields.

### Standalone tasks (root `tasks:`)

Root-level `tasks:` are one-shot commands **not tied to any process** — for commands that belong to no single service or span several (deploys, backups, ad-hoc scripts). Each becomes its own entry in the TUI list and web sidebar (marked `▶`), with its own log. Running one (TUI `enter`, web `▶ Run`, or `stacker start <task>`) executes the command once; a clean exit shows `idle`, not a crashed service. This is distinct from per-process `tasks:` (nested under a process, which stream into that process's log).

```yaml
tasks:
  deploy:
    command: ./deploy.sh
    cwd: ./infra
  backup-db:
    command: pg_dump app > backup.sql
```

- Each standalone task has `command` (required, non-empty), optional `cwd` (resolved from the config dir, must exist), and optional `color`.
- A standalone task name must not clash with a process name.
- Standalone tasks are pinned after the processes in the list; they are excluded from reorder (TUI `shift+↑/↓`, web drag) and their color is not runtime-editable.

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
2. Confirm that every configured `cwd` exists.
3. Add one process entry per independently managed long-running service.
4. Enable `autostart` only for services that should start whenever Stacker opens.
5. Set `port` when the service binds a fixed TCP port so start/restart can reclaim it.
6. Keep `version: 1` and remove all undocumented fields.
7. Run `mise run test` and `mise run build` after changing application code. For configuration-only changes, launch `./bin/stacker -config stacker.yml` and confirm the processes start and stop correctly.

### CLI (control plane)

While the TUI is running for a given config, AI agents and scripts should use the CLI instead of starting services in parallel:

```bash
stacker -config stacker.yml              # start TUI + control plane
stacker ping                             # is an instance running?
stacker list --json                      # process names and status
stacker start backend
stacker stop backend
stacker restart backend
stacker free-port 8000                   # works even without the TUI
stacker tasks                            # list one-shot tasks per process
stacker run backend migrate              # run a one-shot task
stacker version                          # print binary version (-v, --version)
```

Only one TUI instance is allowed per absolute config path. The control plane listens on `127.0.0.1` and writes a state file under `$XDG_RUNTIME_DIR/stacker/` (or the user cache dir).

Pressing `w` in the TUI toggles a separate web log viewer on `127.0.0.1` with a random high port (off by default, no restart needed). The log page header shows only the main actions (Start, Stop, Restart, and Free port when the process has `port:` set); everything else (copy, marks, auto-refresh, wrap, error badge, color selector, raw link) lives in the `More ▾` menu. Routes: `GET /` (process index), `GET /logs/{name}` (HTML page), `GET /logs/{name}/raw` (plain text), `GET /api/{name}/tail?from=N` (incremental logs + all process statuses, colors, and error counts; `nolines=1` for statuses only), `POST /api/{name}/{start|stop|restart}`, `POST /api/{name}/free-port` (kill listeners on the configured port; 400 when the process has no `port:`), `POST /api/{name}/task` with body `{"name": "migrate"}` (run a one-shot task; output streams into the log; 404 for an unknown task), `POST /api/{name}/mark` (append a timestamped separator to the logs), `POST /api/{name}/color` with body `{"color": "#38bdf8"}` (set the process dot color and rewrite it in `stacker.yml`; empty string removes it), `POST /api/order` with body `{"names": [...]}` (reorder the process list — must be a permutation of all names; rewrites the mapping order in `stacker.yml` and the TUI follows), `POST /api/highlight-errors` with body `{"enabled": true}` (toggle error detection and persist `ui.highlight_errors`), and `POST /api/mark-all` (separator on every running process). Turning it on copies the URL of the selected process's log page and opens it in the default browser; pressing `w` again shuts it down. In the TUI, `space` appends the same separator to the selected process's logs, `m` marks every running process, `t` opens the one-shot task picker, `W` toggles word wrap, `c` cycles the selected process's color, `shift+↑/↓` moves the selected process in the list (saved to the YAML), and `?` opens the key help overlay.

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
