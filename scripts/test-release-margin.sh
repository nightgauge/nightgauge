#!/usr/bin/env bash
# Regression suite for the margin scan (#3192):
#   scripts/release-margin-builds.sh   (staging.yml dry-run)
#   scripts/release-rc-margin-check.sh (release.yml release gate)
#
# Offline: `make`, the signing script and `gh` are stubs, so no arm builds Go,
# signs anything or reaches GitHub.
#
# Exit codes: 0 every assertion held; 1 an assertion failed; 2 HARNESS ERROR
# (the check could not run, so it asserted nothing).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILDS="$ROOT/scripts/release-margin-builds.sh"
GATE="$ROOT/scripts/release-rc-margin-check.sh"
PASS=0
FAIL=0

for f in "$BUILDS" "$GATE"; do
  [ -f "$f" ] || { echo "HARNESS ERROR: $f does not exist."; exit 2; }
done
command -v jq >/dev/null 2>&1 || { echo "HARNESS ERROR: jq is not on PATH."; exit 2; }
TMP="$(mktemp -d)" || { echo "HARNESS ERROR: mktemp failed."; exit 2; }
trap 'rm -rf "$TMP"' EXIT

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (expected '$expected', got '$actual')"
    FAIL=$((FAIL + 1))
  fi
}

# --- release-margin-builds.sh: version derivation ---

check "0.8.6-rc.3 margin strings" "0.8.6 0.8.6-rc.4 0.8.7-rc.1" \
  "$(bash "$BUILDS" --print-versions 0.8.6-rc.3 2>/dev/null)"
check "0.9.0-rc.12 margin strings" "0.9.0 0.9.0-rc.13 0.9.1-rc.1" \
  "$(bash "$BUILDS" --print-versions 0.9.0-rc.12 2>/dev/null)"
check "1.0.9-rc.09 is read in base 10" "1.0.9 1.0.9-rc.10 1.0.10-rc.1" \
  "$(bash "$BUILDS" --print-versions 1.0.9-rc.09 2>/dev/null)"
for bad in v0.8.6-rc.3 0.8.6 0.8.6-beta.1 0.8-rc.1 ""; do
  bash "$BUILDS" --print-versions "$bad" >/dev/null 2>&1
  check "'$bad' is a HARNESS ERROR" "2" "$?"
done
bash "$BUILDS" 0.8.6-rc.3 >/dev/null 2>&1
check "a missing OUT_DIR is a HARNESS ERROR" "2" "$?"

# --- release-margin-builds.sh: builds with a stub make and signer ---

STUBS="$TMP/stubs"
mkdir -p "$STUBS"
cat >"$STUBS/make" <<'EOF'
#!/usr/bin/env bash
# Stub make: write placeholder binaries where the real targets would.
TARGET="" VERSION="" BIN_DIR=""
for a in "$@"; do
  case "$a" in
    VERSION=*) VERSION="${a#VERSION=}" ;;
    BIN_DIR=*) BIN_DIR="${a#BIN_DIR=}" ;;
    build-release-targets | darwin-universal) TARGET="$a" ;;
  esac
done
mkdir -p "$BIN_DIR"
case "$TARGET" in
  build-release-targets)
    for t in darwin-arm64 darwin-amd64 linux-amd64; do
      [ "$t" = linux-amd64 ] && [ "$VERSION" = "${STUB_SKIP_LINUX:-}" ] && continue
      echo "$t $VERSION" >"$BIN_DIR/nightgauge-$t"
    done ;;
  darwin-universal) cat "$BIN_DIR"/nightgauge-darwin-a* >"$BIN_DIR/nightgauge-darwin-universal" ;;
  *) echo "stub make: unexpected target in $*" >&2; exit 1 ;;
esac
EOF
# shellcheck disable=SC2016 # the stub expands $1 when it runs
printf '#!/usr/bin/env bash\necho "signed $1" >>"$(dirname "$1")/signed.log"\n' >"$STUBS/sign.sh"
chmod +x "$STUBS/make" "$STUBS/sign.sh"

OUT="$TMP/margin-ok"
MAKE="$STUBS/make" MARGIN_SIGN_SCRIPT="$STUBS/sign.sh" bash "$BUILDS" 0.8.6-rc.3 "$OUT" >"$TMP/ok.log" 2>&1
check "six margin builds exit 0" "0" "$?"
check "six raw margin binaries" "6" "$(find "$OUT" -maxdepth 1 -type f -perm -u+x -name 'nightgauge-margin_*_*' | wc -l | tr -d ' ')"
check "no margin archive is written (#3202)" "0" "$(find "$OUT" -maxdepth 1 -name 'nightgauge-margin_*.tar.gz' | wc -l | tr -d ' ')"
check "manifest names the three strings" "0.8.6 0.8.6-rc.4 0.8.7-rc.1" \
  "$(awk '{print $1}' "$OUT/margin-manifest.txt" | sort -u | tr '\n' ' ' | sed 's/ $//')"
