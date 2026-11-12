#!/usr/bin/env bash
#
# release-scan-gate.sh — hold a GitHub Release as a draft until VirusTotal
# reports every file in it clean, and let the channels move only from a
# published, scan-clean release (#2946).
#
#   scan <tag>    The release must be a DRAFT. Download every asset of the draft,
#                 unpack every .vsix and .tar.gz, collect the assets plus every
#                 binary (ELF, Mach-O or PE magic, or the exec bit) and every JS
#                 bundle (*.js, *.cjs, *.mjs) inside them, and scan the set with
#                 scripts/vt-scan.sh (deduplicated by sha256). Upload
#                 vt-scan-report.json to the draft either way. Publish the draft
#                 (`gh release edit --draft=false --latest`) only when the scan
#                 is clean and the draft's assets did not change meanwhile.
#
#   verify <tag>  The release must be PUBLISHED, and its vt-scan-report.json must
#                 say clean and cover the digest of checksums.txt and of every
#                 file checksums.txt lists. The Homebrew cask, the Marketplace
#                 and Open VSX move only after this passes.
#
#   local FILE... Scan release files that are on disk, with no tag and no GitHub
#                 Release (#3064). The files are unpacked and collected exactly
#                 as `scan` does for a draft, and scanned with vt-scan.sh.
#                 Nothing is uploaded or published. staging.yml runs it over a
#                 release candidate's archives and VSIXs, so a flagged build
#                 fails the RC instead of reaching the soak.
#
# The scan reads the draft's own downloaded bytes, never local build outputs:
# what is scanned is exactly what a user will download.
#
# A published release is never scanned or uploaded to: `scan` refuses it, so a
# re-run of release.yml cannot change the files of a public release.
#
# Options:
#   -R, --repo OWNER/REPO  scan/verify: repository. Default: $GITHUB_REPOSITORY.
#   --report PATH          scan/local: also keep the report here (run artifact).
#
# Environment: VT_API_KEY (scan, local), plus vt-scan.sh's optional VT_* settings.
#
# Exit:
#   0  scan: clean and published. verify: published and covered by a clean scan.
#      local: every file is clean.
#   1  scan: a finding, the draft is kept. verify: draft, dirty or not covered.
#      local: a finding.
#   2  HARNESS ERROR: nothing could be decided; nothing was published.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPORT_ASSET="vt-scan-report.json"

USAGE="usage: release-scan-gate.sh scan|verify <tag> [-R OWNER/REPO] [--report PATH]
       release-scan-gate.sh local [--report PATH] FILE..."
MODE="${1:-}"
[ $# -gt 0 ] && shift
TAG=""
REPO="${GITHUB_REPOSITORY:-}"
KEEP_REPORT=""
LOCAL_FILES=()

harness() { echo "HARNESS ERROR: release-scan-gate: $*" >&2; exit 2; }
refuse() { echo "::error::release-scan-gate: $*" >&2; exit 1; }

case "$MODE" in
scan | verify)
  TAG="${1:-}"
  [ $# -gt 0 ] && shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -R | --repo) [ $# -ge 2 ] || harness "$1 needs a value"; REPO="$2"; shift 2 ;;
    --report) [ $# -ge 2 ] || harness "$1 needs a value"; KEEP_REPORT="$2"; shift 2 ;;
    *) harness "unknown argument '$1'" ;;
    esac
  done
  [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || harness "tag must be vX.Y.Z, got '$TAG'"
  [[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || harness "--repo OWNER/REPO (or GITHUB_REPOSITORY) is required"
  command -v gh >/dev/null 2>&1 || harness "gh is required"
  ;;
local)
  while [ $# -gt 0 ]; do
    case "$1" in
    --report) [ $# -ge 2 ] || harness "$1 needs a value"; KEEP_REPORT="$2"; shift 2 ;;
    -*) harness "unknown argument '$1'" ;;
    *) LOCAL_FILES+=("$1"); shift ;;
    esac
  done
  [ "${#LOCAL_FILES[@]}" -gt 0 ] || harness "local: no files given: a scan of nothing is not a clean scan"
  ;;
*) harness "$USAGE" ;;
esac
command -v jq >/dev/null 2>&1 || harness "jq is required"

