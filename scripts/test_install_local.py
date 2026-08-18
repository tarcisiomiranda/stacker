#!/usr/bin/env python3
"""Tests for scripts/install_local.py (run: mise run test:install-local)."""

from __future__ import annotations

import contextlib
import io
import os
import subprocess
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import install_local as il  # noqa: E402


class ResolveInstallDirTest(unittest.TestCase):
    def setUp(self) -> None:
        self.home = Path("/home/dev")
        self.entries = il.path_entries(f"/usr/bin{os.pathsep}/home/dev/.local/bin")

    def test_explicit_dir_wins(self) -> None:
        target = il.resolve_install_dir(
            explicit="/opt/bin",
            env={"STACKER_INSTALL_DIR": "/ignored"},
            entries=self.entries,
            existing_binary="/usr/local/bin/stacker",
            home=self.home,
        )
        self.assertEqual(target.directory, Path("/opt/bin"))
        self.assertIn("--dir", target.reason)

    def test_env_beats_detection(self) -> None:
        target = il.resolve_install_dir(
            explicit=None,
            env={"STACKER_INSTALL_DIR": "/opt/bin"},
            entries=self.entries,
            existing_binary="/usr/local/bin/stacker",
            home=self.home,
        )
        self.assertEqual(target.directory, Path("/opt/bin"))
        self.assertEqual(target.reason, "STACKER_INSTALL_DIR")

    def test_replaces_the_binary_already_on_path(self) -> None:
        """The point of a local install: shadow nothing, replace what runs."""
        with TemporaryBin() as bindir:
            existing = bindir / il.BINARY_NAME
            existing.write_text("old")
            target = il.resolve_install_dir(
                explicit=None,
                env={},
                entries=self.entries,
                existing_binary=str(existing),
                home=self.home,
            )
        self.assertEqual(target.directory, bindir.resolve())
        self.assertIn("already on PATH", target.reason)

    def test_falls_back_to_local_bin_when_on_path(self) -> None:
        target = il.resolve_install_dir(
            explicit=None,
            env={},
            entries=self.entries,
            existing_binary=None,
            home=self.home,
        )
        self.assertEqual(target.directory, self.home / ".local" / "bin")
        self.assertIn("PATH", target.reason)

    def test_default_when_nothing_matches(self) -> None:
        target = il.resolve_install_dir(
            explicit=None,
            env={},
            entries=il.path_entries("/usr/bin"),
            existing_binary=None,
            home=self.home,
        )
        self.assertEqual(target.directory, self.home / ".local" / "bin")
        self.assertIn("default", target.reason)

    def test_unwritable_existing_dir_is_skipped(self) -> None:
        target = il.resolve_install_dir(
            explicit=None,
            env={},
            entries=self.entries,
            existing_binary="/definitely/not/here/stacker",
            home=self.home,
        )
        self.assertEqual(target.directory, self.home / ".local" / "bin")


class InstallBinaryTest(unittest.TestCase):
    def test_replace_keeps_running_image_intact(self) -> None:
        """Rename-into-place, so a supervisor on the old inode is undisturbed."""
        with TemporaryBin() as bindir:
            source = bindir / "built"
            source.write_text("new build")
            target = bindir / il.BINARY_NAME
            target.write_text("old build")
            opened = target.open("rb")  # stands in for a running process
            try:
                il.install_binary(source, target, dry_run=False)
                self.assertEqual(target.read_text(), "new build")
                self.assertEqual(opened.read().decode(), "old build")
            finally:
                opened.close()
            leftovers = [p.name for p in bindir.iterdir() if ".new-" in p.name]
            self.assertEqual(leftovers, [])

    def test_dry_run_writes_nothing(self) -> None:
        with TemporaryBin() as bindir:
            source = bindir / "built"
            source.write_text("new build")
            target = bindir / il.BINARY_NAME
            with contextlib.redirect_stdout(io.StringIO()) as out:
                il.install_binary(source, target, dry_run=True)
            self.assertFalse(target.exists())
            self.assertIn("would copy", out.getvalue())

    def test_target_directory_is_created(self) -> None:
        with TemporaryBin() as bindir:
            source = bindir / "built"
            source.write_text("new build")
            target = bindir / "nested" / "deeper" / il.BINARY_NAME
            il.install_binary(source, target, dry_run=False)
            self.assertTrue(target.exists())
            self.assertTrue(os.access(target, os.X_OK))


class CompletionTargetsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.home = Path("/home/dev")

    def test_fish_needs_no_configuration(self) -> None:
        targets = il.completion_targets(self.home, None, ["fish"])
        self.assertEqual(len(targets), 1)
        self.assertEqual(
            targets[0].path,
            self.home / ".config/fish/completions/stacker.fish",
        )
        self.assertEqual(targets[0].note, "")

    def test_prefers_homebrew_dirs_because_they_are_already_loaded(self) -> None:
        with TemporaryBin() as prefix:
            (prefix / "share/zsh/site-functions").mkdir(parents=True)
            (prefix / "etc/bash_completion.d").mkdir(parents=True)
            (prefix / "etc/profile.d").mkdir(parents=True)
            (prefix / "etc/profile.d/bash_completion.sh").write_text("")
            targets = {
                target.shell: target
                for target in il.completion_targets(self.home, prefix, ["bash", "zsh"])
            }
            self.assertEqual(
                targets["zsh"].path, prefix / "share/zsh/site-functions/_stacker"
            )
            self.assertEqual(targets["zsh"].note, "")
            self.assertEqual(targets["bash"].path, prefix / "etc/bash_completion.d/stacker")
            self.assertEqual(targets["bash"].note, "")

    def test_zsh_fallback_explains_the_missing_fpath_line(self) -> None:
        targets = il.completion_targets(self.home, None, ["zsh"])
        self.assertEqual(
            targets[0].path, self.home / ".local/share/zsh/site-functions/_stacker"
        )
        self.assertIn("fpath", targets[0].note)

    def test_bash_warns_when_bash_completion_is_absent(self) -> None:
        with TemporaryBin() as prefix:
            (prefix / "etc/bash_completion.d").mkdir(parents=True)
            targets = il.completion_targets(self.home, prefix, ["bash"])
            self.assertIn("bash-completion", targets[0].note)

    def test_no_shells_means_no_targets(self) -> None:
        self.assertEqual(il.completion_targets(self.home, None, []), [])


class InstallCompletionsTest(unittest.TestCase):
    def test_writes_what_the_binary_prints(self) -> None:
        with TemporaryBin() as workdir:
            fake = workdir / "fake-stacker"
            fake.write_text(
                "#!/bin/sh\n"
                'test "$1" = completion || exit 1\n'
                'echo "# completion for $2"\n'
            )
            fake.chmod(0o755)
            target = il.CompletionTarget("zsh", workdir / "site-functions" / "_stacker")

            with contextlib.redirect_stdout(io.StringIO()):
                il.install_completions(fake, [target], dry_run=False)

            self.assertEqual(target.path.read_text().strip(), "# completion for zsh")
            leftovers = [p.name for p in target.path.parent.iterdir() if ".new-" in p.name]
            self.assertEqual(leftovers, [])

    def test_failure_is_reported_and_skipped(self) -> None:
        with TemporaryBin() as workdir:
            fake = workdir / "fake-stacker"
            fake.write_text("#!/bin/sh\necho 'nope' >&2\nexit 2\n")
            fake.chmod(0o755)
            target = il.CompletionTarget("zsh", workdir / "_stacker")

            with contextlib.redirect_stdout(io.StringIO()) as out:
                il.install_completions(fake, [target], dry_run=False)

            self.assertFalse(target.path.exists())
            self.assertIn("skipped", out.getvalue())

    def test_dry_run_writes_nothing(self) -> None:
        with TemporaryBin() as workdir:
            target = il.CompletionTarget("fish", workdir / "stacker.fish")
            with contextlib.redirect_stdout(io.StringIO()):
                il.install_completions(Path("/nonexistent"), [target], dry_run=True)
            self.assertFalse(target.path.exists())


class PathHelpersTest(unittest.TestCase):
    def test_path_entries_expands_home(self) -> None:
        entries = il.path_entries(f"~/bin{os.pathsep}/usr/bin")
        self.assertEqual(entries[0], Path("~/bin").expanduser())

    def test_path_entries_drops_empty_chunks(self) -> None:
        entries = il.path_entries(f"/a{os.pathsep}{os.pathsep}/b")
        self.assertEqual(entries, [Path("/a"), Path("/b")])

    def test_on_path(self) -> None:
        entries = il.path_entries("/usr/bin")
        self.assertTrue(il.on_path(Path("/usr/bin"), entries))
        self.assertFalse(il.on_path(Path("/usr/local/bin"), entries))


class CLITest(unittest.TestCase):
    def test_dry_run_does_not_touch_the_system(self) -> None:
        """End-to-end sanity: --dry-run --skip-build must not build or install."""
        script = Path(il.__file__)
        built = il.BUILD_OUTPUT
        if not built.exists():
            self.skipTest("bin/stacker not built yet")
        before = built.stat().st_mtime_ns
        proc = subprocess.run(
            [sys.executable, str(script), "--dry-run", "--skip-build", "--dir", "/tmp/stacker-test-dir"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("Dry run", proc.stdout)
        self.assertEqual(built.stat().st_mtime_ns, before)
        self.assertFalse(Path("/tmp/stacker-test-dir").exists())


class TemporaryBin:
    """Temporary directory as a context manager returning a Path."""

    def __enter__(self) -> Path:
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        return Path(self._tmp.name)

    def __exit__(self, *exc_info) -> None:
        self._tmp.cleanup()


if __name__ == "__main__":
    unittest.main()
