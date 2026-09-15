#!/usr/bin/env bash
# adapter-canary.sh — the scheduled latest-CLI canary's implementation (#1639).
#
# Subcommands, each independently testable (scripts/test-adapter-canary.sh):
#
#   install <adapter> <version>          Install <adapter>'s CLI at <version>
#                                         ("latest" resolves npm's dist-tag)
#                                         into a throwaway prefix. Prints
#                                         `version=<resolved>` then
#                                         `bin=<path>` on stdout.
#   capture-help <adapter> <bin> <dir>   Capture `<bin> [<sub>] --help` into
#                                         dir/<adapter>[-<sub>]-<version>.txt,
#                                         the shape flag_contract_test.go's
#                                         NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR
#                                         override reads. Also carries the
#                                         committed testdata/cli-help sidecar
#                                         (<capture>.txt.hidden) over onto the
#                                         fresh capture whatever version it was
#                                         itself probed on (#1617, #1639 round
#                                         3): a hidden flag is judged on the
#                                         evidence already probed for it, not
#                                         auto-failed for the CLI being newer.
#   flag-contract <dir>                  Run TestFlagContract once against
#                                         every adapter's capture in dir (a
#                                         shared directory captured for every
#                                         manifest CLI). Prints one JSON row
#                                         per adapter: pass, or fail naming the
#                                         offending flag; plus one more
#                                         "flag-contract" row on a genuine
#                                         failure the suite could not attribute
#                                         to a single adapter.
#   opencode-canary <bin> [version]      Run the #1639 OpenCode leg
#                                         (go test -tags canary ./internal/execution
#                                         -run TestOpenCodeCanary) with <bin>
#                                         first on PATH. Prints one JSON row
#                                         labelled with [version] — the
#                                         INSTALLED version, not the
#                                         manifest's max_tested.
#   schema-diff [--live-file F]          Compare the live OpenCode config
#          [--version V]                 schema (fetched from
#                                         schemaContractSchemaURL, or read from
#                                         F) against the manifest's
#                                         config_schema_sha256, labelling the
#                                         row with V (the installed version;
#                                         defaults to max_tested). Prints one
#                                         JSON row; on a difference, re-runs
#                                         the #1634 suite against the live
#                                         schema, and fails (result: fail, and
#                                         this command's own exit code) when
#                                         that suite fails.
#   report <summary.json>                File or update one open
#                                         "canary: <adapter> <version> drift"
#                                         issue per adapter+version with a
#                                         result: fail row. gh, GH_TOKEN.
#
# Every install and CLI call is bounded (ADAPTER_CANARY_TIMEOUT,
# ADAPTER_CANARY_INSTALL_TIMEOUT, ADAPTER_CANARY_GOTEST_TIMEOUT); a
# stub-provider this script starts is always killed and its death confirmed
# before the function that started it returns (opencode_canary_stub_start/
# opencode_canary_stub_stop). flag-contract, opencode-canary and schema-diff
# all return the underlying check's own exit code, so a caller that pipes
# their output through `tee` needs `set -o pipefail` (or `shell: bash`, which
# implies it) to see it — see .github/workflows/adapter-canary.yml.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFESTS="$REPO_ROOT/internal/adaptercompat/manifests"
COMMITTED_HELP_DIR="$REPO_ROOT/internal/execution/adapters/testdata/cli-help"
HELP_TIMEOUT="${ADAPTER_CANARY_TIMEOUT:-300}"
INSTALL_TIMEOUT="${ADAPTER_CANARY_INSTALL_TIMEOUT:-900}"
# ci.yml wraps its own `go test` in `timeout 10m`; the three suites this
# script drives (flag-contract, opencode-canary, schema-diff) get the same
# backstop so a hang in the suite, not just the install, is bounded.
GOTEST_TIMEOUT="${ADAPTER_CANARY_GOTEST_TIMEOUT:-600}"
SCHEMA_URL="https://opencode.ai/config.json"

die() {
  echo "adapter-canary.sh: $*" >&2
  exit 1
}

# help_subcommand mirrors helpSubcommand in flag_contract_test.go and
# help_subcommand() in capture-cli-help.sh: keep the three in step.
help_subcommand() {
  case "$1" in
    codex) echo exec ;;
    opencode) echo run ;;
    *) echo "" ;;
  esac
}

