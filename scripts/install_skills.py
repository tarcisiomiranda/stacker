#!/usr/bin/env python3
"""Detect AI coding agents on this machine and install the Stacker skill.

Discovers tools by config directories and PATH binaries, then copies
skills/stacker/SKILL.md into the correct project and/or global skill paths.

Examples:
  python scripts/install_skills.py              # detect + install (project + global for found tools)
  python scripts/install_skills.py --list       # only show what was detected
  python scripts/install_skills.py --dry-run    # show actions without writing
  python scripts/install_skills.py --all        # install for every known tool (ignore detection)
  python scripts/install_skills.py --global-only
  python scripts/install_skills.py --project-only
"""

from __future__ import annotations

import argparse
import os
import shutil
import sys
from dataclasses import dataclass, field
from pathlib import Path


SKILL_REL = Path("skills/stacker/SKILL.md")
SKILL_NAME = "stacker"


@dataclass(frozen=True)
class AgentTool:
    """One AI coding agent and how to detect / install its skill."""

    id: str
    name: str
    # Relative dirs under home that mean the tool is (or was) installed.
    home_markers: tuple[str, ...] = ()
    # Binary names looked up on PATH.
    binaries: tuple[str, ...] = ()
    # Project skill directory relative to repo root (parent of SKILL.md).
    project_skill_dir: str | None = None
    # Global skill directory relative to home (parent of SKILL.md).
    global_skill_dir: str | None = None
    # Extra global skill dirs (compat paths, e.g. OpenCode reading .claude).
    extra_global_skill_dirs: tuple[str, ...] = ()
    # Extra project skill dirs for this tool.
    extra_project_skill_dirs: tuple[str, ...] = ()


# Known agents that understand Agent Skills (SKILL.md) or equivalent layouts.
TOOLS: tuple[AgentTool, ...] = (
    AgentTool(
        id="claude",
        name="Claude Code",
        home_markers=(".claude",),
        binaries=("claude",),
        project_skill_dir=".claude/skills/stacker",
        global_skill_dir=".claude/skills/stacker",
    ),
    AgentTool(
        id="codex",
        name="OpenAI Codex",
        home_markers=(".codex",),
        binaries=("codex",),
        project_skill_dir=".codex/skills/stacker",
        global_skill_dir=".codex/skills/stacker",
    ),
    AgentTool(
        id="opencode",
        name="OpenCode",
        home_markers=(".config/opencode", ".opencode", ".local/share/opencode"),
        binaries=("opencode",),
        project_skill_dir=".opencode/skills/stacker",
        global_skill_dir=".config/opencode/skills/stacker",
        # OpenCode also discovers Claude/agents paths (handled as separate tools).
    ),
    AgentTool(
        id="cursor",
        name="Cursor",
        home_markers=(".cursor",),
        binaries=("cursor", "cursor-agent"),
        project_skill_dir=".cursor/skills/stacker",
        global_skill_dir=".cursor/skills/stacker",
    ),
    AgentTool(
        id="grok",
        name="Grok / xAI",
        home_markers=(".grok",),
        binaries=("grok",),
        project_skill_dir=".grok/skills/stacker",
        global_skill_dir=".grok/skills/stacker",
    ),
    AgentTool(
        id="agents",
        name="Generic Agent Skills (.agents)",
        home_markers=(".agents",),
        binaries=(),
        project_skill_dir=".agents/skills/stacker",
        global_skill_dir=".agents/skills/stacker",
    ),
    AgentTool(
        id="gemini",
        name="Gemini CLI",
        home_markers=(".gemini",),
        binaries=("gemini",),
        project_skill_dir=".agents/skills/stacker",  # often via agents standard
        global_skill_dir=".gemini/skills/stacker",
        extra_global_skill_dirs=(".agents/skills/stacker",),
    ),
    AgentTool(
        id="windsurf",
        name="Windsurf",
        home_markers=(".codeium/windsurf", ".windsurf"),
        binaries=("windsurf",),
        project_skill_dir=".agents/skills/stacker",
        global_skill_dir=".codeium/windsurf/skills/stacker",
        extra_global_skill_dirs=(".agents/skills/stacker",),
    ),
    AgentTool(
        id="continue",
        name="Continue",
        home_markers=(".continue",),
        binaries=(),
        # Continue primarily uses rules; still drop Agent Skills layout if present.
        project_skill_dir=".continue/skills/stacker",
        global_skill_dir=".continue/skills/stacker",
    ),
)


@dataclass
class Detection:
    tool: AgentTool
    present: bool
    reasons: list[str] = field(default_factory=list)


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def which(name: str) -> Path | None:
    path = shutil.which(name)
    return Path(path) if path else None


def detect_tool(tool: AgentTool, home: Path) -> Detection:
    reasons: list[str] = []
    for rel in tool.home_markers:
        marker = home / rel
        if marker.exists():
            reasons.append(f"found {marker}")
    for binary in tool.binaries:
        found = which(binary)
        if found is not None:
            reasons.append(f"binary {found}")
    return Detection(tool=tool, present=bool(reasons), reasons=reasons)


