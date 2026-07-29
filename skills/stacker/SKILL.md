---
name: stacker
description: >
  Manage long-running project services through Stacker instead of starting them
  in parallel. Use when starting, stopping, or restarting APIs or frontends;
  when a port is already in use; when stacker.yml exists; or when the user
  mentions Stacker, dev processes, process supervisor, free-port, or service
  restart. Compatible with Claude Code, Codex, OpenCode, Cursor, Grok, and
  other Agent Skills clients.
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

Process names come from keys under `processes:` in `stacker.yml` — never guess.

## 3. Port already in use

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

## 4. YAML contract (do not invent fields)

```yaml
version: 1
processes:
  backend:
    command: mise run back:dev
    cwd: .
    autostart: false   # registered, but user/CLI starts it
    graceful_timeout: 8s
    port: 8000         # optional; freed before every start/restart
```

- `autostart: false` (default) → listed, not auto-started when Stacker opens.
- `port` → free listeners before start (fixes stray AI-started servers).
- Unknown YAML fields are rejected by Stacker.

## 5. Hard rules for agents

1. **If `stacker ping` succeeds → only control services through Stacker CLI.**
2. **Do not** `nohup`, background shells, or open a second terminal for the same service.
3. **Do not** start a second Stacker instance for the same config (it will refuse). Prefer `stacker serve -d` for background, then CLI/`attach`.
4. After changing app code that is already running under Stacker, use `stacker restart <name>`.
5. When leaving a project, `stacker stop <name>` or `stacker down` so the next project can bind ports.
6. Read project `stacker.yml` and any `AGENTS.md` before editing config fields.
7. You may edit `stacker.yml` while Stacker runs — it hot-reloads (add/remove/reorder). Do not kill the supervisor just to add a process.

## 6. Quick decision tree

```
Need a long-running service?
  └─ stacker.yml present?
       ├─ no  → start service the project's normal way (mise/task), or ask user
       └─ yes → stacker ping
                 ├─ running → stacker list/status → start|restart|stop
                 └─ not running → ask user to open stacker / stacker serve -d
                                  (optional: stacker free-port N if blocked)
```

## 7. CLI cheat sheet

```bash
stacker -config stacker.yml     # session TUI + control plane (human)
stacker serve -d                # headless daemon
stacker attach                  # TUI attach (q detaches)
stacker down                    # stop everything + supervisor
stacker ping --json
stacker list --json
stacker status <name> --json
stacker start <name>
stacker stop <name>
stacker restart <name>
stacker free-port <port>        # no instance required
```
