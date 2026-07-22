"""Tests for the GitHub release publisher."""

from __future__ import annotations

import hashlib
import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("release.py")
SPEC = importlib.util.spec_from_file_location("stacker_release", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"could not load {SCRIPT_PATH}")
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ValidateTagTests(unittest.TestCase):
    def test_accepts_semver_tags(self) -> None:
        for tag in ("v0.1.0", "v1.2.3", "v2.0.0-rc.1", "v3.4.5+build.7"):
            with self.subTest(tag=tag):
                self.assertEqual(release.validate_tag(tag), tag)

    def test_rejects_invalid_tags(self) -> None:
        for tag in ("1.2.3", "v1.2", "v01.2.3", "latest", "v1.2.3-"):
            with self.subTest(tag=tag):
                with self.assertRaises(ValueError):
                    release.validate_tag(tag)


class ReleaseBodyTests(unittest.TestCase):
    def test_groups_conventional_commits(self) -> None:
        body = release.build_release_body(
            [
                "feat: add word wrap",
                "feat(web): color selector",
                "fix: tail offset drift",
                "docs: update AGENTS.md",
                "plain subject without prefix",
            ],
            "v0.6.0",
            "v0.7.0",
            "owner/stacker",
        )

        self.assertIsNotNone(body)
        assert body is not None
        self.assertIn("## What's new", body)
        self.assertIn("### Features\n- add word wrap\n- color selector", body)
        self.assertIn("### Fixes\n- tail offset drift", body)
        self.assertIn(
            "### Other changes\n- update AGENTS.md\n- plain subject without prefix",
            body,
        )
        self.assertIn(
            "https://github.com/owner/stacker/compare/v0.6.0...v0.7.0",
            body,
        )

    def test_returns_none_without_commits(self) -> None:
        self.assertIsNone(
            release.build_release_body([], "v0.6.0", "v0.7.0", "owner/stacker")
        )

    def test_first_release_has_no_compare_link(self) -> None:
        body = release.build_release_body(
            ["feat: initial"], None, "v0.1.0", "owner/stacker"
        )
        assert body is not None
        self.assertNotIn("compare", body)

    def test_skips_empty_sections(self) -> None:
        body = release.build_release_body(
            ["fix: only a fix"], "v0.6.0", "v0.7.0", "owner/stacker"
        )
        assert body is not None
        self.assertNotIn("### Features", body)
        self.assertNotIn("### Other changes", body)


class ChangelogCommitsTests(unittest.TestCase):
    def test_reads_range_from_git_history(self) -> None:
        import os
        import subprocess

        with tempfile.TemporaryDirectory() as temporary_directory:
            env = {
                **os.environ,
                "GIT_AUTHOR_NAME": "t",
                "GIT_AUTHOR_EMAIL": "t@example.com",
                "GIT_COMMITTER_NAME": "t",
                "GIT_COMMITTER_EMAIL": "t@example.com",
            }

            def git(*args: str) -> None:
                subprocess.run(
                    ["git", *args],
                    cwd=temporary_directory,
                    env=env,
                    check=True,
                    capture_output=True,
                )

            git("init", "-q")
            git("commit", "--allow-empty", "-q", "-m", "feat: first")
            git("tag", "v0.1.0")
            git("commit", "--allow-empty", "-q", "-m", "feat: second")
            git("commit", "--allow-empty", "-q", "-m", "fix: third")
            git("tag", "v0.2.0")

            cwd = os.getcwd()
            os.chdir(temporary_directory)
            try:
                previous, subjects = release.changelog_commits("v0.2.0")
            finally:
                os.chdir(cwd)

        self.assertEqual(previous, "v0.1.0")
        self.assertEqual(subjects, ["fix: third", "feat: second"])


class AssetValidationTests(unittest.TestCase):
    def test_accepts_complete_release_directory(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            release_dir = Path(temporary_directory)
            checksums = []
            for filename in sorted(release.EXPECTED_BINARIES):
                contents = f"binary:{filename}".encode()
                (release_dir / filename).write_bytes(contents)
                checksums.append(f"{hashlib.sha256(contents).hexdigest()}  {filename}")
            (release_dir / "checksums.txt").write_text(
                "\n".join(checksums) + "\n",
                encoding="utf-8",
            )

            assets = release.collect_and_verify_assets(release_dir)

            self.assertEqual(len(assets), len(release.EXPECTED_BINARIES) + 1)
            self.assertEqual(assets[-1].name, "checksums.txt")

    def test_rejects_checksum_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            release_dir = Path(temporary_directory)
            checksums = []
            for filename in sorted(release.EXPECTED_BINARIES):
                (release_dir / filename).write_bytes(b"binary")
                checksums.append(f"{'0' * 64}  {filename}")
            (release_dir / "checksums.txt").write_text(
                "\n".join(checksums) + "\n",
                encoding="utf-8",
            )

            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                release.collect_and_verify_assets(release_dir)


if __name__ == "__main__":
    unittest.main()