# bounded <seconds> <command...>: run command in its own process group, kill
# the group on timeout or signal, and kill whatever it left running in the
# group after a normal exit. macOS has no timeout(1); this mirrors
# capture-cli-help.sh's own `bounded`.
bounded() {
  perl -e '
    use strict;
    use POSIX ();
    my $secs = shift @ARGV;
    my $pid = fork();
    die "fork: $!\n" unless defined $pid;
    if ($pid == 0) {
      setpgrp(0, 0);
      exec { $ARGV[0] } @ARGV;
      print STDERR "exec $ARGV[0]: $!\n";
      POSIX::_exit(127);
    }
    setpgrp($pid, $pid);
    my $stop = sub {
      my ($code, $why) = @_;
      kill "KILL", -$pid;
      waitpid($pid, 0);
      print STDERR "adapter-canary.sh: $why: @ARGV\n";
      exit $code;
    };
    $SIG{ALRM} = sub { $stop->(124, "timed out after ${secs}s") };
    $SIG{INT} = $SIG{TERM} = $SIG{HUP} = sub { $stop->(130, "interrupted") };
    alarm $secs;
    waitpid($pid, 0);
    my $status = $?;
    alarm 0;
    kill "KILL", -$pid;
    exit($status & 127 ? 128 + ($status & 127) : $status >> 8);
  ' "$@" </dev/null
}

