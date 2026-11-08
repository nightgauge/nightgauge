#!/usr/bin/env bash
# Offline harness for scripts/release-scan-gate.sh (#2946, AC7).
#
# Proves, without GitHub or VirusTotal, the two paths the release gate exists
# for: a forced detection leaves the draft a draft, and a clean scan publishes
# it exactly once. `gh` is a stub on PATH serving a fake release from a
# directory and logging every upload and edit; the VirusTotal API is the stub in
# scripts/testdata/vt-scan/stub-api.py, and the real scripts/vt-scan.sh runs
# against it.
#
# The forced detection sits in a JS bundle inside a deflated VSIX, so its marker
# is not visible in the VSIX's own bytes: the arm goes red unless the gate
# extracts the VSIX and scans what is inside.
#
# This is not the real dry run on a throwaway tag (AC7), which needs a fork,
# a VT_API_KEY secret there and a tag push. It is the offline proof of both paths.
#
# Run: bash scripts/test-release-scan-gate.sh   (also run by scripts/ci-local.sh)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="$SCRIPT_DIR/release-scan-gate.sh"
FIXTURES="$SCRIPT_DIR/testdata/vt-scan"
PASS=0
FAIL=0
ok() { echo "  ok   $1"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL $1" >&2; FAIL=$((FAIL + 1)); }

KEY="StubKey0123456789LeakCanaryAbCdEf0123456789"
TAG="v9.9.9"
WORK="$(mktemp -d)"
STUB_PID=""
cleanup() {
  if [ -n "$STUB_PID" ]; then
    kill "$STUB_PID" 2>/dev/null
    wait "$STUB_PID" 2>/dev/null
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

command -v python3 >/dev/null 2>&1 || { echo "HARNESS ERROR: python3 is required"; exit 2; }

VTSTATE="$WORK/vt"
mkdir -p "$VTSTATE" "$WORK/bin"
STUB_VT_KEY="$KEY" python3 -I "$FIXTURES/stub-api.py" "$FIXTURES" "$VTSTATE" &
STUB_PID=$!
for _ in $(seq 1 100); do [ -s "$VTSTATE/port" ] && break; sleep 0.1; done
[ -s "$VTSTATE/port" ] || { echo "HARNESS ERROR: the VirusTotal stub did not start"; exit 2; }
VT_API_BASE="http://127.0.0.1:$(cat "$VTSTATE/port")/api/v3"
export VT_API_BASE

# --- stub gh: a release is a directory of assets plus a draft flag ------------
REL="$WORK/release"
cat >"$WORK/bin/gh" <<GH
#!/usr/bin/env bash
rel='$REL'
GH
cat >>"$WORK/bin/gh" <<'GH'
[ "$1" = "release" ] || exit 1
sub="$2"; shift 2
dir="" pats=() files=()
while [ $# -gt 0 ]; do
  case "$1" in
  -R | --json | --jq) shift 2 ;;
  -D) dir="$2"; shift 2 ;;
  -p) pats+=("$2"); shift 2 ;;
  --clobber | --latest | --draft=false) [ "$1" = "--draft=false" ] && files+=("$1"); shift ;;
  v*) shift ;;
  *) files+=("$1"); shift ;;
  esac
done
[ -e "$rel/fail-$sub" ] && { echo "gh: stub failure (HTTP 502)" >&2; exit 1; }
case "$sub" in
view)
  [ -d "$rel/assets" ] || { echo "release not found" >&2; exit 1; }
  draft=false; [ -e "$rel/draft" ] && draft=true
  ( cd "$rel/assets" && for f in *; do [ -f "$f" ] || continue; d="sha256:$(shasum -a 256 "$f" | cut -d' ' -f1)"; [ -e "$rel/bad-digest" ] && d="sha256:$(printf '0%.0s' $(seq 64))"; [ -e "$rel/no-digest" ] && d=""; printf '{"name":"%s","size":%s,"digest":"%s"}\n' "$f" "$(wc -c <"$f" | tr -d ' ')" "$d"; done ) |
    jq -sc --argjson d "$draft" '{isDraft: $d, assets: .}'
  ;;
