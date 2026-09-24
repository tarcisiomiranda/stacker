---
name: stacker
description: >
  Manage long-running project services through Stacker instead of starting them
  in parallel, and read their logs without opening a TUI. Use when starting,
  stopping, or restarting APIs or frontends; when reading the output of a
  running service or daemon; when a port is already in use; when stacker.yml
  exists; or when the user mentions Stacker, dev processes, process supervisor,
  logs, free-port, or service restart. Compatible with Claude Code, Codex,
  OpenCode, Cursor, Grok, and other Agent Skills clients.
license: MIT
metadata:
  author: stacker
  version: "1.0"
  homepage: https://github.com/tarcisiomiranda/stacker
---

# Stacker — process supervisor for agents

Stacker owns long-running project services (API, frontend, workers) defined in
`stacker.yml`. **Never start the same service with `mise run`, `go run`,
`uvicorn`, `bun run dev`, `npm run dev`, etc. if Stacker is already managing
it** — that causes "address already in use" and duplicate processes.

## 1. Detect Stacker for this project

From the project root (where `stacker.yml` lives, or pass `-config`):

```bash
stacker ping --json
# or
stacker list --json
```

| Result | Meaning | What you do |
|--------|---------|-------------|
| exit 0 / `"running": true` | Supervisor up (session or serve) | Use CLI only (`start` / `stop` / `restart`) |
| exit 1 / not running | No Stacker for this config | Ask the user to open `stacker` or `stacker serve -d`. Optionally `free-port` if a bind is stuck. **Do not** invent a second supervisor. |
| `stacker` not found | Binary missing | Say Stacker is not installed; do not background services unless the user asks. |

Always use the same `-config` path the human uses (default `stacker.yml` in cwd).

## 2. Manage services (preferred)

```bash
stacker list --json
stacker status backend --json
stacker start backend
stacker stop backend
stacker restart backend
```

- Prefer **`restart`** after code changes that need a process bounce.
- Prefer **`start`** only when status is `stopped` or `failed`.
- Prefer **`stop`** when switching projects or releasing a port.
- Prefer **`--json`** so you can parse status reliably.

Process names come from keys under `processes:` in `stacker.yml`; group names come from the configured `group:` labels — never guess either.

## 3. Reading logs (do this instead of opening the TUI)

Process output lives in the supervisor's memory. `stacker logs` is the only
sane way for an agent to read it — the TUI and the web viewer are for humans.

| You want | Command |
|---|---|
| What a service just printed | `stacker --config X logs back -n 100` |
| Machine-readable + a resume point | `stacker --config X logs back --json` |
| Only what is new since last read | `stacker --config X logs back --since <next> --json` |
| Everything still retained | `stacker --config X logs back --all` |
| Why `serve -d` / `ping` failed | `stacker --config X logs --supervisor -n 50` |
| The process names | `stacker --config X list --json` |

Incremental reading is the important pattern. Each `--json` response carries
`next`; keep it and pass it back as `--since` to get only the lines added since:

```bash
stacker --config ./stacker.yml logs back --json
# {"ok":true,"process":"back","status":"running","from":40,"next":60,"lines":[...]}
stacker --config ./stacker.yml logs back --since 60 --json   # only what came after
```

Rules:

- **Never use `-f`.** It follows until Ctrl+C and will hang your turn. Poll
  with `--since` instead.
- **Default is the last 200 lines.** Ask for `-n N` deliberately; `--all` can
  be enormous.
- **`--supervisor` reads a file, not the control plane**, so it still answers
  when the supervisor is dead or never started. It holds the daemon's own
  notices — not process output.
- Logs are **memory only**: they die with the supervisor and are capped per
  process by `ui.max_log_lines` and `ui.max_log_bytes` (default 256MB).
  Nothing is written to disk except the daemon log above.
- A bare `stacker` with no TTY prints the running instances **and** the log
  commands for each one; it never opens a UI you cannot answer.
- Need names and statuses without a JSON parser? `stacker --config X __complete
  processes` prints `name<TAB>status · :port` per line — the same source the
  shell completion uses. `list --json` stays the richer answer when you can
  parse JSON.

**Tell the human about TAB.** When you hand over a log command, it is worth
mentioning that `stacker completion <bash|zsh|fish>` (installed automatically by
`mise run build:install`) makes `stacker logs <TAB>` list every process with its
status — that is the fluent way for a person to find a log, and it is not
discoverable otherwise.

## 4. Port already in use

If start/restart fails because the port is busy (often another agent left a server):

```bash
# If the process has port: N in stacker.yml, restart already frees it.
stacker restart backend

# Otherwise free explicitly (works even without the TUI):
stacker free-port 8000
stacker start backend
```

`free-port` works on **Linux, macOS, and Windows**. It terminates listeners on that TCP port.

In the TUI, the user can press **`f`** on a selected process that has `port` set.

## 5. YAML contract (do not invent fields)

