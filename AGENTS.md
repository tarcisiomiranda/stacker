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

- `command`: required, non-empty string executed with `/bin/sh -c`. It inherits the environment, including `PATH`, from the user who launched Stacker. Prefer a project task such as `mise run back:dev` instead of duplicating a long command.
- `cwd`: optional directory. Relative paths are resolved from the directory containing `stacker.yml`. The directory must already exist. Omission means `.`.
- `autostart`: optional boolean. Omission means `false`.
- `graceful_timeout`: optional positive Go duration such as `500ms`, `8s`, `2m`, or `1m30s`. Omission means `8s`.
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
5. Keep `version: 1` and remove all undocumented fields.
6. Run `mise run test` and `mise run build` after changing application code. For configuration-only changes, launch `./bin/stacker -config stacker.yml` and confirm the processes start and stop correctly.

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