# abspath <path>: an absolute path, because `go test` runs with the PACKAGE
# directory as its working directory, not the caller's — a relative
# NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR or NIGHTGAUGE_OPENCODE_SCHEMA_PATH would
# resolve against the wrong directory otherwise.
abspath() {
  local p="$1"
  case "$p" in
    /*) printf '%s\n' "$p" ;;
    *) printf '%s/%s\n' "$PWD" "$p" ;;
  esac
}

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

# manifest_field <adapter> <jq filter>: read one field of a manifest.
manifest_field() {
  local adapter="$1" filter="$2"
  local file="$MANIFESTS/$adapter.json"
  [ -f "$file" ] || die "no manifest for adapter '$adapter' ($file)"
  jq -r "$filter" "$file"
}

# json_row: build one JSON summary row from key=value args (every value a
# plain string; "null" is passed through unquoted for a missing detail).
json_row() {
  python3 - "$@" <<'PY'
import json, sys
row = {}
for arg in sys.argv[1:]:
    k, _, v = arg.partition("=")
    row[k] = v
print(json.dumps(row))
PY
}

# ---------------------------------------------------------------------------
# install
# ---------------------------------------------------------------------------

# resolve_npm_version <package> <version>: <version> as given, or npm's
# "latest" dist-tag resolved to a concrete MAJOR.MINOR.PATCH.
resolve_npm_version() {
  local pkg="$1" version="$2"
  if [ "$version" != "latest" ]; then
    printf '%s\n' "$version"
    return
  fi
  # AC8 names an explicit timeout on every external command; npm view talks
  # to the registry over the network, so it is bounded the same way every
  # other network/CLI call in this script is.
  bounded "$HELP_TIMEOUT" npm view "$pkg" version 2>/dev/null || die "npm view $pkg version failed"
}

cmd_install() {
  local adapter="${1:?usage: install <adapter> <version|latest>}"
  local requested="${2:?usage: install <adapter> <version|latest>}"
  local binary npm_pkg installer
  binary="$(manifest_field "$adapter" '.binary')"
  npm_pkg="$(manifest_field "$adapter" '.install.npm // ""')"
  installer="$(manifest_field "$adapter" '.install.installer // ""')"

  local prefix bin version
  prefix="$(mktemp -d "${TMPDIR:-/tmp}/adapter-canary-install.XXXXXX")"

  if [ -n "$npm_pkg" ]; then
    version="$(resolve_npm_version "$npm_pkg" "$requested")"
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] \
      || die "$adapter: resolved version '$version' is not a semver"
    bounded "$INSTALL_TIMEOUT" npm install --prefix "$prefix" --no-save \
      --no-package-lock --loglevel=error "$npm_pkg@$version" >&2 \
      || die "$adapter: npm install $npm_pkg@$version failed"
    bin="$prefix/node_modules/.bin/$binary"
  elif [ -n "$installer" ]; then
    [ "$requested" = "latest" ] && requested=""
    local script="$prefix/installer.sh"
    bounded "$INSTALL_TIMEOUT" curl -fsSL --proto '=https' -o "$script" "$installer" >&2 \
      || die "$adapter: downloading $installer failed"
    local bindir="$prefix/bin"
    bounded "$INSTALL_TIMEOUT" env "GROK_BIN_DIR=$bindir" /bin/bash "$script" "$requested" >&2 \
      || die "$adapter: the installer failed for '${requested:-latest}'"
    bin="$bindir/$binary"
    version="$(bounded "$HELP_TIMEOUT" "$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
    [ -n "$version" ] || die "$adapter: could not read the installed version from $bin --version"
  else
    die "$adapter: the manifest names neither an npm package nor an installer"
  fi
  [ -x "$bin" ] || die "$adapter: the install produced no executable $binary at $bin"
  printf 'version=%s\n' "$version"
  printf 'bin=%s\n' "$bin"
}

# ---------------------------------------------------------------------------
# capture-help
# ---------------------------------------------------------------------------

cmd_capture_help() {
  local adapter="${1:?usage: capture-help <adapter> <bin> <outdir>}"
  local bin="${2:?usage: capture-help <adapter> <bin> <outdir>}"
  local outdir="${3:?usage: capture-help <adapter> <bin> <outdir>}"
  local sub binary version
  sub="$(help_subcommand "$adapter")"
  binary="$(manifest_field "$adapter" '.binary')"
  mkdir -p "$outdir"

  local raw command_text
  raw="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-help.XXXXXX")"
  if [ -n "$sub" ]; then
    command_text="$binary $sub --help"
    bounded "$HELP_TIMEOUT" "$bin" "$sub" --help >"$raw" 2>&1
  else
    command_text="$binary --help"
    bounded "$HELP_TIMEOUT" "$bin" --help >"$raw" 2>&1
  fi
  [ -s "$raw" ] || die "$adapter: \`$command_text\` printed nothing"

  # The version this canary captured help at: whatever the caller resolved
  # (ADAPTER_CANARY_VERSION), else read from the binary itself.
  version="${ADAPTER_CANARY_VERSION:-}"
  if [ -z "$version" ]; then
    version="$(bounded "$HELP_TIMEOUT" "$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
  fi
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$adapter: could not resolve a MAJOR.MINOR.PATCH version to name the capture"

  local stem="$adapter${sub:+-$sub}"
  {
    printf '# adapter=%s version=%s command=%s\n' "$adapter" "$version" "$command_text"
    cat "$raw"
  } >"$outdir/$stem-$version.txt"
  rm -f "$raw"

  # Carry over the committed hidden-flag sidecar (#1617), whatever version it
  # was probed on: a hidden flag a CLI does not document in its help tends to
  # stay hidden and accepted release over release, so exact-match-only carried
  # it forward on a pull_request run (every manifest CLI installed at its own
  # already-approved max_tested) but auto-failed claude-headless and grok on
  # EVERY daily/latest run after the next release shipped, because the
  # freshly captured version then differs from the committed probe's own —
  # judging the adapter for being newer instead of on its own captured flags
  # (#1639 round 3). There is at most one committed sidecar per stem (a
  # re-probe replaces it, per testdata/cli-help/README.md), so `ls -t | head`
  # is exact, not a guess among several; nothing here re-probes automatically,
  # so a flag genuinely dropped between the probed and the freshly captured
  # version is still missed until a human re-probes it — the same limitation
  # exact-match carryover already had for any version it DID carry forward to.
  local committed
  committed="$(ls -t "$COMMITTED_HELP_DIR/$stem"-*.txt.hidden 2>/dev/null | head -n1)"
  if [ -n "$committed" ]; then
    cp "$committed" "$outdir/$stem-$version.txt.hidden"
  fi
  echo "$outdir/$stem-$version.txt"
}

# ---------------------------------------------------------------------------
# flag-contract
# ---------------------------------------------------------------------------

# flag_contract_capture_version <helpdir> <adapter>: the version named in
# <adapter>'s capture file in helpdir (the header flag_contract_test.go's
# helpHeaderRE reads), or "" when there is none.
flag_contract_capture_version() {
  local helpdir="$1" adapter="$2" f
  for f in "$helpdir/$adapter"-*.txt "$helpdir/$adapter"-*-*.txt; do
    [ -f "$f" ] || continue
    sed -n '1s/.*version=\([0-9.]*\).*/\1/p' "$f"
    return
  done
}

