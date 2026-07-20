# Stacker

Terminal process supervisor written in Go and configured with YAML.

- processes defined in YAML;
- graceful start, stop, and restart;
- optional `port` that is freed before start (kills stray listeners on Linux, macOS, and Windows);
- CLI control plane so agents can list/start/stop/restart without spawning parallel services;
- separate stdout and stderr capture;
- scrollable logs with a configurable memory limit;
- drag selection and copying through OSC 52;
- termination of the entire process group on Linux and macOS.

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

## Run

```bash
stacker -config stacker.yml
```

Without `-config`, Stacker looks for `stacker.yml` in the current directory.

Relative `cwd` paths are resolved from the directory containing the configuration file. Unknown YAML fields, empty commands, missing directories, and invalid timeouts are rejected before the TUI opens.

Each command inherits the environment, including `PATH`, from the user who started Stacker and runs through `/bin/sh -c`. If `mise` already works in the current terminal, use `command: mise run task-name`; there is no need to export paths or activate `mise` inside the YAML file.

## Development

```bash
mise install
mise run dev
```

The `demo` process starts automatically so you can test log handling without configuring another project.

## Local build

```bash
mise run build
./bin/stacker -config stacker.yml
```

## Install from source

```bash
mise run install
```

`go install` writes the binary to `$GOBIN` or, when it is not configured, to `$(go env GOPATH)/bin`.

## Releases

The `.github/workflows/release.yml` workflow publishes releases for SemVer tags. It runs tests and `go vet`, builds CGO-disabled binaries for Linux and macOS on AMD64 and ARM64, generates `checksums.txt`, and attaches all artifacts to the GitHub Release.

To publish a version:

```bash
git tag v0.1.0
git push origin v0.1.0
```

You can also start the workflow manually from the Actions tab and provide the desired tag. The pipeline does not create commits or modify the `main` branch.

## Controls

- `↑/↓` or `j/k`: select a process
- `Enter`: start
- `s`: stop
- `r`: restart
- `f`: free the configured `port` for the selected process
- mouse wheel: scroll through logs
- drag with the left mouse button: select lines
- release the left mouse button: copy the selection
- `G` or `End`: return to the bottom
- `Esc`: clear the selection
- `q`: quit

## YAML process options

```yaml
processes:
  backend:
    command: mise run back:dev
    cwd: .
    autostart: false      # listed in the TUI, but do not start until Enter/CLI
    graceful_timeout: 8s
    port: 8000            # free this TCP port before every start/restart
```

When `port` is set, Stacker terminates whatever is listening on that port before start. Use this when an IDE or AI agent left an API process bound and `restart` would fail with “address already in use”.

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

See `skills/README.md` for details.

## CLI (for scripts and AI agents)

Start the TUI once per project config. While it is running, use the CLI instead of launching services yourself:

```bash
stacker -config stacker.yml     # TUI + local control plane
stacker ping                    # exit 0 if running for this config
stacker list --json
stacker status backend --json
stacker start backend
stacker stop backend
stacker restart backend
stacker free-port 8000          # works without the TUI
```

Only one Stacker TUI is allowed per absolute config path. Agents should:

1. `stacker ping` (or `list`) for the project `stacker.yml`
2. if running, `restart` / `start` / `stop` via CLI
3. never start the same service with `mise run …` or `go run …` in parallel

## Current limitations

- process group signaling is best-effort on Windows (`taskkill /T`); Unix uses process groups via `setpgid`
- selection operates on entire lines, not individual columns;
- OSC 52 depends on terminal support and configuration;
- process health checks and dependencies are not supported yet.