```yaml
version: 1
ui:
  max_log_lines: 10000   # per-process line cap (default 10000)
  max_log_bytes: 256MB   # per-process memory cap (default 256MB)
processes:
  backend:
    command: mise run back:dev
    cwd: .
    autostart: false   # registered, but user/CLI starts it
    graceful_timeout: 8s
    port: 8000         # optional; freed before every start/restart
    group: application
```

- `autostart: false` (default) → listed, not auto-started when Stacker opens.
- `max_log_bytes` bounds the log memory of **each** process; accepts `256MB`,
  `512kb`, or a plain byte count. Whichever cap hits first (lines or bytes)
  drops the oldest lines. Raise it before raising `max_log_lines` a lot.
- `port` → free listeners before start (fixes stray AI-started servers).
- `group` → optional trimmed label shared by services and root-level standalone tasks. Missing or empty values use the implicit `Other` group. Standalone task entries accept only `command`, `cwd`, `color`, and `group`.
- Group sections keep first-appearance order among service-bearing groups, followed by task-only groups; `Other` is last. Services precede standalone tasks within each section. TUI section folds persist per config in Stacker's user cache; web sidebar folds persist in browser local storage per config.
- Unknown YAML fields are rejected by Stacker.
- A `cwd` that does not exist on this machine (repo not cloned) does **not**
  break the config: that entry alone shows status `disabled` and never starts
  or frees its port. Do not delete such entries from a shared `stacker.yml` —
  they work for whoever has the directory. Fix by cloning the repo, then
  `stacker start <name>` (the directory is re-checked on start).

## 6. Hard rules for agents

1. **If `stacker ping` succeeds → only control services through Stacker CLI.**
2. **Do not** `nohup`, background shells, or open a second terminal for the same service.
3. **Do not** start a second Stacker instance for the same config (it will refuse). Prefer `stacker serve -d` for background, then CLI/`attach`.
4. After changing app code that is already running under Stacker, use `stacker restart <name>`.
5. When leaving a project, `stacker stop <name>` or `stacker down` so the next project can bind ports.
6. Read project `stacker.yml` and any `AGENTS.md` before editing config fields.
7. You may edit `stacker.yml` while Stacker runs — it hot-reloads (add/remove/reorder). Do not kill the supervisor just to add a process.
8. **Always pass `--config`.** Several projects can be supervised at once, and without the flag a command may resolve to a different instance. With the flag, the config you name is honoured unconditionally — a config that exists on disk is never swapped for another instance.
9. **Never run a state-changing command without `--config`.** `start`, `stop`, `restart`, `run` and `down` refuse to adopt another instance and will error; that error is correct, so add `--config` instead of retrying.
10. Use `stacker instances --json` to see every supervisor on the machine (label, config, pid, mode, ports, and `port_collisions`).
11. **Read logs with `stacker logs`, never by opening the TUI or the web viewer** (section 3), and never with `-f`.
12. If two configs declare the same port, starting one **terminates** the other's listener. `port_collisions` in `stacker instances --json` tells you before it happens; both logs record it after.

## 7. Quick decision tree

```
Need a long-running service?
  └─ stacker.yml present?
       ├─ no  → start service the project's normal way (mise/task), or ask user
       └─ yes → stacker ping
                 ├─ running → stacker list/status → start|restart|stop
                 │            └─ need output? → stacker logs <name> -n 100
                 └─ not running → ask user to open stacker / stacker serve -d
                                  (optional: stacker free-port N if blocked)
                                  (failed to start? stacker logs --supervisor)

Always with --config <path>. Got "no running Stacker" while another instance
is listed? That is deliberate — the named config wins. Do not retry without
the flag; either use --config for that config, or ask the user.
```

## 8. CLI cheat sheet

```bash
stacker -config stacker.yml     # session TUI + control plane (human)
stacker serve -d                # headless daemon
stacker attach                  # TUI attach (q detaches)
stacker down                    # stop everything + supervisor
stacker ping --json
stacker list --json
stacker status <name> --json
stacker start <name>
stacker start --group <group>
stacker start -g <group>
stacker stop <name>
stacker stop --group=<group>
stacker restart <name>
stacker restart --group <group>
stacker logs <name> -n 100      # tail of one process (default 200 lines)
stacker logs <name> --json      # lines + "next" to resume from
stacker logs <name> --since N   # only what came after index N
stacker logs --supervisor       # daemon's own log; works with nothing running
stacker free-port <port>        # no instance required
stacker instances --json        # every supervisor on this machine
stacker completion zsh          # completion script (bash | zsh | fish)
stacker __complete processes    # names + status, no jq needed
stacker __complete groups       # live group names for --group / -g
```

`start`, `stop`, and `restart` accept `--group <name>`, `-g <name>`, or
`--group=<name>` instead of a process name. Group mode does not accept a process
name. `stacker list` includes a `GROUP` column (`-` when unassigned), and
`list --json` includes `group` for configured group assignments. Shell completion offers live
group names after either group flag.

Never `stacker logs <name> -f` in an agent: it never returns.

Prefix every one of these with `--config <path>` in real use:

```bash
stacker --config ./stacker.yml list --json
stacker --config ./stacker.yml restart backend
```