# adapter_stem <adapter>: the file-name stem an adapter's captures use,
# <adapter>[-<sub>] — the same stem cmd_capture_help writes and
# flag_contract_test.go's findHelpCapture reads. Every path
# flagContractProblems names is under this stem, so it is what attributes a
# problem line that has no "<adapter>: " prefix to its real adapter.
adapter_stem() {
  local adapter="$1" sub
  sub="$(help_subcommand "$adapter")"
  printf '%s' "$adapter${sub:+-$sub}"
}

# cmd_flag_contract <helpdir>: runs TestFlagContract once against every
# adapter's capture in helpdir (the daily/PR run captures every manifest CLI
# into one shared directory first) and emits one JSON row per adapter: "pass"
# with no captured help problem, or "fail" naming the offending flag — parsed
# from the test's own `t.Error` lines. Most already start "<adapter>: "
# (flagContractProblems' message shape); the rest — a malformed capture
# ("parsed only N options..."), a header/command/version mismatch, or an
# orphan sidecar — name the capture's own path instead
# (<stem>-<version>.txt[.hidden]), so they are attributed by that path to the
# real adapter and the version IN THE PATH, never dropped and never filed
# under a pseudo adapter with no version.
cmd_flag_contract() {
  local helpdir
  helpdir="$(abspath "${1:?usage: flag-contract <helpdir>}")"
  local out rc=0
  out="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-flagcontract.XXXXXX")"
  ( cd "$REPO_ROOT" && NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR="$helpdir" \
      bounded "$GOTEST_TIMEOUT" go test -v ./internal/execution/adapters -run TestFlagContract -count=1 ) >"$out" 2>&1 || rc=$?

  # Every genuine problem line, both the per-adapter attribution below and the
  # fallback after it read from this same set. flagContractProblems' NOTES
  # (t.Log; not a contract violation — known-broken/hidden-flag bookkeeping
  # and the helpNotCaptured skip reason) share go test's plain
  # "file.go:N: message" shape with a real problem (t.Error), so the three
  # note templates are excluded by name up front.
  local problem_lines=""
  if [ "$rc" -ne 0 ]; then
    problem_lines="$(grep -E '_test\.go:[0-9]+:' "$out" \
      | grep -v -E 'is known broken \(#[0-9]+\)' \
      | grep -v -E 'is hidden in .*records it probed as accepted on' \
      | grep -v -E ': flags not checked against help: ' \
      || true)"
  fi

  local adapters
  adapters="$(for f in "$MANIFESTS"/*.json; do basename "$f" .json; done)"
  local adapter version failed
  for adapter in $adapters; do
    version="$(flag_contract_capture_version "$helpdir" "$adapter")"
    [ -n "$version" ] || version="$(manifest_field "$adapter" '.max_tested // ""')"
    # When the suite as a whole passed (rc=0), nothing failed anything, so
    # every adapter is reported pass regardless of what its notes say.
    if [ "$rc" -eq 0 ]; then
      json_row adapter="$adapter" version="$version" check=flag-contract result=pass detail=""
      continue
    fi
    failed="$(printf '%s\n' "$problem_lines" | grep -E "^\s*[a-z_.]+_test\.go:[0-9]+: ${adapter}: " | head -n1 || true)"
    local stem file_version=""
    stem="$(adapter_stem "$adapter")"
    if [ -z "$failed" ]; then
      failed="$(printf '%s\n' "$problem_lines" | grep -E -- "${stem}-[0-9]+\.[0-9]+\.[0-9]+\.txt" | head -n1 || true)"
      if [ -n "$failed" ]; then
        file_version="$(printf '%s\n' "$failed" \
          | grep -oE -- "${stem}-[0-9]+\.[0-9]+\.[0-9]+\.txt" | head -n1 \
          | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')"
      fi
    fi
    if [ -n "$failed" ]; then
      json_row adapter="$adapter" version="${file_version:-$version}" check=flag-contract result=fail \
        detail="$(echo "$failed" | sed -E "s/^[^:]+:[0-9]+: //")"
    else
      json_row adapter="$adapter" version="$version" check=flag-contract result=pass detail=""
    fi
  done

  # A genuine failure (rc!=0) whose t.Error line names no known adapter,
  # neither by an "<adapter>: " prefix nor by one of its own capture paths —
  # a manifest-level problem, not a per-adapter one — is attributed to no row
  # above. `report` only acts on result == "fail", so without this a problem
  # of this shape is filed nowhere even though SOME OTHER adapter's own row
  # already carries its own failure: any_failed alone no longer suppresses
  # this. There is no real adapter+version to name it with, so, and only
  # here, it is filed as adapter="flag-contract" version="" — never as a
  # substitute for the per-adapter attribution above, which handles every
  # problem flagContractProblems is observed to raise today.
  if [ "$rc" -ne 0 ]; then
    local remaining="$problem_lines"
    for adapter in $adapters; do
      remaining="$(printf '%s\n' "$remaining" | grep -v -E "^\s*[a-z_.]+_test\.go:[0-9]+: ${adapter}: " || true)"
      stem="$(adapter_stem "$adapter")"
      remaining="$(printf '%s\n' "$remaining" | grep -v -E -- "${stem}-[0-9]+\.[0-9]+\.[0-9]+\.txt" || true)"
    done
    remaining="$(printf '%s\n' "$remaining" | grep -E '_test\.go:[0-9]+:' || true)"
    if [ -z "$remaining" ] && [ -z "$problem_lines" ]; then
      # go test itself produced no attributable line at all (a build failure,
      # a panic before any subtest ran): the last 5 lines are the best detail
      # available, so at least ONE row still tells `report` something broke.
      remaining="$(tail -n 5 "$out" | tr '\n' ' ')"
    fi
    if [ -n "$remaining" ]; then
      local fallback
      fallback="$(printf '%s\n' "$remaining" | head -n1)"
      json_row adapter=flag-contract version="" check=flag-contract result=fail \
        detail="$(echo "$fallback" | sed -E 's/^[^:]+:[0-9]+: //')"
    fi
  fi
  rm -f "$out"
  return "$rc"
}

