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
