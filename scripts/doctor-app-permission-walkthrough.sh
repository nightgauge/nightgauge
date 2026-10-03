#!/usr/bin/env bash
# The #1955 GitHub App permission sequence, walked against a real App, with
# doctor's answer checked at every state (#2094's manual verification).
#
#   scripts/doctor-app-permission-walkthrough.sh [--record] [--out <dir>]
#
# Run it from a checkout whose Nightgauge config authenticates as a GitHub App
# (`github_auth.app`) installed on an organization, and whose board is that
# organization's project. It changes nothing itself: each step says what to
# change on GitHub, waits for Enter, then runs
#
#   nightgauge doctor --only github_identity,board_population --json
#
# and checks that doctor names the state it is in:
#
#   0. baseline   every permission granted              no App or board finding
#   1. removed    Organization → Projects removed       NGD036 for organization_projects
#   2. restored   Projects restored, not yet accepted   NGD037 for organization_projects
#   3. accepted   an owner accepted the change          no App or board finding
#
# In states 1 and 2 the board check, if it reports anything, must report
# NGD041 (the identity cannot see projects), never the raw ProjectV2 failure.
# State 3 passing also shows the App token cache was replaced without anyone
# deleting `github-app-token-*.json` by hand.
#
# Between steps 1 and 3 the App cannot read or write the board, so stop
# autonomous mode first (`nightgauge autonomous stop`) and finish the walk.
#
# --record also records #2095's guided-repair session in state 1: with vhs,
# from scripts/doctor-guided-repair.tape (a GIF and an MP4); without vhs but
# with asciinema, as a cast you drive by hand. Neither installed: the walk
# goes on and says so.
#
# Output (default: a new directory under $TMPDIR): transcript.md, one section
# per state with the codes doctor reported and the verdict, ready to paste
# into #2094, plus each state's doctor JSON. The JSON is doctor's own redacted
# output; no token is read or written here.
#
# NIGHTGAUGE_BIN names the binary (default: `nightgauge` on PATH).
#
# Exit: 0 every state read as expected; 1 a state did not (the transcript
# says which); 2 the walk could not run (no binary, no python3, or doctor
# printed no JSON).
set -uo pipefail

BIN="${NIGHTGAUGE_BIN:-nightgauge}"
RECORD=0
OUT=""
CHECKS="github_identity,board_population"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TAPE="$HERE/doctor-guided-repair.tape"

