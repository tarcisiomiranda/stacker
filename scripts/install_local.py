#!/usr/bin/env python3
"""Build Stacker for this machine and install it into PATH.

This is the "test my working tree" installer: it detects the platform, builds
from source with the same flags as `mise run build`, and replaces the binary
that PATH already resolves — so `stacker` in a new terminal is the code you
just changed. For released versions use install.sh instead.

Examples:
  python scripts/install_local.py                  # build + install
  python scripts/install_local.py --dry-run        # show what it would do
  python scripts/install_local.py --dir ~/bin      # explicit destination
  python scripts/install_local.py --skip-build     # install the existing bin/stacker
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BINARY_NAME = "stacker.exe" if os.name == "nt" else "stacker"
BUILD_OUTPUT = REPO_ROOT / "bin" / BINARY_NAME
PACKAGE = "./cmd/stacker"


class InstallError(RuntimeError):
    """Anything that should stop the install with a readable message."""


@dataclass(frozen=True)
class Target:
    """Where the build is going and how that was decided."""

    directory: Path
    reason: str

    @property
    def path(self) -> Path:
        return self.directory / BINARY_NAME


@dataclass(frozen=True)
class CompletionTarget:
    """A completion script destination for one shell."""

    shell: str
    path: Path
    # note is printed after the install when the shell needs one more step.
    note: str = ""


def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=REPO_ROOT, text=True, **kwargs)


def capture(cmd: list[str]) -> str | None:
    """Run a command and return stripped stdout, or None when it fails."""
    try:
        proc = run(cmd, capture_output=True, check=False)
    except (OSError, ValueError):
        return None
    if proc.returncode != 0:
        return None
    return proc.stdout.strip()


def go_command() -> list[str]:
    """Locate a Go toolchain: PATH first, then mise (the repo pins go there)."""
    if shutil.which("go"):
        return ["go"]
    if shutil.which("mise") and capture(["mise", "which", "go"]):
        return ["mise", "exec", "--", "go"]
    raise InstallError(
        "no Go toolchain found. Install it with `mise install` in this repo, "
        "or put `go` on PATH."
    )


def detect_platform(go: list[str]) -> tuple[str, str, str]:
    """Return (goos, goarch, label).

    `go env` is the source of truth rather than the `platform` module: under
    Rosetta they disagree, and what matters is the binary Go will produce.
    """
    env = capture(go + ["env", "GOOS", "GOARCH"])
    if not env:
        raise InstallError("could not read `go env GOOS GOARCH`")
    lines = [line.strip() for line in env.splitlines() if line.strip()]
    if len(lines) < 2:
        raise InstallError(f"unexpected `go env` output: {env!r}")
    goos, goarch = lines[0], lines[1]
    label = f"{platform.system()} {platform.release()} ({platform.machine()})"
    return goos, goarch, label


def build_version() -> str:
    """Same stamp as `mise run build`, so the binary reports its provenance."""
    described = capture(["git", "describe", "--tags", "--always", "--dirty"])
    return described or "dev"


def build(go: list[str], version: str, dry_run: bool) -> None:
    cmd = go + [
        "build",
        "-trimpath",
        "-ldflags",
        f"-s -w -X main.version={version}",
        "-o",
        str(BUILD_OUTPUT),
        PACKAGE,
    ]
    if dry_run:
        print(f"  would run: {' '.join(cmd)}")
        return
    BUILD_OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    proc = run(cmd)
    if proc.returncode != 0:
        raise InstallError("build failed")


def path_entries(env_path: str | None = None) -> list[Path]:
    raw = env_path if env_path is not None else os.environ.get("PATH", "")
    entries = []
    for chunk in raw.split(os.pathsep):
        if chunk:
            entries.append(Path(chunk).expanduser())
    return entries


def on_path(directory: Path, entries: list[Path]) -> bool:
    return any(entry == directory for entry in entries)


def writable_dir(directory: Path) -> bool:
    """True when the directory exists and accepts writes."""
    return directory.is_dir() and os.access(directory, os.W_OK)


def resolve_install_dir(
    explicit: str | None,
    env: dict[str, str],
    entries: list[Path],
    existing_binary: str | None,
    home: Path,
) -> Target:
    """Decide where the binary goes.

    Replacing the `stacker` PATH already resolves comes first: on a machine
    that has one, installing anywhere else means `stacker` keeps running the
    old build and the whole point is lost.
    """
    if explicit:
        return Target(Path(explicit).expanduser().resolve(), "requested with --dir")

    from_env = env.get("STACKER_INSTALL_DIR")
    if from_env:
        return Target(Path(from_env).expanduser().resolve(), "STACKER_INSTALL_DIR")

    if existing_binary:
        current = Path(existing_binary).expanduser().resolve().parent
        if writable_dir(current):
            return Target(current, "replaces the stacker already on PATH")

    local_bin = home / ".local" / "bin"
    if on_path(local_bin, entries):
        return Target(local_bin, "~/.local/bin is on PATH")

    usr_local = Path("/usr/local/bin")
    if os.name != "nt" and writable_dir(usr_local) and on_path(usr_local, entries):
        return Target(usr_local, "/usr/local/bin is writable and on PATH")

    return Target(local_bin, "default (~/.local/bin)")


def install_binary(source: Path, target: Path, dry_run: bool) -> None:
    """Copy into place without disturbing a running process.

    A supervisor started from this path still has the old image mapped;
    writing over it fails with "text file busy". Writing a sibling and
    renaming swaps the directory entry instead, so the running instance keeps
    the old inode and the next launch gets the new build.
    """
    staged = target.with_name(f"{target.name}.new-{os.getpid()}")
    if dry_run:
        print(f"  would copy {source} -> {staged}")
        print(f"  would rename {staged} -> {target}")
        return

    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, staged)
    staged.chmod(0o755)
    try:
        os.replace(staged, target)
    except OSError as exc:
        staged.unlink(missing_ok=True)
        raise InstallError(
            f"could not replace {target}: {exc}\n"
            "close anything running that binary, or pass --dir to install elsewhere"
        ) from exc


def brew_prefix() -> Path | None:
    """Homebrew's prefix, when brew is installed."""
    if not shutil.which("brew"):
        return None
    out = capture(["brew", "--prefix"])
    if not out:
        return None
    prefix = Path(out)
    return prefix if prefix.is_dir() else None