check "manifest has six lines" "6" "$(wc -l <"$OUT/margin-manifest.txt" | tr -d ' ')"
check "the linux file is the linux build of its version" "linux-amd64 0.8.6" \
  "$(cat "$OUT/nightgauge-margin_0.8.6_linux_amd64" 2>/dev/null)"
check "the universal file is the universal build of its version" \
  "$(cat "$OUT/build/0.8.7-rc.1/nightgauge-darwin-universal")" \
  "$(cat "$OUT/nightgauge-margin_0.8.7-rc.1_darwin_universal" 2>/dev/null)"
check "manifest records each file's own sha256" "ok" \
  "$(bad="$(cd "$OUT" && while read -r v f s; do [ "$(shasum -a 256 "$f" | cut -d' ' -f1)" = "$s" ] || echo "bad $v $f"; done <margin-manifest.txt)"; [ -n "$bad" ] && echo bad || echo ok)"
check "every universal file was signed" "3" "$(cat "$OUT"/build/*/signed.log 2>/dev/null | wc -l | tr -d ' ')"

OUT="$TMP/margin-short"
STUB_SKIP_LINUX=0.8.6-rc.4 MAKE="$STUBS/make" MARGIN_SIGN_SCRIPT="$STUBS/sign.sh" \
  bash "$BUILDS" 0.8.6-rc.3 "$OUT" >"$TMP/short.log" 2>&1
check "a missing build fails with fewer than six binaries" "1" "$?"

OUT="$TMP/margin-extra"
mkdir -p "$OUT" && : >"$OUT/nightgauge-margin_stale_linux_amd64"
MAKE="$STUBS/make" MARGIN_SIGN_SCRIPT="$STUBS/sign.sh" bash "$BUILDS" 0.8.6-rc.3 "$OUT" >/dev/null 2>&1
check "a stray margin file in OUT_DIR fails the count" "1" "$?"

printf '#!/usr/bin/env bash\nexit 1\n' >"$STUBS/sign-fail.sh"
MAKE="$STUBS/make" MARGIN_SIGN_SCRIPT="$STUBS/sign-fail.sh" bash "$BUILDS" 0.8.6-rc.3 "$TMP/margin-nosign" >/dev/null 2>&1
check "a signing failure fails the step" "1" "$?"

# --- release-rc-margin-check.sh: a stub gh over fixture JSON ---

SHA=1111111111111111111111111111111111111111
OTHER=2222222222222222222222222222222222222222
REPO=acme/widget
mkdir -p "$TMP/bin"
cat >"$TMP/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Stub gh: answers from $FX. A missing fixture is an API error.
FX="${FX:?}"
[ -f "$FX/fail" ] && { echo "HTTP 503" >&2; exit 1; }
if [ "$1" = api ]; then
  path="${2%%\?*}"
  case "$path" in
    repos/*/git/matching-refs/tags/*) f=refs.json ;;
    repos/*/git/tags/*) f="tag-${path##*/}.json" ;;
    repos/*/actions/workflows/staging.yml/runs) f=runs.json ;;
    repos/*/actions/runs/*/jobs) id="${path%/jobs}"; f="jobs-${id##*/}.json" ;;
    repos/*/actions/runs/*/artifacts) id="${path%/artifacts}"; f="artifacts-${id##*/}.json" ;;
    *) echo "stub gh: unexpected api $2" >&2; exit 1 ;;
  esac
  [ -f "$FX/$f" ] || { echo "HTTP 404 $f" >&2; exit 1; }
  cat "$FX/$f"
  exit 0
