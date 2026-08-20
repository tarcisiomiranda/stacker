#!/usr/bin/env python3
"""Tests for install_skills.py"""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest import mock

# Allow importing sibling module without package install.
import install_skills as mod


class DetectTests(unittest.TestCase):
    def test_detects_claude_home_marker(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir()
            d = mod.detect_tool(mod.TOOLS[0], home)  # claude is first
            self.assertTrue(d.present)
            self.assertTrue(any(".claude" in r for r in d.reasons))

    def test_detects_binary(self) -> None:
        tool = mod.AgentTool(
            id="fake",
            name="Fake",
            binaries=("definitely-not-on-path-xyz",),
        )
        with mock.patch.object(mod, "which", return_value=Path("/usr/bin/fake")):
            d = mod.detect_tool(tool, Path("/tmp"))
        self.assertTrue(d.present)
        self.assertIn("binary", d.reasons[0])

    def test_not_present_without_markers(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            d = mod.detect_tool(
                mod.AgentTool(id="x", name="X", home_markers=(".nope",)),
                home,
            )
            self.assertFalse(d.present)


class SelectTests(unittest.TestCase):
    def _detections(self) -> list[mod.Detection]:
        tools = list(mod.TOOLS)
        return [
            mod.Detection(tool=tools[0], present=True, reasons=["home"]),
            mod.Detection(tool=tools[1], present=False, reasons=[]),
        ]

    def test_select_only_present(self) -> None:
        selected = mod.select_tools(self._detections(), install_all=False, only=[])
        self.assertEqual([t.id for t in selected], ["claude"])

    def test_select_all(self) -> None:
        dets = [
            mod.Detection(tool=t, present=False, reasons=[]) for t in mod.TOOLS
        ]
        selected = mod.select_tools(dets, install_all=True, only=[])
        self.assertEqual(len(selected), len(mod.TOOLS))

    def test_select_only_ids(self) -> None:
        dets = [mod.Detection(tool=t, present=False, reasons=[]) for t in mod.TOOLS]
        selected = mod.select_tools(dets, install_all=False, only=["codex", "grok"])
        self.assertEqual([t.id for t in selected], ["codex", "grok"])


class InstallTests(unittest.TestCase):
    def test_copy_skill_writes_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            src_dir = root / "skills" / "stacker"
            src_dir.mkdir(parents=True)
            src = src_dir / "SKILL.md"
            src.write_text("---\nname: stacker\n---\nbody\n", encoding="utf-8")
            dest_dir = root / ".claude" / "skills" / "stacker"
            out = mod.copy_skill(src, dest_dir, dry_run=False)
            self.assertTrue(out.is_file())
            self.assertEqual(out.read_text(encoding="utf-8"), src.read_text(encoding="utf-8"))

    def test_dry_run_does_not_write(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            src = root / "SKILL.md"
            src.write_text("x", encoding="utf-8")
            dest_dir = root / "out"
            out = mod.copy_skill(src, dest_dir, dry_run=True)
            self.assertFalse(out.exists())
            self.assertFalse(dest_dir.exists())

    def test_main_installs_detected_global(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            root = base / "repo"
            home = base / "home"
            root.mkdir()
            home.mkdir()
            (home / ".codex").mkdir()
            skill_dir = root / "skills" / "stacker"
            skill_dir.mkdir(parents=True)
            skill = skill_dir / "SKILL.md"
            skill.write_text("---\nname: stacker\ndescription: test skill for agents\n---\n# ok\n", encoding="utf-8")

            with mock.patch.object(mod, "which", return_value=None):
                rc = mod.main(
                    [
                        "--root",
                        str(root),
                        "--home",
                        str(home),
                        "--global-only",
                        "--only",
                        "codex",
                    ]
                )
            self.assertEqual(rc, 0)
            dest = home / ".codex" / "skills" / "stacker" / "SKILL.md"
            self.assertTrue(dest.is_file())
            self.assertIn("stacker", dest.read_text(encoding="utf-8"))

    def test_main_list_exit_codes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            root = base / "repo"
            home = base / "home"
            root.mkdir()
            home.mkdir()
            skill_dir = root / "skills" / "stacker"
            skill_dir.mkdir(parents=True)
            (skill_dir / "SKILL.md").write_text(
                "---\nname: stacker\ndescription: x\n---\n", encoding="utf-8"
            )
            # Ignore host PATH binaries so only home markers count.
            with mock.patch.object(mod, "which", return_value=None):
                rc_empty = mod.main(
                    ["--root", str(root), "--home", str(home), "--list"]
                )
                self.assertEqual(rc_empty, 1)
                (home / ".claude").mkdir()
                rc_found = mod.main(
                    ["--root", str(root), "--home", str(home), "--list"]
                )
                self.assertEqual(rc_found, 0)


class KiroHermesBmadTests(unittest.TestCase):
    def _tool(self, tool_id: str) -> mod.AgentTool:
        for t in mod.TOOLS:
            if t.id == tool_id:
                return t
        self.fail(f"missing tool {tool_id}")

    def _skill_repo(self, base: Path) -> tuple[Path, Path]:
        root = base / "repo"
        home = base / "home"
        root.mkdir()
        home.mkdir()
        skill_dir = root / "skills" / "stacker"
        skill_dir.mkdir(parents=True)
        (skill_dir / "SKILL.md").write_text(
            "---\nname: stacker\ndescription: test\n---\n# ok\n", encoding="utf-8"
        )
        return root, home

    def test_kiro_hermes_bmad_registered(self) -> None:
        ids = [t.id for t in mod.TOOLS]
        for expected in ("kiro", "hermes", "bmad"):
            self.assertIn(expected, ids)

    def test_detects_kiro_home(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            (home / ".kiro").mkdir()
            d = mod.detect_tool(self._tool("kiro"), home)
            self.assertTrue(d.present)

    def test_detects_hermes_home(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            (home / ".hermes").mkdir()
            d = mod.detect_tool(self._tool("hermes"), home)
            self.assertTrue(d.present)

    def test_detects_bmad_when_project_has_bmad(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            home = base / "home"
            root = base / "repo"
            home.mkdir()
            root.mkdir()
            (root / "_bmad").mkdir()
            d = mod.detect_tool(self._tool("bmad"), home, root=root)
            self.assertTrue(d.present)

    def test_bmad_absent_without_project_marker(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            d = mod.detect_tool(self._tool("bmad"), home, root=home)
            self.assertFalse(d.present)

    def test_kiro_install_targets(self) -> None:
        tool = self._tool("kiro")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            home = Path(tmp) / "home"
            root.mkdir()
            home.mkdir()
            targets = mod.install_targets(
                tool, root=root, home=home, project=True, global_=True
            )
            self.assertIn(root / ".kiro" / "skills" / "stacker", targets)
            self.assertIn(home / ".kiro" / "skills" / "stacker", targets)

    def test_hermes_install_targets(self) -> None:
        tool = self._tool("hermes")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            home = Path(tmp) / "home"
            root.mkdir()
            home.mkdir()
            targets = mod.install_targets(
                tool, root=root, home=home, project=True, global_=True
            )
            self.assertIn(root / ".hermes" / "skills" / "stacker", targets)
            self.assertIn(home / ".hermes" / "skills" / "stacker", targets)

    def test_bmad_targets_skipped_without_marker(self) -> None:
        tool = self._tool("bmad")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            home = Path(tmp) / "home"
            root.mkdir()
            home.mkdir()
            targets = mod.install_targets(
                tool, root=root, home=home, project=True, global_=True
            )
            self.assertEqual(targets, [])

    def test_bmad_targets_when_marker_exists(self) -> None:
        tool = self._tool("bmad")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            home = Path(tmp) / "home"
            root.mkdir()
            home.mkdir()
            (root / "_bmad").mkdir()
            targets = mod.install_targets(
                tool, root=root, home=home, project=True, global_=True
            )
            self.assertIn(root / "_bmad" / "custom" / "skills" / "stacker", targets)
            self.assertIn(root / ".claude" / "skills" / "stacker", targets)
            self.assertIn(root / ".agents" / "skills" / "stacker", targets)
            self.assertTrue(all(not str(p).startswith(str(home)) for p in targets))

    def test_main_installs_kiro_global(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, home = self._skill_repo(Path(tmp))
            (home / ".kiro").mkdir()
            with mock.patch.object(mod, "which", return_value=None):
                rc = mod.main(
                    [
                        "--root",
                        str(root),
                        "--home",
                        str(home),
                        "--global-only",
                        "--only",
                        "kiro",
                    ]
                )
            self.assertEqual(rc, 0)
            dest = home / ".kiro" / "skills" / "stacker" / "SKILL.md"
            self.assertTrue(dest.is_file())
            self.assertIn("stacker", dest.read_text(encoding="utf-8"))

    def test_main_installs_bmad_and_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root, home = self._skill_repo(Path(tmp))
            config = root / "_bmad" / "_config"
            config.mkdir(parents=True)
            manifest = config / "skill-manifest.csv"
            manifest.write_text(
                "canonicalId,name,description,module,path\n", encoding="utf-8"
            )
            with mock.patch.object(mod, "which", return_value=None):
                rc = mod.main(
                    [
                        "--root",
                        str(root),
                        "--home",
                        str(home),
                        "--project-only",
                        "--only",
                        "bmad",
                    ]
                )
            self.assertEqual(rc, 0)
            dest = root / "_bmad" / "custom" / "skills" / "stacker" / "SKILL.md"
            self.assertTrue(dest.is_file())
            text = manifest.read_text(encoding="utf-8")
            self.assertIn("stacker", text)
            self.assertIn("_bmad/custom/skills/stacker/SKILL.md", text)


if __name__ == "__main__":
    unittest.main()