WORK="$(mktemp -d)" || harness "mktemp failed"
trap 'rm -rf "$WORK"' EXIT

# release_state — prints "draft" or "published", or fails closed.
release_state() {
  local json
  json="$(gh release view "$TAG" -R "$REPO" --json isDraft,assets 2>"$WORK/gh.err")" ||
    harness "cannot read release $TAG of $REPO: $(head -c 300 "$WORK/gh.err")"
  printf '%s' "$json" >"$WORK/release.json"
  case "$(jq -r '.isDraft | tostring' <<<"$json" 2>/dev/null)" in
  true) echo draft ;;
  false) echo published ;;
  *) harness "cannot tell whether release $TAG is a draft" ;;
  esac
}

# asset_fingerprint — the draft's assets other than the report, as
# "name size sha256". Name and size alone miss a same-size replacement, which
# would publish bytes nobody scanned, so an asset without a sha256 digest
# fails closed.
asset_fingerprint() {
  local fp
  fp="$(jq -r --arg r "$REPORT_ASSET" '[.assets[] | select(.name != $r)
      | "\(.name) \(.size) \((.digest // "") | ltrimstr("sha256:"))"] | sort | .[]' \
    "$WORK/release.json" 2>/dev/null)" || harness "unparseable asset list for $TAG"
  while read -r n _ d; do
    [ -n "$n" ] || continue
    [[ "$d" =~ ^[0-9a-f]{64}$ ]] || harness "an asset of $TAG has no sha256 digest, so its bytes cannot be pinned"
  done <<<"$fp"
  printf '%s\n' "$fp"
}

is_binary() {
  local f="$1" magic
  [ -x "$f" ] && return 0
  magic="$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n')"
  case "$magic" in
  7f454c46 | feedface | feedfacf | cefaedfe | cffaedfe | cafebabe | 4d5a*) return 0 ;;
  esac
  return 1
}

