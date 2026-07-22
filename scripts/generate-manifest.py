#!/usr/bin/env python3
"""Generate manifest.json from goreleaser checksums.txt.

Reads checksums.txt (SHA256 per artifact) and emits a JSON manifest
consumed by the SDK postinstall script for binary verification.

Usage:
    python3 scripts/generate-manifest.py dist/

Output (stdout):
    {
      "version": "0.1.42",
      "files": {
        "nightgauge_0.1.42_darwin_arm64.tar.gz": "sha256:abc...",
        ...
      }
    }
"""

import json
import os
import re
import sys


def parse_checksums(checksums_path: str) -> dict[str, str]:
    """Parse goreleaser checksums.txt into {filename: sha256} mapping."""
    files: dict[str, str] = {}
    with open(checksums_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            # Format: "<sha256>  <filename>" (two spaces)
            parts = line.split("  ", 1)
            if len(parts) != 2:
                continue
            sha256, filename = parts
            # Only include tar.gz archives (skip individual binaries)
            if filename.endswith(".tar.gz"):
                files[filename] = f"sha256:{sha256}"
    return files


def extract_version(files: dict[str, str]) -> str:
    """Extract version from archive filenames."""
    for filename in files:
        match = re.search(r"nightgauge_([^_]+)_", filename)
        if match:
            return match.group(1)
    return "unknown"


def main() -> None:
    if len(sys.argv) < 2:
        print("Usage: generate-manifest.py <dist-dir>", file=sys.stderr)
        sys.exit(1)

    dist_dir = sys.argv[1]
    checksums_path = os.path.join(dist_dir, "checksums.txt")

    if not os.path.exists(checksums_path):
        print(f"ERROR: {checksums_path} not found", file=sys.stderr)
        sys.exit(1)

    files = parse_checksums(checksums_path)
    version = extract_version(files)

    manifest = {"version": version, "files": files}

    json.dump(manifest, sys.stdout, indent=2)
    print()  # trailing newline


if __name__ == "__main__":
    main()
