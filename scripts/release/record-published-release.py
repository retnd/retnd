#!/usr/bin/env python3
"""Record registry facts after the release workflow publishes an image.

The workflow reads the immutable index and platform digests back from GHCR,
then passes them here. This command performs the three state changes that must
remain atomic: release-manifest digests, canonical published state, and the
installer's carried digest.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")


def fail(message: str) -> None:
    raise SystemExit(f"record-published-release: {message}")


def read_json(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"cannot read {path}: {exc}")
    if not isinstance(value, dict):
        fail(f"{path} must contain a JSON object")
    return value


def write_json(path: Path, value: dict) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def replace_once(text: str, pattern: str, replacement: str, label: str) -> str:
    updated, count = re.subn(pattern, replacement, text, flags=re.MULTILINE)
    if count != 1:
        fail(f"expected exactly one {label} assignment, found {count}")
    return updated


def record(root: Path, index_digest: str, index_path: Path) -> None:
    if not DIGEST.fullmatch(index_digest):
        fail(f"invalid index digest {index_digest!r}")

    canonical_path = root / "distribution/packaging/canonical.json"
    release_path = root / "container/release-manifest.json"
    installer_path = root / "scripts/install/install_docker_host.py"

    canonical = read_json(canonical_path)
    release = read_json(release_path)
    index = read_json(index_path)

    image = canonical.get("image")
    if not isinstance(image, dict):
        fail(f"{canonical_path} has no image object")
    version = image.get("tag")
    if version != release.get("version"):
        fail(f"canonical tag {version!r} and release manifest version {release.get('version')!r} differ")

    expected = {
        item.get("architecture")
        for item in release.get("architectures", [])
        if isinstance(item, dict)
    }
    if not expected or None in expected:
        fail(f"{release_path} has an invalid architecture list")

    registry_digests: dict[str, str] = {}
    for item in index.get("manifests", []):
        if not isinstance(item, dict):
            continue
        platform = item.get("platform")
        if not isinstance(platform, dict) or platform.get("os") != "linux":
            continue
        architecture = platform.get("architecture")
        digest = item.get("digest")
        if architecture in expected:
            if architecture in registry_digests:
                fail(f"registry index contains linux/{architecture} more than once")
            if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
                fail(f"registry index has an invalid linux/{architecture} digest")
            registry_digests[architecture] = digest

    missing = expected - registry_digests.keys()
    extra = registry_digests.keys() - expected
    if missing or extra:
        fail(f"registry architectures differ from the release manifest (missing={sorted(missing)}, extra={sorted(extra)})")

    for item in release["architectures"]:
        item["registry_digest"] = registry_digests[item["architecture"]]
    release["index_digest"] = index_digest

    image["published"] = True
    image["note"] = [
        f"published:true. {version} was published by .github/workflows/release.yml from the append-only release branch.",
        "container/release-manifest.json records the index digest and each linux architecture digest read back from GHCR after the push.",
        "The installer carries the same immutable index digest; distribution/packaging tests refuse any disagreement among these three records.",
    ]

    try:
        installer = installer_path.read_text(encoding="utf-8")
    except OSError as exc:
        fail(f"cannot read {installer_path}: {exc}")
    installer = replace_once(
        installer,
        r'^CARRIED_RELEASE = "[^"]+"$',
        f'CARRIED_RELEASE = "{version}"',
        "CARRIED_RELEASE",
    )
    installer = replace_once(
        installer,
        r"^CARRIED_RELEASE_DIGEST = (?:None|\"sha256:[0-9a-f]{64}\")$",
        f'CARRIED_RELEASE_DIGEST = "{index_digest}"',
        "CARRIED_RELEASE_DIGEST",
    )

    write_json(release_path, release)
    write_json(canonical_path, canonical)
    installer_path.write_text(installer, encoding="utf-8")

    print(f"recorded {version} as {index_digest}")
    for architecture in sorted(registry_digests):
        print(f"recorded linux/{architecture} as {registry_digests[architecture]}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--index-digest", required=True)
    parser.add_argument("--index-manifest", type=Path, required=True)
    args = parser.parse_args()
    record(args.root.resolve(), args.index_digest, args.index_manifest.resolve())
    return 0


if __name__ == "__main__":
    sys.exit(main())