# collect_scan_set — run inside $WORK/scan with the release files in assets/.
# Writes $LIST: every asset, plus every binary and JS bundle inside each .vsix
# and .tar.gz. Shared by `scan` and `local`, so a release candidate and a
# draft are collected the same way.
LIST="$WORK/files.txt"
collect_scan_set() {
  local a name dest bins bundles f
  : >"$LIST"
  for a in assets/*; do
    [ -f "$a" ] || continue
    echo "$a" >>"$LIST"
    name="$(basename "$a")"
    dest="extracted/$name"
    case "$name" in
    *.vsix) mkdir -p "$dest"; unzip -qo "$a" -d "$dest" 2>"$WORK/x.err" ||
      harness "cannot unpack $name: $(head -c 200 "$WORK/x.err")" ;;
    *.tar.gz) mkdir -p "$dest"; tar -xzf "$a" -C "$dest" 2>"$WORK/x.err" ||
      harness "cannot unpack $name: $(head -c 200 "$WORK/x.err")" ;;
    *) continue ;;
    esac
    bins=0 bundles=0
    while IFS= read -r f; do
      case "$f" in
      *.js | *.cjs | *.mjs) echo "$f" >>"$LIST"; bundles=$((bundles + 1)) ;;
      *) is_binary "$f" && { echo "$f" >>"$LIST"; bins=$((bins + 1)); } ;;
      esac
    done < <(find "$dest" -type f | LC_ALL=C sort)
    # An archive that yields nothing to scan means extraction or selection broke,
    # and the scan would be green having looked inside nothing (#2772).
    [ "$bins" -gt 0 ] || harness "$name yielded no binary to scan"
    case "$name" in *.vsix) [ "$bundles" -gt 0 ] || harness "$name yielded no JS bundle to scan" ;; esac
    echo "  $name: $bins binar$([ "$bins" -eq 1 ] && echo y || echo ies), $bundles JS bundle(s)"
  done
}

# run_vt_scan — scan $LIST with vt-scan.sh into $REPORT; sets SCAN_RC.
REPORT="$WORK/$REPORT_ASSET"
run_vt_scan() {
  local f files=()
  while IFS= read -r f; do files+=("$f"); done <"$LIST"
  bash "$SCRIPT_DIR/vt-scan.sh" --report "$REPORT" "${files[@]}"
  SCAN_RC=$?
  if [ -n "$KEEP_REPORT" ] && [ -f "$REPORT" ]; then
    cp "$REPORT" "$KEEP_REPORT" || echo "::warning::could not keep the report at $KEEP_REPORT" >&2
  fi
}

# ============================================================================
if [ "$MODE" = "local" ]; then
  # The basename is the asset name, as on a release, so two files sharing one
  # would silently scan only the last: refuse instead.
  mkdir -p "$WORK/scan/assets" "$WORK/scan/extracted"
  for f in "${LOCAL_FILES[@]}"; do
    [ -f "$f" ] || harness "local: $f is not a file"
    [ ! -e "$WORK/scan/assets/$(basename "$f")" ] ||
      harness "local: two files are named $(basename "$f")"
    cp "$f" "$WORK/scan/assets/" || harness "local: cannot copy $f"
  done
  cd "$WORK/scan" || harness "cannot enter the scan directory"
  collect_scan_set
  [ -s "$LIST" ] || harness "local: no files to scan"
  run_vt_scan
  cd - >/dev/null || true
  if [ "$SCAN_RC" -ne 0 ]; then
    echo "::error::release-scan-gate: local scan failed (vt-scan exit $SCAN_RC). Read the report." >&2
    exit "$SCAN_RC"
  fi
  echo "release-scan-gate: every local file is clean (${#LOCAL_FILES[@]} given)"
  exit 0
fi

# ============================================================================
if [ "$MODE" = "verify" ]; then
  STATE="$(release_state)" || exit $?
  [ "$STATE" = "published" ] || refuse "$TAG is still a draft: no channel may move until it is published"
  mkdir -p "$WORK/dl"
  gh release download "$TAG" -R "$REPO" -D "$WORK/dl" -p "$REPORT_ASSET" -p checksums.txt \
    >/dev/null 2>"$WORK/gh.err" || {
    grep -qi 'no assets\|not found\|404' "$WORK/gh.err" &&
      refuse "$TAG has no $REPORT_ASSET or checksums.txt: it was never scanned by this gate"
    harness "cannot download $REPORT_ASSET and checksums.txt from $TAG: $(head -c 300 "$WORK/gh.err")"
  }
  [ -f "$WORK/dl/$REPORT_ASSET" ] || refuse "$TAG has no $REPORT_ASSET: it was never scanned by this gate"
  [ -s "$WORK/dl/checksums.txt" ] || refuse "$TAG has no checksums.txt"
  R="$WORK/dl/$REPORT_ASSET"
  jq -e '.files | type == "array"' "$R" >/dev/null 2>&1 || harness "$REPORT_ASSET of $TAG does not parse"
  STATUS="$(jq -r '.status' "$R")"
  [ "$STATUS" = "clean" ] || refuse "$REPORT_ASSET of $TAG says '$STATUS', not clean"
  DIRTY="$(jq -r '.files[] | select((.findings | length) > 0 or .malicious > 0 or .suspicious > 0)
                   | "\(.file) (\(.sha256))"' "$R")"
  [ -z "$DIRTY" ] || refuse "$REPORT_ASSET of $TAG lists findings for: $DIRTY"
  WANT="$(awk '{ print $1 }' "$WORK/dl/checksums.txt")
$(shasum -a 256 "$WORK/dl/checksums.txt" | cut -d' ' -f1)"
  MISSING=""
  for d in $WANT; do
    [[ "$d" =~ ^[0-9a-f]{64}$ ]] || harness "checksums.txt of $TAG has a line that is not a sha256"
    jq -e --arg d "$d" 'any(.files[]; .sha256 == $d)' "$R" >/dev/null || MISSING+=" $d"
  done
  [ -z "$MISSING" ] || refuse "$REPORT_ASSET of $TAG does not cover:$MISSING"
  echo "release-scan-gate: $TAG is published and every file is covered by a clean VirusTotal scan"
  exit 0
fi

# ============================================================================
# scan
STATE="$(release_state)" || exit $?
[ "$STATE" = "draft" ] ||
  harness "$TAG is already published: refusing to scan or upload to a public release (run 'verify')"
BEFORE="$(asset_fingerprint)" || exit $?
[ -n "$BEFORE" ] || harness "draft $TAG has no assets: a scan of nothing is not a clean scan"

ASSETS="$WORK/scan/assets"
mkdir -p "$ASSETS" "$WORK/scan/extracted"
gh release download "$TAG" -R "$REPO" -D "$ASSETS" >/dev/null 2>"$WORK/gh.err" ||
  harness "cannot download the assets of draft $TAG: $(head -c 300 "$WORK/gh.err")"
rm -f "$ASSETS/$REPORT_ASSET"
# The bytes scanned must be the bytes the draft pinned when the scan started.
while read -r n _ d; do
  [ -f "$ASSETS/$n" ] || harness "asset $n of draft $TAG was not downloaded"
  [ "$(shasum -a 256 "$ASSETS/$n" | cut -d' ' -f1)" = "$d" ] ||
    harness "downloaded $n does not match its sha256 on draft $TAG; re-run the scan"
done <<<"$BEFORE"

cd "$WORK/scan" || harness "cannot enter the scan directory"
collect_scan_set
[ -s "$LIST" ] || harness "draft $TAG has no files to scan"

run_vt_scan
cd - >/dev/null || true

if [ "$SCAN_RC" -ne 0 ]; then
  # Best effort: the report on the draft is what the owner reads (GIT_WORKFLOW
  # step 9a). The draft stays a draft whether or not this upload works.
  if [ -f "$REPORT" ]; then
    gh release upload "$TAG" "$REPORT" --clobber -R "$REPO" >/dev/null 2>&1 ||
      echo "::warning::could not attach $REPORT_ASSET to draft $TAG" >&2
  fi
  if [ "$SCAN_RC" -eq 1 ]; then
    echo "::error::release-scan-gate: $TAG stays a DRAFT (vt-scan exit 1: finding). Read $REPORT_ASSET; fix the finding at its cause and ship the next patch; nothing publishes this draft." >&2
  else
    echo "::error::release-scan-gate: $TAG stays a DRAFT (vt-scan exit $SCAN_RC: harness error, nothing was decided about the files). Re-run the scan (gh run rerun <run-id> --failed reuses the draft); never publish this draft." >&2
  fi
  exit "$SCAN_RC"
fi

gh release upload "$TAG" "$REPORT" --clobber -R "$REPO" >/dev/null 2>"$WORK/gh.err" ||
  harness "cannot attach $REPORT_ASSET to draft $TAG, so it stays a draft: $(head -c 300 "$WORK/gh.err")"

# The bytes scanned must be the bytes published: refuse if the draft changed.
STATE="$(release_state)" || exit $?
[ "$STATE" = "draft" ] || harness "$TAG stopped being a draft during the scan"
AFTER="$(asset_fingerprint)" || exit $?
[ "$BEFORE" = "$AFTER" ] || harness "the assets of draft $TAG changed during the scan; re-run the scan"

gh release edit "$TAG" -R "$REPO" --draft=false --latest >/dev/null 2>"$WORK/gh.err" ||
  harness "every file is clean but publishing $TAG failed: $(head -c 300 "$WORK/gh.err")"
echo "release-scan-gate: every file of $TAG is clean; the release is published"
exit 0