# ---------------------------------------------------------------------------
# opencode-canary
# ---------------------------------------------------------------------------

cmd_opencode_canary() {
  local bin="${1:?usage: opencode-canary <opencode-bin> [version]}"
  local dir version out rc=0
  dir="$(dirname "$bin")"
  # The INSTALLED version, passed by the caller (the workflow's install step
  # already resolved it) — not the manifest's max_tested, which is only the
  # last-approved baseline and, on a daily/latest run, is almost always stale.
  # Falling back to max_tested here is only for a caller that has no other
  # version to hand (e.g. a manual invocation).
  version="${2:-}"
  [ -n "$version" ] || version="$(manifest_field opencode '.max_tested // ""')"
  out="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-opencode.XXXXXX")"
  # Also ./internal/execution/adapters: TestOpenCodeCanaryRelax* there is the
  # #1639 round-3 regression for openCodeCanaryRelax (opencode_preflight.go),
  # the canary-only relaxation that lets THIS run's dispatch through the
  # endpoint-above-max-tested refusal. It needs no live binary (a fake, like
  # the rest of that package's tests), so running it here costs nothing and
  # means the relaxation itself is proven on every canary invocation, not
  # just by hand.
  ( cd "$REPO_ROOT" && PATH="$dir:$PATH" CI=true NIGHTGAUGE_CANARY=true \
      bounded "$GOTEST_TIMEOUT" go test -tags canary ./internal/execution ./internal/execution/adapters \
        -run 'TestOpenCodeCanary' -count=1 ) >"$out" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    json_row adapter=opencode version="$version" check=opencode-canary result=pass detail=""
  else
    local detail
    detail="$(opencode_canary_failing_line "$out")"
    [ -n "$detail" ] || detail="$(tail -n 5 "$out" | tr '\n' ' ')"
    json_row adapter=opencode version="$version" check=opencode-canary result=fail detail="$detail"
  fi
  rm -f "$out"
  return "$rc"
}