usage() {
  sed -n '2,/^set -uo pipefail/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --record) RECORD=1 ;;
    --out)
      [ $# -ge 2 ] || { echo "--out needs a directory" >&2; exit 2; }
      OUT="$2"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option $1 (see --help)" >&2
      exit 2
      ;;
  esac
  shift
done

command -v "$BIN" >/dev/null 2>&1 || { echo "cannot run: $BIN is not on PATH (set NIGHTGAUGE_BIN)" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "cannot run: python3 is not on PATH" >&2; exit 2; }

if [ -z "$OUT" ]; then
  OUT="$(mktemp -d "${TMPDIR:-/tmp}/nightgauge-app-walkthrough.XXXXXX")" || exit 2
fi
mkdir -p "$OUT" || exit 2
TRANSCRIPT="$OUT/transcript.md"
CHECKOUT="$(pwd)"
FAILED=0

{
  echo "## #2094 App permission walkthrough"
  echo
  echo "- binary: \`$("$BIN" --version 2>/dev/null || echo "$BIN")\`"
  echo "- checks: \`$CHECKS\`"
  echo "- started: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$TRANSCRIPT"

pause() {
  echo
  printf '%s\n' "$@"
  printf 'Press Enter when done (Ctrl-C stops the walk). '
  read -r _ || { echo; echo "stopped: no more input" >&2; exit 2; }
}

# judge <state> <json file>: prints the verdict and the codes; exit 0 when the
# state reads as expected, 1 when it does not, 2 when the JSON is unusable.
judge() {
  python3 - "$1" "$2" <<'PY'
import json, sys

state, path = sys.argv[1], sys.argv[2]
try:
    with open(path, encoding="utf-8") as fh:
        doc = json.load(fh)
    findings = doc["findings"]
except (OSError, ValueError, KeyError, TypeError) as err:
    print(f"doctor printed no usable JSON v2 ({err})")
    sys.exit(2)

APP = {"NGD036", "NGD037", "NGD038", "NGD039"}
BOARD = {"NGD013", "NGD040", "NGD041", "NGD042"}
app = [f for f in findings if f.get("code") in APP]
board = [f for f in findings if f.get("code") in BOARD]

def projects(f):
    return (f.get("evidence") or {}).get("permission") == "organization_projects"

def links(f):
    return [u for r in f.get("remedies") or [] for u in r.get("links") or []]

for f in app + board:
    print(f"  {f.get('code')} {f.get('severity')}: {f.get('title')}")
    for u in links(f):
        print(f"    link: {u}")

problems = []
if state in ("baseline", "accepted"):
    if app or board:
        problems.append("expected no App or board finding")
else:
    want = {"removed": "NGD036", "restored": "NGD037"}[state]
    other = {"NGD036": "NGD037", "NGD037": "NGD036"}[want]
    hits = [f for f in app if f.get("code") == want and projects(f)]
    if not hits:
        problems.append(f"expected {want} for organization_projects")
    else:
        got = links(hits[0])
        if not got or not all(u.startswith("https://github.com/") for u in got):
            problems.append(f"{want} carries no github.com link")
    if any(f.get("code") == other and projects(f) for f in app):
        problems.append(f"{other} reported too: the two states are not told apart")
    if any(f.get("code") != "NGD041" for f in board):
        problems.append("a board finding other than NGD041: the ProjectV2 failure was not re-diagnosed")

if problems:
    print("FAIL: " + "; ".join(problems))
    sys.exit(1)
print("PASS")
PY
}

# check <state> <title>: runs doctor, records the state, and judges it.
check() {
  local state="$1" title="$2" json="$OUT/$1.json" verdict rc
  echo
  echo "== $title: running doctor --only $CHECKS"
  "$BIN" doctor --only "$CHECKS" --json >"$json" 2>"$OUT/$1.stderr"
  verdict="$(judge "$state" "$json")"
  rc=$?
  printf '%s\n' "$verdict"
  {
    echo
    echo "### $title"
    echo
    echo '```text'
    printf '%s\n' "$verdict"
    echo '```'
  } >>"$TRANSCRIPT"
  if [ "$rc" -eq 2 ]; then
    echo "cannot go on: see $OUT/$1.stderr" >&2
    exit 2
  fi
  [ "$rc" -eq 0 ] || FAILED=1
  return "$rc"
}

# links_of <state>: the github.com links doctor gave in that state, one per line.
links_of() {
  python3 - "$OUT/$1.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as fh:
    doc = json.load(fh)
seen = []
for f in doc.get("findings", []):
    for r in f.get("remedies") or []:
        for u in r.get("links") or []:
            if u.startswith("https://github.com/") and u not in seen:
                seen.append(u)
print("\n".join(seen))
PY
}

record() {
  if command -v vhs >/dev/null 2>&1; then
    local dir
    dir="$(mktemp -d "${TMPDIR:-/tmp}/nightgauge-doctor-recording.XXXXXX")" || return 0
    echo "Recording the guided-repair session with vhs (about a minute; do not touch the keyboard)."
    # The tape types `nightgauge`: put this walk's binary first on its PATH.
    if (cd "$dir" && PATH="$(dirname "$(command -v "$BIN")"):$PATH" \
      NIGHTGAUGE_RECORD_CHECKOUT="$CHECKOUT" vhs "$TAPE"); then
      cp "$dir"/doctor-guided-repair.* "$OUT"/ 2>/dev/null
      echo "Recorded: $OUT/doctor-guided-repair.gif and .mp4 (attach one to #2095)."
      echo "- #2095 recording: \`$OUT/doctor-guided-repair.gif\`" >>"$TRANSCRIPT"
    else
      echo "vhs failed; the walk goes on without a recording." >&2
    fi
  elif command -v asciinema >/dev/null 2>&1; then
    pause "Recording with asciinema. In the session press: d (details), c (check again), s (skip)." \
      "It ends by itself after the report."
    asciinema rec --overwrite -c "$BIN doctor --only github_identity" "$OUT/doctor-guided-repair.cast"
    echo "- #2095 recording: \`$OUT/doctor-guided-repair.cast\`" >>"$TRANSCRIPT"
  else
    echo "No recorder: install vhs (brew install vhs) or asciinema, then run again with --record." >&2
  fi
}

check baseline "0. baseline" || {
  echo
  echo "The baseline is not clean, so the sequence would prove nothing. Fix it first."
  echo "Transcript: $TRANSCRIPT"
  exit 1
}

pause "1. Remove the permission. On GitHub: the organization's Settings → Developer settings →" \
  "   GitHub Apps → the App → Permissions & events → Organization permissions → Projects:" \
  "   set it to 'No access' and save. (A removal applies at once; nothing to accept.)"
if check removed "1. Projects removed from the App" && [ "$RECORD" -eq 1 ]; then
  record
fi

app_links="$(links_of removed)"
pause "2. Restore the permission, and do NOT accept it yet. Set Projects to 'Read and write'" \
  "   and save. Doctor's links for this state:" "${app_links:-   (doctor gave none)}"
check restored "2. Projects restored, not yet accepted"

install_links="$(links_of restored)"
pause "3. Accept the change. An organization owner opens the installation and accepts the" \
  "   requested permissions. Doctor's link for this state:" "${install_links:-   (doctor gave none)}"
check accepted "3. accepted on the installation"

echo
if [ "$FAILED" -eq 0 ]; then
  echo "Every state read as expected." | tee -a "$TRANSCRIPT"
else
  echo "At least one state did not read as expected; see above." | tee -a "$TRANSCRIPT"
fi
echo "Transcript: $TRANSCRIPT (paste it into #2094)"
exit "$FAILED"
