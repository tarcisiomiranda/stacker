---
name: stacker
description: >
  Manage long-running project services and read their logs through Stacker.
  Use when working with stacker.yml, discovering Stacker projects from a
  parent folder, starting, stopping or restarting supervised services, reading
  service or daemon logs, or freeing an occupied port. Covers CLI and MCP
  access to local and remote supervisors, including projects edited over
  SFTP. Compatible with Claude Code, Codex, OpenCode, Cursor, Grok, and other
  Agent Skills clients.
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

If a Stacker MCP server is registered, call `stacker_list` on it first. Check the returned `host` and absolute `config` path, then choose the exact process name from `processes`. An SFTP editor and the supervisor can be on different machines. Select the MCP connection for the machine running the services.

| Operation | MCP tool |
|---|---|
| List processes and confirm the config | `stacker_list` |
| Check a process | `stacker_status` with `name` |
| Read its log | `stacker_logs` with `name`, optional `limit` (1–500), optional `since` |
| Start, stop or restart it | `stacker_start`, `stacker_stop`, `stacker_restart` with `name` |

For example, if `stacker_list` includes `backend`, call `stacker_logs` with `{"name":"backend","limit":50}` for its latest 50 lines. Pass the returned `next` as `since` on the same server and process to read new output. To restart it, call `stacker_restart` with `{"name":"backend"}`.

With a local supervisor, pass its config to the CLI.

```bash
stacker --config ./stacker.yml ping --json
# or
stacker --config ./stacker.yml list --json
```

| Result | Meaning | What you do |
|--------|---------|-------------|
| exit 0 / `"running": true` | Supervisor up (session or serve) | Use its CLI or its registered MCP server |
| exit 1 / not running | No Stacker for this config | Ask the user to open `stacker` or `stacker serve -d`. Optionally `free-port` if a bind is stuck. **Do not** invent a second supervisor. |
| `stacker` not found | Binary missing | Say Stacker is not installed; do not background services unless the user asks. |

Always pass the `--config` path for the target service.

### Open projects from a parent folder

Run `stacker` in a terminal from a parent folder without its own
`stacker.yml`. The picker lists the configs in immediate subfolders, including
stopped projects. Use the arrow keys and `Enter` to open one. Opening a stopped
project on its own uses the `autostart` settings in its YAML.

With two or more discovered configs, choose **All projects** to see services
and standalone tasks grouped by folder. `Enter` starts the selected entry,
`s` stops it, and `r` restarts it. The log pane shows the selected entry's
output. Stacker reuses running supervisors and launches a background
supervisor for a stopped project when you first start an entry. Supervisors
launched this way run entries on demand, including after a config reload.
Press `q` to detach and leave services running.

For an agent using the CLI, pass the selected project's own config, for example
`stacker --config ./project-a/stacker.yml list --json`. Use the same config
with `down` when the user wants to shut down that project's supervisor.
Without a terminal, the picker prints project paths and launch commands, plus
log commands for running instances, then exits with status 1.

### Connect an MCP client

Run the MCP command on the supervisor host using the same OS account and runtime directory as its Stacker session or daemon. If a tool reports an offline supervisor, run `stacker --config /srv/app/stacker.yml ping` on that host with the project's actual config path.

The examples use Codex. In other MCP clients, use the executable and arguments after `--` for stdio, or enter the URL for Streamable HTTP. Replace the example hostname, account and paths with those of the installation.

**Local command (stdio).** When the agent runs on the supervisor host, register the command once from the directory containing `stacker.yml`. This opens no network port and needs no token. For a source build, use the absolute path to `bin/stacker` as the executable.

```bash
codex mcp add stacker-local -- stacker --config "$PWD/stacker.yml" mcp
```

**Local URL without authentication.** Keep the listener running on the supervisor host.

```bash
stacker --config "$PWD/stacker.yml" mcp --no-auth
```

On that same machine, enter `http://127.0.0.1:52912/mcp` in the client. With Codex, register it from another terminal.

```bash
codex mcp add stacker-local-http --url http://127.0.0.1:52912/mcp
```

`--no-auth` binds only to loopback. To choose another port, add e.g. `--listen 127.0.0.1:9300` and update the client URL. Any local process that can reach that port can read logs and control the services.

**Remote command over SSH.** Register SSH on the agent machine. It can reuse the SFTP key or SSH agent if the remote account can execute commands. SSH authenticates the connection, so a separate Stacker token is unnecessary. Use the config and binary paths on the supervisor host.

```bash
codex mcp add stacker-ssh -- ssh -T -o BatchMode=yes dev@stacker.example \
  '/usr/local/bin/stacker --config /srv/app/stacker.yml mcp'
```

**Remote HTTPS URL.** On the supervisor host, start the listener with a TLS certificate for the hostname clients will use, its private key and a file containing one bearer token.

```bash
stacker --config /srv/app/stacker.yml mcp --listen stacker.example:9443 \
  --tls-cert ~/.config/stacker/mcp-tls/cert.pem --tls-key ~/.config/stacker/mcp-tls/key.pem \
  --token-file ~/.config/stacker/mcp.token
```

On the agent machine, configure the client to trust the certificate's issuer. Supply the same token through the client's secret store. For Codex, make it available as `STACKER_MCP_TOKEN` in the environment that launches Codex and register the URL.

```bash
codex mcp add stacker-https --url https://stacker.example:9443/mcp \
  --bearer-token-env-var STACKER_MCP_TOKEN
```

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

Process output lives in the supervisor's memory. Use `stacker_logs` over MCP or
`stacker logs` through the CLI.

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

1. **Control supervised services through the CLI or the registered MCP server for that config.**
2. **Do not** `nohup`, background shells, or open a second terminal for the same service.
3. **Do not** start a second Stacker instance for the same config (it will refuse). Prefer `stacker serve -d` for background, then CLI/`attach`.
4. After changing app code that is already running under Stacker, use `stacker restart <name>`.
5. When leaving a project, `stacker stop <name>` or `stacker down` so the next project can bind ports.
6. Read project `stacker.yml` and any `AGENTS.md` before editing config fields.
7. You may edit `stacker.yml` while Stacker runs — it hot-reloads (add/remove/reorder). Do not kill the supervisor just to add a process.
8. **Always pass `--config`.** Several projects can be supervised at once, and without the flag a command may resolve to a different instance. With the flag, the config you name is honoured unconditionally — a config that exists on disk is never swapped for another instance.
9. **Never run a state-changing command without `--config`.** `start`, `stop`, `restart`, `run` and `down` refuse to adopt another instance and will error; that error is correct, so add `--config` instead of retrying.
10. Use `stacker instances --json` to see every supervisor on the machine (label, config, pid, mode, ports, and `port_collisions`).
11. **Read logs with `stacker_logs` or the CLI's `stacker logs`** (section 3). Use the resume index to poll.
12. If two configs declare the same port, starting one **terminates** the other's listener. `port_collisions` in `stacker instances --json` tells you before it happens; both logs record it after.

## 7. Quick decision tree

```
Need a long-running service?
  ├─ Stacker MCP registered for this project? → stacker_list → confirm host and config
  │                                            → stacker_status / stacker_start / stacker_restart / stacker_logs
  ├─ Editing over SFTP? → connect to the supervisor host over SSH or HTTPS
  └─ stacker.yml for this service here or in an immediate subfolder?
       ├─ no  → start service the project's normal way (mise/task), or ask user
       └─ yes → stacker --config PATH ping
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
