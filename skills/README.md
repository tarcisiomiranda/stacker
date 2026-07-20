# Stacker agent skills

Canonical skill definition: [`stacker/SKILL.md`](stacker/SKILL.md)

Follows the [Agent Skills](https://agentskills.io/specification) open format (`name` + `description` frontmatter + markdown body).

## Install with the Stacker binary installer (optional)

The curl/`install.sh` installer **does not** install agent skills by default.

```bash
# Binary only (default)
curl -fsSL …/install.sh | bash

# Binary + detect AIs and install SKILL.md globally
curl -fsSL …/install.sh | STACKER_INSTALL_SKILLS=1 bash
```

Truthy values: `1`, `true`, `yes`, `on`.

## Install from a git checkout (detects AIs on this PC)

From the repository root:

```bash
# See which agents are present
mise run skills:list
# or: python scripts/install_skills.py --list

# Install for detected agents (project + global paths)
mise run skills:install
# or: python scripts/install_skills.py

# Force every known tool path
mise run skills:install:all

# Only list / dry-run / scope
python scripts/install_skills.py --dry-run
python scripts/install_skills.py --project-only
python scripts/install_skills.py --global-only
python scripts/install_skills.py --only claude --only codex
```

The installer (`scripts/install_skills.py`) looks for config dirs under `$HOME`
(e.g. `~/.claude`, `~/.codex`, `~/.config/opencode`) and binaries on `PATH`,
then copies `skills/stacker/SKILL.md` only where those tools live.

`./scripts/install-skills.sh` is a thin wrapper around the same Python script.

## Discovery paths by tool

| Tool | Project | Global |
|------|---------|--------|
| **Claude Code** | `.claude/skills/stacker/SKILL.md` | `~/.claude/skills/stacker/SKILL.md` |
| **OpenAI Codex** | `.codex/skills/stacker/SKILL.md` | `~/.codex/skills/stacker/SKILL.md` |
| **OpenCode** | `.opencode/skills/stacker/SKILL.md` | `~/.config/opencode/skills/stacker/SKILL.md` |
| **OpenCode (compat)** | also reads `.claude/skills` and `.agents/skills` | same under `~/` |
| **Cursor** | `.cursor/skills/stacker/SKILL.md` | `~/.cursor/skills/stacker/SKILL.md` |
| **Grok** | `.grok/skills/stacker/SKILL.md` | `~/.grok/skills/stacker/SKILL.md` |
| **Generic / multi-agent** | `.agents/skills/stacker/SKILL.md` | `~/.agents/skills/stacker/SKILL.md` |

## When agents should load this skill

- User asks to start, stop, or restart a backend/frontend/API
- `stacker.yml` exists in the project
- Port already in use / EADDRINUSE
- Mentions of Stacker, process supervisor, free-port
