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
#                                         override reads.
#   flag-contract <dir>                  Run TestFlagContract once against
#                                         every adapter's capture in dir (a
#                                         shared directory captured for every
#                                         manifest CLI). Prints one JSON row
#                                         per adapter: pass, or fail naming the
#                                         offending flag.
#   opencode-canary <bin>                Run the #1639 OpenCode leg
#                                         (go test -tags canary ./internal/execution
#                                         -run TestOpenCodeCanary) with <bin>
#                                         first on PATH. Prints one JSON row.
#   schema-diff [--live-file F]          Compare the live OpenCode config
#                                         schema (fetched from
#                                         schemaContractSchemaURL, or read from
#                                         F) against the manifest's
#                                         config_schema_sha256. Prints one JSON
#                                         row; on a difference, re-runs the
#                                         #1634 suite against the live schema.
#   report <summary.json>                File or update one open
#                                         "canary: <adapter> <version> drift"
#                                         issue per adapter+version with a
#                                         result: fail row. gh, GH_TOKEN.
#
# Every install and CLI call is bounded (ADAPTER_CANARY_TIMEOUT,
# ADAPTER_CANARY_INSTALL_TIMEOUT); a stub-provider this script starts is
# always killed and its death confirmed before the function that started it
# returns (opencode_canary_stub_start/opencode_canary_stub_stop).
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFESTS="$REPO_ROOT/internal/adaptercompat/manifests"
HELP_TIMEOUT="${ADAPTER_CANARY_TIMEOUT:-300}"
INSTALL_TIMEOUT="${ADAPTER_CANARY_INSTALL_TIMEOUT:-900}"
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
  npm view "$pkg" version 2>/dev/null || die "npm view $pkg version failed"
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
    version="$("$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
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
    version="$("$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
  fi
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$adapter: could not resolve a MAJOR.MINOR.PATCH version to name the capture"

  local stem="$adapter${sub:+-$sub}"
  {
    printf '# adapter=%s version=%s command=%s\n' "$adapter" "$version" "$command_text"
    cat "$raw"
  } >"$outdir/$stem-$version.txt"
  rm -f "$raw"
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

# cmd_flag_contract <helpdir>: runs TestFlagContract once against every
# adapter's capture in helpdir (the daily/PR run captures every manifest CLI
# into one shared directory first) and emits one JSON row per adapter: "pass"
# with no captured help problem, or "fail" naming the offending flag — parsed
# from the test's own `t.Error` lines, each of which already starts
# "<adapter>: ..." (flagContractProblems' message shape).
cmd_flag_contract() {
  local helpdir
  helpdir="$(abspath "${1:?usage: flag-contract <helpdir>}")"
  local out rc=0
  out="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-flagcontract.XXXXXX")"
  ( cd "$REPO_ROOT" && NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR="$helpdir" \
      go test -v ./internal/execution/adapters -run TestFlagContract -count=1 ) >"$out" 2>&1 || rc=$?

  local adapters
  adapters="$(for f in "$MANIFESTS"/*.json; do basename "$f" .json; done)"
  local adapter version failed
  for adapter in $adapters; do
    version="$(flag_contract_capture_version "$helpdir" "$adapter")"
    [ -n "$version" ] || version="$(manifest_field "$adapter" '.max_tested // ""')"
    # go test's plain output does not distinguish a t.Error line (a real
    # contract violation) from a t.Log note (known-broken/hidden-flag/skip
    # bookkeeping) — both print as "file.go:N: message". When the suite as a
    # whole passed (rc=0), NEITHER kind failed anything, so every adapter is
    # reported pass regardless of what its notes say. Only on a genuine
    # failure (rc!=0) is a line naming the adapter attributed to it.
    if [ "$rc" -eq 0 ]; then
      json_row adapter="$adapter" version="$version" check=flag-contract result=pass detail=""
      continue
    fi
    # flagContractProblems' NOTES (t.Log; not a contract violation) share the
    # plain "file.go:N: message" shape a real problem (t.Error) does, so the
    # three note templates it uses (knownBroken/hidden-flag bookkeeping and
    # the helpNotCaptured skip reason) are excluded by name before the first
    # remaining "<adapter>: " line is taken as a genuine failure.
    failed="$(grep -E '_test\.go:[0-9]+:' "$out" \
      | grep -E "^\s*[a-z_.]+_test\.go:[0-9]+: ${adapter}: " \
      | grep -v -E 'is known broken \(#[0-9]+\)' \
      | grep -v -E 'is hidden in .*records it probed as accepted on' \
      | grep -v -E ': flags not checked against help: ' \
      | head -n1 || true)"
    if [ -n "$failed" ]; then
      json_row adapter="$adapter" version="$version" check=flag-contract result=fail \
        detail="$(echo "$failed" | sed -E "s/^[^:]+:[0-9]+: //")"
    else
      json_row adapter="$adapter" version="$version" check=flag-contract result=pass detail=""
    fi
  done
  rm -f "$out"
  return "$rc"
}

# ---------------------------------------------------------------------------
# opencode-canary
# ---------------------------------------------------------------------------

cmd_opencode_canary() {
  local bin="${1:?usage: opencode-canary <opencode-bin>}"
  local dir version out rc=0
  dir="$(dirname "$bin")"
  version="$(manifest_field opencode '.max_tested // ""')"
  out="$(mktemp "${TMPDIR:-/tmp}/adapter-canary-opencode.XXXXXX")"
  ( cd "$REPO_ROOT" && PATH="$dir:$PATH" CI=true \
      go test -tags canary ./internal/execution -run TestOpenCodeCanary -count=1 ) >"$out" 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    json_row adapter=opencode version="$version" check=opencode-canary result=pass detail=""
  else
    local detail
    detail="$(grep -m1 -E '^\s+.*_test\.go:[0-9]+:' "$out" | sed 's/^ *//' || true)"
    [ -n "$detail" ] || detail="$(tail -n 5 "$out" | tr '\n' ' ')"
    json_row adapter=opencode version="$version" check=opencode-canary result=fail detail="$detail"
  fi
  rm -f "$out"
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
  local live_file=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --live-file) live_file="$2"; shift 2 ;;
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
  local version
  version="$(manifest_field opencode '.max_tested // ""')"

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
      go test ./internal/execution/adapters \
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
  json_row adapter=opencode version="$version" check=schema-diff result=changed detail="$detail"
  [ -n "$live_file" ] || rm -f "$live"
  rm -f "$suite_out"
}

# ---------------------------------------------------------------------------
# stub-run: start/stop the real stub-provider BINARY (cmd/stub-provider),
# bounded, PID captured and confirmed dead — the artifact-capture leg the
# workflow uses to upload the raw stream, apart from the Go test's own
# in-process stub.
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
  [ -s "$urlfile" ] || die "stub-provider did not print its base_url in time"
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
  # failing rows folded into the issue body.
  local titles
  titles="$(python3 - "$summary" <<'PY'
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
    body_lines = [f"- {r.get('check')}: {r.get('detail','')}" for r in failing]
    print(title + "\x1f" + "\n".join(body_lines))
PY
)"
  [ -n "$titles" ] || { echo "no failing rows; nothing to file"; return 0; }

  while IFS=$'\x1f' read -r title body; do
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
  done <<<"$titles"
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