# opencode_canary_failing_line <go-test-output-file>: the line naming the
# actual failure, not a t.Logf notice (realOpenCode's own "opencode %s is
# installed (canary: pin relaxed ...)" notice, printed by nearly every case
# here once the installed version differs from the pinned 1.18.30 baseline —
# it shares the exact indented "file.go:N: message" shape a real t.Error or
# t.Fatalf line has). cmd_opencode_canary runs `go test -tags canary ...
# -count=1` WITHOUT -v: go prints the "--- FAIL: TestName" summary FIRST and
# the failing test's own buffered "file.go:N: message" lines AFTER it,
# stopping at the first unindented line (bare "FAIL"). `go test -v` prints
# the opposite order: the same lines BEFORE the "--- FAIL:" summary, back to
# the unindented "=== RUN" marker that started that test. Either order can
# put the notice ahead of the real message (t.Log always runs before the
# t.Fatalf/t.Error that ends the test), so both directions are scanned in
# the order go test actually prints them and the first non-notice line wins
# — never a hard-coded assumption of one order. A t.Fatalf's continuation
# lines are indented past "file.go:N:" and never match, so only the failing
# message's own first line is ever returned. Fixtures for both orders,
# captured from a real `go test` run: scripts/testdata/adapter-canary-gotest/.
opencode_canary_failing_line() {
  python3 - "$1" <<'PY'
import re, sys

lines = open(sys.argv[1]).read().split("\n")
# Any indented, non-blank line: a candidate "file.go:N: message" line AND a
# t.Fatalf/t.Errorf message's own indented continuation lines (e.g. embedded
# stderr), which carry no "_test.go:N:" prefix of their own and must not end
# the block early — only an unindented line (the next unindented go test
# marker, or the bare "FAIL") does that.
indent_re = re.compile(r'^\s+\S')
detail_re = re.compile(r'^\s+\S.*_test\.go:\d+:')
fail_re = re.compile(r'^\s*--- FAIL:')
notice_re = re.compile(r'is installed \(canary: pin relaxed')


def block_after(start):
    out = []
    for i in range(start + 1, len(lines)):
        if not indent_re.match(lines[i]):
            break
        if detail_re.match(lines[i]):
            out.append(lines[i].strip())
    return out


def block_before(start):
    out = []
    for i in range(start - 1, -1, -1):
        if not indent_re.match(lines[i]):
            break
        if detail_re.match(lines[i]):
            out.append(lines[i].strip())
    out.reverse()
    return out


def first_non_notice(candidates):
    for c in candidates:
        if not notice_re.search(c):
            return c
    return ""


detail = ""
fail_at = next((i for i, l in enumerate(lines) if fail_re.match(l)), None)
if fail_at is not None:
    # Non -v layout: the failing test's own lines follow "--- FAIL:", in the
    # order they were logged, so the first non-notice one is the real one.
    detail = first_non_notice(block_after(fail_at))
    if not detail:
        # -v layout: the failing test's own lines precede "--- FAIL:", also
        # in logged order, so its own last word is closest to "--- FAIL:".
        detail = first_non_notice(list(reversed(block_before(fail_at))))
if not detail:
    # No attributable line at all (a build failure, a panic before any
    # subtest ran, or every candidate was a notice): fall back to the first
    # non-notice "file.go:N:" line anywhere in the output.
    detail = first_non_notice([l.strip() for l in lines if detail_re.match(l)])
print(detail)
PY
}

# ---------------------------------------------------------------------------
# schema-diff
# ---------------------------------------------------------------------------

# schema_top_level_diff <old> <new>: added/removed/deprecated top-level
# property names, one JSON object.
schema_top_level_diff() {
  python3 - "$1" "$2" <<'PY'
import json, sys

def top_level(path):
    doc = json.load(open(path))
    node = doc
    for _ in range(8):
        if "properties" in node:
            return node["properties"]
        if "$ref" in node and "$defs" in doc:
            name = node["$ref"].split("/")[-1]
            node = doc["$defs"].get(name, {})
            continue
        break
    return {}

old_props = top_level(sys.argv[1])
new_props = top_level(sys.argv[2])
added = sorted(set(new_props) - set(old_props))
removed = sorted(set(old_props) - set(new_props))
deprecated = sorted(
    name for name, schema in new_props.items()
    if isinstance(schema, dict) and "@deprecated" in (schema.get("description") or "")
)
print(json.dumps({"added": added, "removed": removed, "deprecated": deprecated}))
PY
}