def detect_all(home: Path, tools: tuple[AgentTool, ...] = TOOLS) -> list[Detection]:
    return [detect_tool(t, home) for t in tools]


def skill_source(root: Path) -> Path:
    return root / SKILL_REL


def install_targets(
    tool: AgentTool,
    *,
    root: Path,
    home: Path,
    project: bool,
    global_: bool,
) -> list[Path]:
    """Return destination directories that should contain SKILL.md."""
    dirs: list[Path] = []
    if project and tool.project_skill_dir:
        dirs.append(root / tool.project_skill_dir)
        for rel in tool.extra_project_skill_dirs:
            dirs.append(root / rel)
    if global_ and tool.global_skill_dir:
        dirs.append(home / tool.global_skill_dir)
        for rel in tool.extra_global_skill_dirs:
            dirs.append(home / rel)
    # De-duplicate while preserving order
    seen: set[Path] = set()
    unique: list[Path] = []
    for d in dirs:
        key = d.resolve() if d.exists() else d
        if key in seen:
            continue
        seen.add(key)
        unique.append(d)
    return unique


def copy_skill(src: Path, dest_dir: Path, *, dry_run: bool) -> Path:
    dest = dest_dir / "SKILL.md"
    if dry_run:
        return dest
    dest_dir.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dest)
    return dest


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Detect AI coding agents and install the Stacker SKILL.md",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    p.add_argument(
        "--list",
        action="store_true",
        help="Only list detected agents; do not install",
    )
    p.add_argument(
        "--dry-run",
        action="store_true",
        help="Print install paths without writing files",
    )
    p.add_argument(
        "--all",
        action="store_true",
        help="Install for every known tool, even if not detected",
    )
    p.add_argument(
        "--only",
        metavar="ID",
        action="append",
        default=[],
        help="Only these tool ids (repeatable). Ids: "
        + ", ".join(t.id for t in TOOLS),
    )
    scope = p.add_mutually_exclusive_group()
    scope.add_argument(
        "--project-only",
        action="store_true",
        help="Install only under the repository",
    )
    scope.add_argument(
        "--global-only",
        action="store_true",
        help="Install only under the user home",
    )
    p.add_argument(
        "--home",
        type=Path,
        default=None,
        help="Override home directory (default: ~)",
    )
    p.add_argument(
        "--root",
        type=Path,
        default=None,
        help="Override repository root (default: parent of scripts/)",
    )
    return p.parse_args(argv)


def select_tools(
    detections: list[Detection],
    *,
    install_all: bool,
    only: list[str],
) -> list[AgentTool]:
    by_id = {d.tool.id: d for d in detections}
    if only:
        unknown = [i for i in only if i not in by_id]
        if unknown:
            raise SystemExit(f"unknown tool id(s): {', '.join(unknown)}")
        return [by_id[i].tool for i in only]
    if install_all:
        return [d.tool for d in detections]
    selected = [d.tool for d in detections if d.present]
    if not selected:
        # Always offer generic .agents when nothing else is found so install is useful.
        agents = by_id.get("agents")
        if agents is not None:
            return [agents.tool]
    return selected


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    root = (args.root or repo_root()).resolve()
    home = (args.home or Path.home()).expanduser().resolve()

    src = skill_source(root)
    if not src.is_file():
        print(f"error: missing canonical skill at {src}", file=sys.stderr)
        return 1

    detections = detect_all(home)
    present = [d for d in detections if d.present]
    missing = [d for d in detections if not d.present]

    print(f"Repository: {root}")
    print(f"Home:       {home}")
    print(f"Skill:      {src}")
    print()
    print("Detected AI tools:")
    if present:
        for d in present:
            why = "; ".join(d.reasons)
            print(f"  ✓ {d.tool.name:24} ({d.tool.id})  — {why}")
    else:
        print("  (none detected)")
    if missing:
        print("Not found:")
        for d in missing:
            print(f"  · {d.tool.name:24} ({d.tool.id})")
    print()

    if args.list:
        return 0 if present else 1

    try:
        tools = select_tools(detections, install_all=args.all, only=args.only)
    except SystemExit as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    if not tools:
        print("Nothing to install. Use --all to force every known tool.")
        return 1

    project = not args.global_only
    global_ = not args.project_only
    # Default: both project and global for selected tools.

    installed: list[Path] = []
    skipped: list[str] = []

    for tool in tools:
        targets = install_targets(
            tool, root=root, home=home, project=project, global_=global_
        )
        if not targets:
            skipped.append(tool.id)
            continue
        print(f"{tool.name} ({tool.id}):")
        for dest_dir in targets:
            dest = copy_skill(src, dest_dir, dry_run=args.dry_run)
            prefix = "would install" if args.dry_run else "installed"
            print(f"  {prefix} {dest}")
            installed.append(dest)

    print()
    if args.dry_run:
        print(f"Dry run: {len(installed)} path(s) would be written.")
    else:
        print(f"Done: {len(installed)} skill file(s) written.")
    if skipped:
        print(f"Skipped (no path mapping): {', '.join(skipped)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