def completion_targets(
    home: Path,
    prefix: Path | None,
    shells: list[str],
) -> list[CompletionTarget]:
    """Where each shell's completion goes.

    Homebrew's directories win when they exist: they are already in $fpath (zsh)
    and already sourced (bash), so completion works in the next terminal with no
    edit to a shell rc file. The home fallbacks need one line of setup, which is
    printed as a note rather than written into the user's config.
    """
    targets = []
    for shell in shells:
        if shell == "fish":
            # fish loads this directory on its own, everywhere.
            targets.append(
                CompletionTarget(
                    "fish", home / ".config" / "fish" / "completions" / "stacker.fish"
                )
            )
        elif shell == "zsh":
            brew_dir = prefix / "share" / "zsh" / "site-functions" if prefix else None
            if brew_dir and writable_dir(brew_dir):
                targets.append(CompletionTarget("zsh", brew_dir / "_stacker"))
            else:
                directory = home / ".local" / "share" / "zsh" / "site-functions"
                targets.append(
                    CompletionTarget(
                        "zsh",
                        directory / "_stacker",
                        f"add to ~/.zshrc:  fpath=({directory} $fpath)",
                    )
                )
        elif shell == "bash":
            brew_dir = prefix / "etc" / "bash_completion.d" if prefix else None
            note = ""
            if prefix and not (prefix / "etc/profile.d/bash_completion.sh").exists():
                note = "bash-completion is not installed; run: brew install bash-completion@2"
            if brew_dir and writable_dir(brew_dir):
                targets.append(CompletionTarget("bash", brew_dir / "stacker", note))
            else:
                directory = home / ".local" / "share" / "bash-completion" / "completions"
                targets.append(
                    CompletionTarget(
                        "bash",
                        directory / "stacker",
                        note or "needs bash-completion active to load automatically",
                    )
                )
    return targets


def detect_shells() -> list[str]:
    return [shell for shell in ("bash", "zsh", "fish") if shutil.which(shell)]


