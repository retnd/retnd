#!/usr/bin/env python3
"""Hermetic behavior tests for record-published-release.py."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts/release/record-published-release.py"
INDEX = "sha256:" + "a" * 64
AMD64 = "sha256:" + "b" * 64
ARM64 = "sha256:" + "c" * 64


class RecorderTest(unittest.TestCase):
    def fixture(self, *, include_arm64: bool = True) -> tuple[Path, Path]:
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        (root / "distribution/packaging").mkdir(parents=True)
        (root / "container").mkdir()
        (root / "scripts/install").mkdir(parents=True)
        (root / "distribution/packaging/canonical.json").write_text(json.dumps({
            "image": {"tag": "0.5.0", "published": False, "note": ["candidate"]}
        }))
        (root / "container/release-manifest.json").write_text(json.dumps({
            "version": "0.5.0",
            "architectures": [
                {"architecture": "amd64", "registry_digest": None},
                {"architecture": "arm64", "registry_digest": None},
            ],
            "index_digest": None,
        }))
        (root / "scripts/install/install_docker_host.py").write_text(
            'CARRIED_RELEASE = "0.5.0"\nCARRIED_RELEASE_DIGEST = None\n'
        )
        manifests = [{
            "digest": AMD64,
            "platform": {"os": "linux", "architecture": "amd64"},
        }]
        if include_arm64:
            manifests.append({
                "digest": ARM64,
                "platform": {"os": "linux", "architecture": "arm64"},
            })
        index = root / "index.json"
        index.write_text(json.dumps({"manifests": manifests}))
        return root, index

    def run_recorder(self, root: Path, index: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(root),
             "--index-digest", INDEX, "--index-manifest", str(index)],
            text=True, capture_output=True, check=False,
        )

    def test_records_registry_state_and_installer_pin_together(self) -> None:
        root, index = self.fixture()
        result = self.run_recorder(root, index)
        self.assertEqual(result.returncode, 0, result.stderr)

        canonical = json.loads((root / "distribution/packaging/canonical.json").read_text())
        manifest = json.loads((root / "container/release-manifest.json").read_text())
        installer = (root / "scripts/install/install_docker_host.py").read_text()
        self.assertIs(canonical["image"]["published"], True)
        self.assertEqual(manifest["index_digest"], INDEX)
        self.assertEqual(
            {item["architecture"]: item["registry_digest"] for item in manifest["architectures"]},
            {"amd64": AMD64, "arm64": ARM64},
        )
        self.assertIn(f'CARRIED_RELEASE_DIGEST = "{INDEX}"', installer)

    def test_refuses_an_incomplete_registry_index_without_writing(self) -> None:
        root, index = self.fixture(include_arm64=False)
        before = (root / "container/release-manifest.json").read_text()
        result = self.run_recorder(root, index)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing=['arm64']", result.stderr)
        self.assertEqual((root / "container/release-manifest.json").read_text(), before)


if __name__ == "__main__":
    unittest.main()