download)
  n=0
  for f in "$rel/assets"/*; do
    [ -f "$f" ] || continue
    b="$(basename "$f")" hit=0
    [ "${#pats[@]}" -eq 0 ] && hit=1
    for p in ${pats[@]+"${pats[@]}"}; do [ "$b" = "$p" ] && hit=1; done
    [ "$hit" -eq 1 ] && { cp "$f" "$dir/"; n=$((n + 1)); }
  done
  [ "$n" -gt 0 ] || { echo "no assets match the file pattern" >&2; exit 1; }
  ;;
upload)
  echo "upload ${files[*]}" >>"$rel/writes.log"
  for f in "${files[@]}"; do cp "$f" "$rel/assets/"; done
  ;;
edit)
  echo "edit ${files[*]}" >>"$rel/writes.log"
  rm -f "$rel/draft"
  ;;
*) exit 1 ;;
esac
GH
chmod +x "$WORK/bin/gh"

# make_release <draft|published> [bundle-marker]
# Two VSIXs whose extension.cjs is identical (the dedup case) and whose
# sdk-cli.cjs carries the marker; one tarball; checksums.txt over all three.
make_release() {
  rm -rf "$REL"
  mkdir -p "$REL/assets" "$WORK/src"
  : >"$REL/writes.log"
  [ "$1" = "draft" ] && touch "$REL/draft"
  python3 -I - "$REL/assets" "${2:-}" <<'PY'
import io, os, sys, tarfile, zipfile
out, marker = sys.argv[1], sys.argv[2]
elf = lambda tag: b"\x7fELF" + tag.encode() + b"\0" * 64
for target in ("darwin-arm64", "linux-x64"):
    with zipfile.ZipFile(f"{out}/nightgauge-{target}-9.9.9.vsix", "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("extension/package.json", "{}")
        z.writestr("extension/dist/extension.cjs", "module.exports = 1;\n" * 50)
        z.writestr("extension/dist/sdk-cli.cjs", ("cli(); " + marker + "\n") * 50)
        z.writestr("extension/dist/bin/nightgauge", elf("vsix-" + target))
with tarfile.open(f"{out}/nightgauge_9.9.9_linux_amd64.tar.gz", "w:gz") as t:
    for name, data in (("LICENSE", b"Apache"), ("nightgauge", elf("tar"))):
        info = tarfile.TarInfo(name); info.size = len(data); info.mode = 0o755
        t.addfile(info, io.BytesIO(data))
PY
  (cd "$REL/assets" && shasum -a 256 -- *.vsix *.tar.gz >checksums.txt)
}

OUT=""
RC=0
ALL_OUT="$WORK/all-output.log"
: >"$ALL_OUT"
gate() {
  rm -f "$VTSTATE/uploads.log"
  OUT="$(env PATH="$WORK/bin:$PATH" VT_API_KEY="$KEY" VT_MIN_INTERVAL=0 VT_ANALYSIS_TIMEOUT=2 \
    bash "$SUT" "$@" -R acme/widget 2>&1)"
  RC=$?
  printf '%s\n' "$OUT" >>"$ALL_OUT"
}
expect() {
  local name="$1" want="$2" needle="${3:-}"
  if [ "$RC" -ne "$want" ]; then
    bad "$name: want exit $want, got $RC"
    printf '%s\n' "$OUT" | sed 's/^/        /' >&2
  elif [ -n "$needle" ] && ! grep -qF -- "$needle" <<<"$OUT"; then
    bad "$name: output lacks '$needle'"
    printf '%s\n' "$OUT" | sed 's/^/        /' >&2
  else
    ok "$name"
  fi
}
edits() { grep -c '^edit --draft=false' "$REL/writes.log" 2>/dev/null; }

echo "release-scan-gate.sh"

# 1. Forced detection inside a VSIX: draft kept, report attached, file named.
make_release draft VT-STUB-MALICIOUS
gate scan "$TAG"
expect "forced detection -> exit 1" 1 "extension/dist/sdk-cli.cjs"
[ -e "$REL/draft" ] && [ "$(edits)" -eq 0 ] && ok "forced detection: the release is still a draft, never edited" ||
  bad "forced detection published the release: $(cat "$REL/writes.log")"
[ "$(jq -r .status "$REL/assets/vt-scan-report.json" 2>/dev/null)" = "findings" ] &&
  ok "forced detection: the findings report is attached to the draft" ||
  bad "forced detection: no findings report on the draft"
grep -qF "StubEngineA: malicious" <<<"$OUT" && ok "forced detection names the engine" || bad "engine not named"

# 2. Clean: published exactly once, after the report is attached.
make_release draft
gate scan "$TAG" --report "$WORK/kept-report.json"
expect "clean scan -> exit 0" 0 "the release is published"
[ ! -e "$REL/draft" ] && [ "$(edits)" -eq 1 ] && ok "clean: --draft=false called once" ||
  bad "clean: edits=$(edits) draft=$([ -e "$REL/draft" ] && echo yes || echo no)"
grep -q '^upload .*vt-scan-report.json' <<<"$(sed -n 1p "$REL/writes.log")" &&
  ok "clean: the report is attached before publishing" || bad "clean: write order $(tr '\n' '|' <"$REL/writes.log")"
[ -s "$WORK/kept-report.json" ] && ok "clean: --report keeps a copy for the run artifact" || bad "no kept report"

# 3. Deduplication: the extension.cjs both VSIXs carry is uploaded once.
EXT_SHA="$(python3 -I -c 'import hashlib;print(hashlib.sha256(b"module.exports = 1;\n"*50).hexdigest())')"
[ "$(grep -c " $EXT_SHA " "$VTSTATE/uploads.log")" -eq 1 ] &&
  ok "identical bundles across VSIXs are scanned once" ||
  bad "extension.cjs uploaded $(grep -c " $EXT_SHA " "$VTSTATE/uploads.log") times"
for want in nightgauge_9.9.9_linux_amd64.tar.gz/nightgauge extension/dist/bin/nightgauge checksums.txt; do
  jq -e --arg w "$want" 'any(.files[].files[]; endswith($w))' "$REL/assets/vt-scan-report.json" >/dev/null &&
    ok "scan set includes $want" || bad "scan set lacks $want"
done

# 4. verify passes the release just published.
gate verify "$TAG"
expect "verify: published and clean -> exit 0" 0 "covered by a clean VirusTotal scan"

# 5. An already-published release: scan uploads nothing and edits nothing.
: >"$REL/writes.log"
gate scan "$TAG"
expect "scan of a published release -> exit 2" 2 "refusing to scan or upload to a public release"
[ ! -s "$REL/writes.log" ] && ok "published release: no upload, no edit" || bad "wrote to a published release"

# 6. Missing key: the draft stays a draft.
make_release draft
OUT="$(env -u VT_API_KEY PATH="$WORK/bin:$PATH" VT_MIN_INTERVAL=0 bash "$SUT" scan "$TAG" -R acme/widget 2>&1)"
RC=$?
expect "missing key -> exit 2" 2 "VT_API_KEY is not set"
[ -e "$REL/draft" ] && [ "$(edits)" -eq 0 ] && ok "missing key: still a draft" || bad "missing key published"

# 6b. Bytes the draft does not pin cannot be published (same-size swap class).
make_release draft
touch "$REL/bad-digest"
gate scan "$TAG"
expect "downloaded bytes differ from the draft digest -> exit 2" 2 "does not match its sha256"
[ -e "$REL/draft" ] && [ "$(edits)" -eq 0 ] && ok "digest mismatch: still a draft" || bad "digest mismatch published"
make_release draft
touch "$REL/no-digest"
gate scan "$TAG"
expect "asset without a digest -> exit 2" 2 "has no sha256 digest"
[ -e "$REL/draft" ] && [ "$(edits)" -eq 0 ] && ok "no digest: still a draft" || bad "no digest published"

# 7. A VirusTotal outage mid-scan: the draft stays a draft.
make_release draft
touch "$VTSTATE/fail-file"
gate scan "$TAG"
rm -f "$VTSTATE/fail-file"
expect "VirusTotal HTTP 500 -> exit 2" 2 "HARNESS ERROR"
[ -e "$REL/draft" ] && [ "$(edits)" -eq 0 ] && ok "outage: still a draft" || bad "outage published"

# 8. The report upload fails on a clean scan: not published.
make_release draft
touch "$REL/fail-upload"
gate scan "$TAG"
rm -f "$REL/fail-upload"
expect "clean but the report cannot be attached -> exit 2" 2 "stays a draft"
[ -e "$REL/draft" ] && ok "report upload failure: still a draft" || bad "published without a report"

# 9. A draft with no assets is not a clean scan.
make_release draft
rm -f "$REL/assets"/*
gate scan "$TAG"
expect "empty draft -> exit 2" 2 "has no assets"

# 10. A VSIX that yields nothing to scan fails closed.
make_release draft
printf 'not a zip' >"$REL/assets/broken.vsix"
gate scan "$TAG"
expect "unreadable VSIX -> exit 2" 2 "cannot unpack broken.vsix"

# 11-15. verify refuses everything but a published, covered, clean release.
make_release draft
gate verify "$TAG"
expect "verify refuses a draft" 1 "still a draft"
rm -f "$REL/draft"
gate verify "$TAG"
expect "verify refuses a release with no report" 1 "never scanned by this gate"
make_release draft VT-STUB-SUSPICIOUS
gate scan "$TAG" >/dev/null
rm -f "$REL/draft"
gate verify "$TAG"
expect "verify refuses a dirty report" 1 "says 'findings', not clean"
make_release draft
gate scan "$TAG"
echo "0000000000000000000000000000000000000000000000000000000000000abc  extra.vsix" >>"$REL/assets/checksums.txt"
gate verify "$TAG"
expect "verify refuses a report missing a checksums digest" 1 "does not cover"
make_release draft
gate scan "$TAG"
jq '.files[0].findings = ["engine X: malicious Y"]' "$REL/assets/vt-scan-report.json" >"$WORK/r.json"
cp "$WORK/r.json" "$REL/assets/vt-scan-report.json"
gate verify "$TAG"
expect "verify refuses a clean-labelled report with a finding" 1 "lists findings"
touch "$REL/fail-view"
gate verify "$TAG"
rm -f "$REL/fail-view"
expect "verify: an unreadable release is exit 2" 2 "cannot read release"

# 17-21. local mode (#3064): the RC's on-disk files, no tag, no GitHub Release.
# Same collection as `scan`, so the marker inside a deflated VSIX bundle is found.
local_gate() {
  rm -f "$VTSTATE/uploads.log"
  OUT="$(env PATH="$WORK/bin:$PATH" VT_API_KEY="$KEY" VT_MIN_INTERVAL=0 VT_ANALYSIS_TIMEOUT=2 \
    bash "$SUT" local "$@" 2>&1)"
  RC=$?
  printf '%s\n' "$OUT" >>"$ALL_OUT"
}
make_release draft
: >"$REL/writes.log"
local_gate --report "$WORK/local-report.json" "$REL"/assets/*.vsix "$REL"/assets/*.tar.gz
expect "local: clean files -> exit 0" 0 "every local file is clean"
[ "$(jq -r .status "$WORK/local-report.json" 2>/dev/null)" = "clean" ] &&
  ok "local: --report keeps the clean report" || bad "local: no clean report kept"
jq -e 'any(.files[].files[]; endswith("nightgauge_9.9.9_linux_amd64.tar.gz/nightgauge"))' \
  "$WORK/local-report.json" >/dev/null &&
  ok "local: the binary inside the tarball is scanned" || bad "local: tarball binary not scanned"
[ ! -s "$REL/writes.log" ] && ok "local: nothing uploaded or edited" || bad "local wrote to a release"

make_release draft VT-STUB-MALICIOUS
local_gate "$REL"/assets/*.vsix "$REL"/assets/*.tar.gz
expect "local: forced detection inside a VSIX -> exit 1" 1 "extension/dist/sdk-cli.cjs"

local_gate
expect "local: no files -> exit 2" 2 "a scan of nothing is not a clean scan"

python3 -I - "$WORK/nobin.tar.gz" <<'PY2'
import io, sys, tarfile
with tarfile.open(sys.argv[1], "w:gz") as t:
    info = tarfile.TarInfo("LICENSE"); info.size = 6; info.mode = 0o644
    t.addfile(info, io.BytesIO(b"Apache"))
PY2
local_gate "$WORK/nobin.tar.gz"
expect "local: an archive with no binary -> exit 2" 2 "nobin.tar.gz yielded no binary to scan"

local_gate "$WORK/does-not-exist.vsix"
expect "local: a missing file -> exit 2" 2 "is not a file"

OUT="$(env -u VT_API_KEY PATH="$WORK/bin:$PATH" VT_MIN_INTERVAL=0 \
  bash "$SUT" local "$REL"/assets/*.tar.gz 2>&1)"
RC=$?
expect "local: missing key -> exit 2" 2 "VT_API_KEY is not set"

# 16. The key never appears in any output.
grep -qF "$KEY" "$ALL_OUT" && bad "the API key appears in the gate's output" ||
  ok "the API key never appears in the gate's output"

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