cmd_schema_diff() {
  local live_file="" opt_version=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --live-file) live_file="$2"; shift 2 ;;
      --version) opt_version="$2"; shift 2 ;;
      *) die "schema-diff: unknown argument '$1'" ;;
    esac
  done

  local pinned="$REPO_ROOT/internal/execution/adapters/testdata/opencode-config-schema/opencode-config.schema.json"
  local pinned_sum manifest_sum
  pinned_sum="$(sha256_of "$pinned")"
  manifest_sum="$(manifest_field opencode '.config_schema_sha256')"
  if [ "$manifest_sum" != "$pinned_sum" ]; then
    die "the manifest's config_schema_sha256 ($manifest_sum) does not match the pinned schema's own hash ($pinned_sum); TestManifestSchemaHash should already have caught this"
  fi

  local live
  if [ -n "$live_file" ]; then
    live="$live_file"
  else
    live="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-live-schema.XXXXXX")"
    bounded "$HELP_TIMEOUT" curl -fsSL --proto '=https' -o "$live" "$SCHEMA_URL" \
      || die "fetching $SCHEMA_URL failed"
  fi

  local live_sum
  live_sum="$(sha256_of "$live")"
  # The INSTALLED version, passed by the caller — not the manifest's
  # max_tested, for the same reason cmd_opencode_canary takes it (a stale
  # label defeats the per-adapter+version dedupe in `report`). Falls back to
  # max_tested only when the caller has no other version to hand.
  local version="$opt_version"
  [ -n "$version" ] || version="$(manifest_field opencode '.max_tested // ""')"

  if [ "$live_sum" = "$manifest_sum" ]; then
    json_row adapter=opencode version="$version" check=schema-diff result=unchanged detail=""
    [ -n "$live_file" ] || rm -f "$live"
    return 0
  fi

  local diff
  diff="$(schema_top_level_diff "$pinned" "$live")"

  local suite_out rc=0
  suite_out="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-schema-suite.XXXXXX")"
  ( cd "$REPO_ROOT" && NIGHTGAUGE_OPENCODE_SCHEMA_PATH="$live" \
      bounded "$GOTEST_TIMEOUT" go test ./internal/execution/adapters \
        -run 'TestGeneratedConfigsValidate|TestValidatorRejectsUnknownKey|TestNoDeprecatedKeys|TestSecurityKeysPresentAndKnown' \
        -count=1 ) >"$suite_out" 2>&1 || rc=$?

  local suite_result="pass"
  [ "$rc" -eq 0 ] || suite_result="fail"
  local detail
  detail="$(python3 - "$diff" "$suite_result" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
d["config_suite"] = sys.argv[2]
print(json.dumps(d))
PY
)"
  # A live schema that breaks the #1634 suite — including dropping or
  # renaming a security key TestSecurityKeysPresentAndKnown exists to catch —
  # is a real contract break, not mere "changed" bookkeeping: `report` only
  # files a drift issue for a result=fail row, and this step must itself fail
  # so the job goes red (once the tee/pipefail fix lets that status through).
  local result="changed"
  [ "$suite_result" = "fail" ] && result="fail"
  json_row adapter=opencode version="$version" check=schema-diff result="$result" detail="$detail"
  [ -n "$live_file" ] || rm -f "$live"
  rm -f "$suite_out"
  [ "$suite_result" = "pass" ]
}

# ---------------------------------------------------------------------------
# stub-run: start/stop the real stub-provider BINARY (cmd/stub-provider),
# bounded, PID captured and confirmed dead. Used only by
# scripts/test-adapter-canary.sh to exercise the timeout/cleanup contract
# directly at the shell level — the workflow's own opencode-canary leg never
# calls these; its stub run is opencode_canary_test.go's own
# startOpenCodeCanaryStub, which spawns this SAME binary as a real subprocess
# per test and captures/kills/confirms its PID in that test's own t.Cleanup
# (AC8), so this shell-level pair and that Go-level one prove the identical
# lifecycle contract, one directly and one through the Go test's own process.
# ---------------------------------------------------------------------------

# opencode_canary_stub_start <script> <pidfile> <urlfile>: builds (if needed)
# and starts stub-provider in the background, bounded to
# --max-requests 20 --idle-timeout 60s, and writes its pid and base_url.
opencode_canary_stub_start() {
  local script="$1" pidfile="$2" urlfile="$3"
  local bin="$REPO_ROOT/bin/stub-provider"
  if [ ! -x "$bin" ]; then
    ( cd "$REPO_ROOT" && go build -o "$bin" ./cmd/stub-provider ) || die "building stub-provider failed"
  fi
  "$bin" --script "$script" --max-requests 20 --idle-timeout 60s >"$urlfile" 2>/dev/null &
  local pid=$!
  echo "$pid" >"$pidfile"
  local tries=0
  while [ ! -s "$urlfile" ] && [ "$tries" -lt 50 ]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  if [ ! -s "$urlfile" ]; then
    # A hang on this path must not leak $pid (and whatever it spawned): kill
    # the whole subtree and confirm it is dead before dying, the same
    # guarantee opencode_canary_stub_stop gives its own caller. Children
    # first: killing $pid before its children reparents them to init (PID 1)
    # before `pkill -P "$pid"` can find them by parent, which is exactly how
    # the fake stub-provider's own `sleep 9999` used to survive this path.
    pkill -9 -P "$pid" 2>/dev/null || true
    kill -9 "$pid" 2>/dev/null || true
    tries=0
    while kill -0 "$pid" 2>/dev/null && [ "$tries" -lt 50 ]; do
      sleep 0.1
      tries=$((tries + 1))
    done
    die "stub-provider did not print its base_url in time"
  fi
}