fi
if [ "$1 $2" = "run download" ]; then
  id="$3" dest=""
  while [ $# -gt 0 ]; do [ "$1" = -D ] && dest="$2"; shift; done
  [ -d "$FX/artifact-$id" ] || { echo "no artifact" >&2; exit 1; }
  mkdir -p "$dest" && cp "$FX/artifact-$id"/* "$dest/"
  exit 0
fi
echo "stub gh: unexpected $*" >&2
exit 1
EOF
chmod +x "$TMP/bin/gh"

# fixture NAME: a passing world — v0.8.6-rc.4 (annotated) on $SHA, run 42,
# rc-scan successful, artifact present with a manifest naming 0.8.6.
fixture() {
  local d="$TMP/fx-$1"
  mkdir -p "$d/artifact-42"
  cat >"$d/refs.json" <<JSON
[{"ref":"refs/tags/v0.8.6-rc.3","object":{"type":"commit","sha":"$OTHER"}},
 {"ref":"refs/tags/v0.8.6-rc.4","object":{"type":"tag","sha":"aaaa"}},
 {"ref":"refs/tags/v0.8.6-rc.40x","object":{"type":"commit","sha":"$SHA"}}]
JSON
  echo "{\"object\":{\"type\":\"commit\",\"sha\":\"$SHA\"}}" >"$d/tag-aaaa.json"
  cat >"$d/runs.json" <<JSON
{"workflow_runs":[{"id":41,"run_number":7,"head_branch":"v0.8.6-rc.4"},
 {"id":42,"run_number":8,"head_branch":"v0.8.6-rc.4"},
 {"id":43,"run_number":9,"head_branch":"main"}]}
JSON
  echo '{"jobs":[{"name":"Build all artifacts (no publish)","conclusion":"success"},{"name":"VirusTotal scan of the RC","conclusion":"success"}]}' >"$d/jobs-42.json"
  echo '{"artifacts":[{"name":"rc-margin-scan-v0.8.6-rc.4","expired":false}]}' >"$d/artifacts-42.json"
  printf '0.8.6 a x\n0.8.6-rc.5 b y\n0.8.7-rc.1 c z\n' >"$d/artifact-42/margin-manifest.txt"
  echo "$d"
}

gate() { FX="$1" PATH="$TMP/bin:$PATH" bash "$GATE" "${2:-v0.8.6}" "${3:-$SHA}" "$REPO" >"$TMP/gate.log" 2>&1; echo "$?"; }

FX_DIR="$(fixture pass)"
check "an RC on the commit with a margin-clean scan passes" "0" "$(gate "$FX_DIR")"
check "the pass names the RC and its run" "1" "$(grep -c 'OK: v0.8.6-rc.4 .* run 42' "$TMP/gate.log")"

check "no RC on the commit fails" "1" "$(gate "$FX_DIR" v0.8.6 3333333333333333333333333333333333333333)"
grep -q "no RC tag" "$TMP/gate.log"
check "the no-RC failure says so" "0" "$?"

FX_DIR="$(fixture noref)"; echo '[]' >"$FX_DIR/refs.json"
check "a tag with no RC tags at all fails" "1" "$(gate "$FX_DIR")"

FX_DIR="$(fixture scanfail)"
sed -i.bak 's/"VirusTotal scan of the RC","conclusion":"success"/"VirusTotal scan of the RC","conclusion":"failure"/' "$FX_DIR/jobs-42.json"
check "a failed rc-scan fails" "1" "$(gate "$FX_DIR")"
grep -q "is 'failure'" "$TMP/gate.log"
check "the failure names the rc-scan conclusion" "0" "$?"

FX_DIR="$(fixture noscan)"
echo '{"jobs":[{"name":"Build all artifacts (no publish)","conclusion":"success"}]}' >"$FX_DIR/jobs-42.json"
check "a run without rc-scan fails" "1" "$(gate "$FX_DIR")"

FX_DIR="$(fixture norun)"; echo '{"workflow_runs":[]}' >"$FX_DIR/runs.json"
check "an RC with no staging run fails" "1" "$(gate "$FX_DIR")"

FX_DIR="$(fixture expired)"
echo '{"artifacts":[{"name":"rc-margin-scan-v0.8.6-rc.4","expired":true}]}' >"$FX_DIR/artifacts-42.json"
check "an expired margin artifact fails" "1" "$(gate "$FX_DIR")"
grep -q "is expired" "$TMP/gate.log"
check "the expiry failure says expired" "0" "$?"

FX_DIR="$(fixture noartifact)"; echo '{"artifacts":[]}' >"$FX_DIR/artifacts-42.json"
check "a missing margin artifact fails" "1" "$(gate "$FX_DIR")"

FX_DIR="$(fixture uncovered)"
printf '0.8.5 a x\n0.8.6-rc.5 b y\n' >"$FX_DIR/artifact-42/margin-manifest.txt"
check "a manifest without the release string fails" "1" "$(gate "$FX_DIR")"
grep -q "did not cover 0.8.6" "$TMP/gate.log"
check "the coverage failure names the version" "0" "$?"

FX_DIR="$(fixture nomanifest)"; rm "$FX_DIR/artifact-42/margin-manifest.txt"; : >"$FX_DIR/artifact-42/other"
check "an artifact without a manifest fails" "1" "$(gate "$FX_DIR")"

FX_DIR="$(fixture ghdown)"; : >"$FX_DIR/fail"
check "a gh error is a HARNESS ERROR" "2" "$(gate "$FX_DIR")"
grep -q "HARNESS ERROR" "$TMP/gate.log"
check "the gh error prints HARNESS ERROR" "0" "$?"

FX_DIR="$(fixture jobsdown)"; rm "$FX_DIR/jobs-42.json"
check "a gh error mid-check is a HARNESS ERROR, not a pass or a fail" "2" "$(gate "$FX_DIR")"

FX_DIR="$(fixture dldown)"; rm -r "$FX_DIR/artifact-42"
check "a failed artifact download is a HARNESS ERROR" "2" "$(gate "$FX_DIR")"

check "an RC tag is not a release tag" "2" "$(gate "$(fixture rctag)" v0.8.6-rc.4)"
check "a short sha is a HARNESS ERROR" "2" "$(gate "$(fixture short)" v0.8.6 1111111)"

echo ""
echo "release margin suite: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