def install_completions(binary: Path, targets: list[CompletionTarget], dry_run: bool) -> None:
    """Write each shell's script, generated by the binary just installed."""
    for target in targets:
        if dry_run:
            print(f"  would write {target.path}")
            continue
        proc = subprocess.run(
            [str(binary), "completion", target.shell],
            capture_output=True,
            text=True,
            check=False,
        )
        if proc.returncode != 0:
            print(f"  {target.shell}: skipped ({proc.stderr.strip()})")
            continue
        try:
            target.path.parent.mkdir(parents=True, exist_ok=True)
            staged = target.path.with_name(f"{target.path.name}.new-{os.getpid()}")
            staged.write_text(proc.stdout)
            os.replace(staged, target.path)
        except OSError as exc:
            print(f"  {target.shell}: skipped ({exc})")
            continue
        suffix = f"  — {target.note}" if target.note else ""
        print(f"  {target.shell}: {target.path}{suffix}")


def verify(target: Path) -> str:
    proc = subprocess.run(
        [str(target), "version"], capture_output=True, text=True, check=False
    )
    if proc.returncode != 0:
        raise InstallError(f"installed binary did not run: {proc.stderr.strip()}")
    return proc.stdout.strip()


def running_instances(target: Path) -> list[str]:
    """Configs whose supervisor is still running the previous build.

    Best-effort: a replaced binary does not restart anything, so anything up
    right now keeps the old behaviour until it is restarted.
    """
    out = capture([str(target), "instances", "--json"])
    if not out:
        return []
    try:
        data = json.loads(out)
    except json.JSONDecodeError:
        return []
    rows = data.get("instances") if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return []
    configs = []
    for row in rows:
        if isinstance(row, dict) and row.get("config"):
            configs.append(str(row["config"]))
    return configs


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Build Stacker for this machine and install it into PATH."
    )
    parser.add_argument("--dir", help="install directory (default: auto-detected)")
    parser.add_argument(
        "--dry-run", action="store_true", help="show the plan without changing anything"
    )
    parser.add_argument(
        "--skip-build",
        action="store_true",
        help=f"install the existing {BUILD_OUTPUT.relative_to(REPO_ROOT)} as-is",
    )
    parser.add_argument(
        "--no-completions",
        action="store_true",
        help="do not install shell completions",
    )
    args = parser.parse_args(argv)

    try:
        go = go_command()
        goos, goarch, label = detect_platform(go)
        version = build_version()

        print(f"Platform:  {label}")
        print(f"Go target: {goos}/{goarch}  (via {' '.join(go)})")
        print(f"Version:   {version}")

        if args.skip_build:
            if not BUILD_OUTPUT.exists():
                raise InstallError(f"{BUILD_OUTPUT} does not exist; drop --skip-build")
            print(f"Build:     skipped, using {BUILD_OUTPUT}")
        else:
            print(f"Build:     {BUILD_OUTPUT}")
            build(go, version, args.dry_run)

        target = resolve_install_dir(
            explicit=args.dir,
            env=dict(os.environ),
            entries=path_entries(),
            existing_binary=shutil.which(BINARY_NAME),
            home=Path.home(),
        )
        print(f"Install:   {target.path}  ({target.reason})")

        shells = [] if args.no_completions else detect_shells()
        completions = completion_targets(Path.home(), brew_prefix(), shells)
        if shells:
            print(f"Shells:    {', '.join(shells)}")

        install_binary(BUILD_OUTPUT, target.path, args.dry_run)
        if args.dry_run:
            if completions:
                print("Completions:")
                install_completions(BUILD_OUTPUT, completions, dry_run=True)
            print("\nDry run: nothing was written.")
            return 0

        print(f"\nInstalled: {verify(target.path)} -> {target.path}")
        if completions:
            # Generated by the binary just installed, so the two always agree.
            print("Completions:")
            install_completions(target.path, completions, dry_run=False)
            print("  open a new terminal, then: stacker logs <TAB>")

        if not on_path(target.directory, path_entries()):
            print(f"warning: {target.directory} is not on PATH; add it to use `stacker`")

        resolved = shutil.which(BINARY_NAME)
        if resolved and Path(resolved).resolve() != target.path.resolve():
            print(f"warning: `{BINARY_NAME}` on PATH still resolves to {resolved}")

        live = running_instances(target.path)
        if live:
            print("\nSupervisors already running keep the previous build until restarted:")
            for config in live:
                print(f"  stacker --config {config} down   # then start it again")
        return 0
    except InstallError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