# opencode_canary_stub_stop <pidfile>: kills the stub-provider named in
# pidfile and confirms it is dead. Never used to authorize anything on its
# own — only the exit code of `kill -0` after the wait does.
opencode_canary_stub_stop() {
  local pidfile="$1"
  [ -f "$pidfile" ] || return 0
  local pid
  pid="$(cat "$pidfile")"
  kill "$pid" 2>/dev/null || true
  local tries=0
  while kill -0 "$pid" 2>/dev/null && [ "$tries" -lt 50 ]; do
    sleep 0.1
    tries=$((tries + 1))
  done
  kill -9 "$pid" 2>/dev/null || true
  sleep 0.2
  if kill -0 "$pid" 2>/dev/null; then
    die "stub-provider pid $pid is still alive after the cleanup step"
  fi
  echo "stub-provider pid $pid confirmed dead"
}

# ---------------------------------------------------------------------------
# report: file or update one open drift issue per adapter+version.
# ---------------------------------------------------------------------------

cmd_report() {
  local summary="${1:?usage: report <summary.json>}"
  [ -f "$summary" ] || die "no such summary file: $summary"
  command -v gh >/dev/null 2>&1 || die "gh is not on PATH"

  # One title per adapter+version among the FAILING rows, each with its own
  # failing rows folded into the issue body. NUL-delimited (title, body)
  # pairs, read below with `read -d ''`, not "\x1f" + a line-based `read`: a
  # body with more than one failing row (opencode failing both flag-contract
  # and opencode-canary at the same version is the common case) spans several
  # lines, and a line-based read turned every line after the first into a
  # bogus new title with an empty body.
  local any=0
  while IFS= read -r -d '' title && IFS= read -r -d '' body; do
    any=1
    [ -n "$title" ] || continue
    local existing
    existing="$(gh issue list --state open --search "in:title \"$title\"" --json title,url \
      | jq -r --arg t "$title" '.[] | select(.title == $t) | .url' | head -n1)"
    if [ -n "$existing" ]; then
      gh issue comment "$existing" --body "$body"
      echo "commented on $existing"
    else
      local url
      url="$(gh issue create --title "$title" --body "$body" --label component:ci)"
      echo "created $url"
    fi
  done < <(python3 - "$summary" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
seen = {}
for r in rows:
    if r.get("result") != "fail":
        continue
    key = (r.get("adapter", ""), r.get("version", ""))
    seen.setdefault(key, []).append(r)
for (adapter, version), failing in seen.items():
    title = f"canary: {adapter} {version} drift"
    body = "\n".join(f"- {r.get('check')}: {r.get('detail','')}" for r in failing)
    sys.stdout.write(title + "\0" + body + "\0")
PY
)

  [ "$any" -eq 1 ] || echo "no failing rows; nothing to file"
}

# ---------------------------------------------------------------------------

main() {
  local cmd="${1:-}"
  [ -n "$cmd" ] || die "usage: adapter-canary.sh <install|capture-help|flag-contract|opencode-canary|schema-diff|report> ..."
  shift
  case "$cmd" in
    install) cmd_install "$@" ;;
    capture-help) cmd_capture_help "$@" ;;
    flag-contract) cmd_flag_contract "$@" ;;
    opencode-canary) cmd_opencode_canary "$@" ;;
    schema-diff) cmd_schema_diff "$@" ;;
    report) cmd_report "$@" ;;
    *) die "unknown subcommand '$cmd'" ;;
  esac
}

# Sourced (by the test suite, to reuse the functions above without running
# main) when $0 is not this file.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
