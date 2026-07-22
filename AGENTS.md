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

processes:
  process-name:
    command: mise run task-name
    cwd: ./relative/directory
    autostart: false
    graceful_timeout: 8s
    port: 8000
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
- Do not add fields other than `wheel_lines`, `copy_on_release`, and `max_log_lines`.

### Process fields

Each key below `processes` is the process name displayed in the TUI. Names must be non-empty and unique.

- `command`: required, non-empty string executed with `/bin/sh -c` (Unix) or `cmd /C` (Windows). It inherits the environment, including `PATH`, from the user who launched Stacker. Prefer a project task such as `mise run back:dev` instead of duplicating a long command.
- `cwd`: optional directory. Relative paths are resolved from the directory containing `stacker.yml`. The directory must already exist. Omission means `.`.
- `autostart`: optional boolean. Omission means `false`. When `false`, the process is registered in the TUI but does not start until the user (or CLI) starts it.
- `graceful_timeout`: optional positive Go duration such as `500ms`, `8s`, `2m`, or `1m30s`. Omission means `8s`.
- `port`: optional TCP port (`1`–`65535`). When set, Stacker frees that port (terminates listeners) before every start/restart so a stray process left by an IDE/AI agent does not block the bind. Omission means no automatic free-port.
- Do not add any other process fields.

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
```

Only one TUI instance is allowed per absolute config path. The control plane listens on `127.0.0.1` and writes a state file under `$XDG_RUNTIME_DIR/stacker/` (or the user cache dir).

Pressing `w` in the TUI toggles a separate web log viewer on `127.0.0.1` with a random high port (off by default, no restart needed): `GET /` (process index), `GET /logs/{name}` (HTML page with copy button and auto-refresh), `GET /logs/{name}/raw` (plain text), `POST /api/{name}/restart`, and `POST /api/{name}/mark` (append a timestamped separator to the logs). Turning it on copies the URL of the selected process's log page and opens it in the default browser; pressing `w` again shuts it down. In the TUI, `space` appends the same separator to the selected process's logs.

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
