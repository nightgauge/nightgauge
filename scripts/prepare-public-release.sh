#!/usr/bin/env bash
# Export a deterministic public candidate from an immutable source commit.
set -euo pipefail

usage() {
  echo "Usage: $0 --source-sha SHA --output-dir NEW_DIR [--source-repo DIR] [--evidence-dir NEW_DIR]" >&2
  exit 2
}

SOURCE_REPO="$(git rev-parse --show-toplevel 2>/dev/null || true)"
SOURCE_SHA=""
OUTPUT_DIR=""
EVIDENCE_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-repo) SOURCE_REPO="$2"; shift 2 ;;
    --source-sha) SOURCE_SHA="$2"; shift 2 ;;
    --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
    --evidence-dir) EVIDENCE_DIR="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$SOURCE_REPO" && -n "$SOURCE_SHA" && -n "$OUTPUT_DIR" ]] || usage
SOURCE_REPO="$(cd "$SOURCE_REPO" && pwd -P)"
git -C "$SOURCE_REPO" cat-file -e "${SOURCE_SHA}^{commit}"
SOURCE_SHA="$(git -C "$SOURCE_REPO" rev-parse "${SOURCE_SHA}^{commit}")"
MANIFEST_SOURCE="$SOURCE_REPO/scripts/public-release-exclusions.txt"
[[ -f "$MANIFEST_SOURCE" ]] || { echo "ERROR: missing $MANIFEST_SOURCE" >&2; exit 1; }

if [[ -e "$OUTPUT_DIR" ]]; then
  echo "ERROR: output path already exists; provide a new directory: $OUTPUT_DIR" >&2
  exit 1
fi
EVIDENCE_DIR="${EVIDENCE_DIR:-${OUTPUT_DIR}.certification}"
if [[ -e "$EVIDENCE_DIR" ]]; then
  echo "ERROR: evidence path already exists; provide a new directory: $EVIDENCE_DIR" >&2
  exit 1
fi
mkdir -p "$OUTPUT_DIR"
mkdir -p "$EVIDENCE_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd -P)"
EVIDENCE_DIR="$(cd "$EVIDENCE_DIR" && pwd -P)"

git -C "$SOURCE_REPO" archive "$SOURCE_SHA" | tar -x -C "$OUTPUT_DIR"

python3 - "$OUTPUT_DIR" "$MANIFEST_SOURCE" <<'PY'
from pathlib import Path
import fnmatch
import sys

root = Path(sys.argv[1]).resolve()
manifest = Path(sys.argv[2])
patterns = [
    line.strip() for line in manifest.read_text(encoding="utf-8").splitlines()
    if line.strip() and not line.lstrip().startswith("#")
]
files = sorted(path for path in root.rglob("*") if path.is_file())
for path in files:
    relative = path.relative_to(root).as_posix()
    if any(fnmatch.fnmatchcase(relative, pattern) for pattern in patterns):
        path.unlink()

for directory in sorted((p for p in root.rglob("*") if p.is_dir()), reverse=True):
    try:
        directory.rmdir()
    except OSError:
        pass
PY

# Historical commit fingerprints are meaningless in the fresh root commit.
# Replace them with the reviewed, current-tree-only fixture allowlist.
cp "$SOURCE_REPO/scripts/public-gitleaksignore" "$OUTPUT_DIR/.gitleaksignore"

(
  cd "$OUTPUT_DIR"
  find . -type f -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 > "$EVIDENCE_DIR/PUBLIC_FILE_HASHES.txt"
  {
    echo "source_sha=$SOURCE_SHA"
    echo "source_tree=$(git -C "$SOURCE_REPO" rev-parse "${SOURCE_SHA}^{tree}")"
    echo "exporter_sha256=$(shasum -a 256 "$SOURCE_REPO/scripts/prepare-public-release.sh" | awk '{print $1}')"
    echo "exclusions_sha256=$(shasum -a 256 "$MANIFEST_SOURCE" | awk '{print $1}')"
    echo "git_version=$(git --version)"
    echo "python_version=$(python3 --version 2>&1)"
  } > "$EVIDENCE_DIR/PUBLIC_EXPORT_MANIFEST.txt"
)

echo "Exported $SOURCE_SHA to $OUTPUT_DIR"
echo "Certification evidence: $EVIDENCE_DIR"
