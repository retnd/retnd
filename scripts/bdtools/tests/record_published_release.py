#!/usr/bin/env python3
"""Hermetic behavior tests for record-published-release.py."""

from __future__ import annotations

import hashlib
import json
import re
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
DIGESTS = "core/tests/deploymentdiff/testdata/digests.txt"
CANONICAL = "distribution/packaging/canonical.json"


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class RecorderTest(unittest.TestCase):
    def fixture(self, *, include_arm64: bool = True) -> tuple[Path, Path]:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
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
        # core/tests/deploymentdiff records the canonical file's hash, so the
        # publish has to move that line with the file (#1247).
        (root / DIGESTS).parent.mkdir(parents=True)
        (root / DIGESTS).write_text(
            "1" * 64 + "  container/compose.yaml\n"
            + sha256(root / CANONICAL) + "  " + CANONICAL + "\n"
            + "2" * 64 + "  distribution/packaging/compliance.json\n"
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

    def test_moves_the_deployment_digest_of_canonical_json_with_the_publish(self) -> None:
        root, index = self.fixture()
        result = self.run_recorder(root, index)
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = (root / DIGESTS).read_text().splitlines()
        self.assertEqual(lines, [
            "1" * 64 + "  container/compose.yaml",
            sha256(root / CANONICAL) + "  " + CANONICAL,
            "2" * 64 + "  distribution/packaging/compliance.json",
        ])

    def test_refuses_a_canonical_json_that_drifted_from_its_recorded_digest(self) -> None:
        root, index = self.fixture()
        digests = root / DIGESTS
        digests.write_text(digests.read_text().replace(sha256(root / CANONICAL), "3" * 64))
        before = (root / CANONICAL).read_text()
        result = self.run_recorder(root, index)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("deploymentdiff", result.stderr)
        self.assertEqual((root / CANONICAL).read_text(), before)


class MergeBackTest(unittest.TestCase):
    """The merge back into main runs the deployment tripwire before it pushes."""

    def test_the_merge_back_runs_deploymentdiff_before_it_pushes(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        job = workflow[workflow.index("  merge-back-to-main:"):]
        job = job[:job.index("\n  # ---- the latest tag")]
        tripwire = re.search(r"go test \./tests/deploymentdiff", job)
        push = job.find("git push origin HEAD:main")
        self.assertIsNotNone(tripwire, "merge-back-to-main never runs core/tests/deploymentdiff")
        assert tripwire is not None
        self.assertLess(tripwire.start(), push, "core/tests/deploymentdiff has to run before the push")


if __name__ == "__main__":
    unittest.main()
