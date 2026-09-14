#!/usr/bin/env bash
# capture-opencode-fixture.sh — captures the real `opencode run --format json`
# transcripts the opencode stream parser is tested against (#1624), redacts
# them, and refuses to write any that still hold a credential.
#
#   bash scripts/capture-opencode-fixture.sh [--out DIR]
#   bash scripts/capture-opencode-fixture.sh --check FILE...
#   bash scripts/capture-opencode-fixture.sh --self-test
#
# Capture writes, into internal/execution/testdata/ unless --out names another
# directory:
#   opencode_stream_research_sample.jsonl     a stage that edits a file: two
#                                             steps
#   opencode_auto_reject_stream.jsonl         a stage whose bash call resolves
#   opencode_auto_reject_stderr.txt           to `ask` and is rejected, and its
#                                             stderr
#   opencode_auto_reject_heredoc_stream.jsonl the same, for a bash command over
#   opencode_auto_reject_heredoc_stderr.txt   several lines (a heredoc), whose
#                                             notice spans several lines
# Update internal/execution/testdata/README.md's tables in the same change.
#
# How a capture runs, and why:
#   - The model is the repository's stub provider (cmd/stub-provider), bound
#     to 127.0.0.1 and serving the scripts tool-edit-stop and bash-then-stop,
#     and bash-heredoc-then-stop: bash-then-stop with a heredoc command. The
#     stub embeds its scripts, so it is built from a staged copy of its
#     sources whose scripts.json adds that one script; nothing in the
#     repository changes. No hosted provider and no model server on another
#     machine takes part.
#   - OpenCode runs under `env -i` with a throwaway HOME and throwaway XDG
#     config, data, cache and state directories, so it reads none of the
#     operator's OpenCode state and writes nothing outside the sandbox. Its
#     config names only the stub, as a complete provider block that binds no
#     API-key variable.
#   - Every OpenCode run is bounded by a 90 s alarm, the stub exits after 60 s
#     without a request, and the stub's pid is killed and checked dead after
#     each run and on every exit.
#   - The pinned OpenCode version is the one ADR-022's observations were made
#     on; another version is refused rather than recorded.
#
# Redaction (internal/execution/testdata/redact-opencode.jq) rewrites the
# sandbox paths, session ids and credential shapes; the stderr keeps its
# terminal escape codes, which the parser has to handle. Then every staged file
# is checked, and the capture is refused, nothing written, if any still holds
# a credential shape, an IPv4 address other than 127.0.0.1, a sandbox path, or
# the run's server password. Files are moved into place only after all pass.
#
# --check runs that check on existing files and exits 1 if any fails it.
# --self-test plants a credential in a staged transcript and runs the write
# path, which must refuse it: the script then exits 1, having written nothing.
# Exit 0 from --self-test would mean a credential reached a fixture.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TESTDATA="$REPO_ROOT/internal/execution/testdata"
FILTER="$TESTDATA/redact-opencode.jq"
PINNED_VERSION="1.18.30"
FIXTURES=(opencode_stream_research_sample.jsonl opencode_auto_reject_stream.jsonl opencode_auto_reject_stderr.txt
  opencode_auto_reject_heredoc_stream.jsonl opencode_auto_reject_heredoc_stderr.txt)
# The command bash-heredoc-then-stop calls: a heredoc, so its input holds
# newlines. OpenCode never runs it; the call is rejected.
HEREDOC_COMMAND="python3 - <<'PYEOF'"$'\n'"print(1)"$'\n'"PYEOF"

die() {
  echo "capture-opencode-fixture.sh: $*" >&2
  exit 1
}

STAGING=""
STUB_PID=""
SERVER_PASSWORD=""
cleanup() {
  if [ -n "$STUB_PID" ]; then
    kill "$STUB_PID" 2>/dev/null || true
    wait "$STUB_PID" 2>/dev/null || true
  fi
  if [ -n "$STAGING" ]; then
    rm -rf "$STAGING"
  fi
  return 0
}
trap cleanup EXIT

new_staging() {
  STAGING="$(mktemp -d "${TMPDIR:-/tmp}/opencode-fixture.XXXXXX")"
  STAGING="$(cd "$STAGING" && pwd -P)"
}

# credential_shapes lists the shapes a fixture must not hold, one per line:
# name, then "i" for a case-insensitive match or "-", then a POSIX ERE. They
# are the shapes RedactCredentials (internal/execution/opencode_usage.go) and
# redact-opencode.jq remove. A fixture is checked as JSON text, so a key or a
# token may follow a JSON escape (`\n` at the start of a line of tool output,
# `[32m` before coloured output) as well as a character no credential
# holds.
credential_shapes() {
  cat <<'EOF'
api-key	-	(^|[^A-Za-z0-9_]|\\[bfnrt]|\\u[0-9A-Fa-f]{4}|\[[0-9;?]*[A-Za-z])(sk-[A-Za-z0-9_-]{20,}|xai-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|(AKIA|ASIA)[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,})
forge-token	-	(^|[^A-Za-z0-9_]|\\[bfnrt]|\\u[0-9A-Fa-f]{4}|\[[0-9;?]*[A-Za-z])(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,})
bearer-token	i	bearer[[:space:]]+[A-Za-z0-9._~+/-]{16,}
authorization	i	authorization[[:space:]]*[:=][[:space:]]*(basic|token)[[:space:]]+[A-Za-z0-9._~+/-]{8,}
userinfo	-	[A-Za-z][A-Za-z0-9+.-]*://[^[:space:]/@:"'\\]+:[^[:space:]/@"'\\]+@
query-credential	i	[?&](api[_-]?key|apikey|key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|secret|password|passwd|pwd|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=[^&#[:space:]"'\\]+
EOF
}

# check_file prints why FILE may not become a fixture, and nothing when it
# may. The redaction placeholders are removed before the credential shapes
# are matched, so a redacted value does not read as a credential.
check_file() {
  local file="$1" scratch name flag pattern others
  scratch="$(mktemp "${TMPDIR:-/tmp}/opencode-fixture-check.XXXXXX")"
  sed -E 's/\[REDACTED:[a-z-]+\]//g' "$file" >"$scratch"
  while IFS=$'\t' read -r name flag pattern; do
    local opts=(-E)
    [ "$flag" = i ] && opts+=(-i)
    # Not -q on a pipe: grep reads the file itself, so no writer can be
    # killed by an early exit and turn a match into a pass under pipefail.
    if grep "${opts[@]}" -- "$pattern" "$scratch" >/dev/null; then
      echo "it still matches the credential shape '$name'"
    fi
  done < <(credential_shapes)
  # Every dotted quad on its own line, so 127.0.0.1 on the same line cannot
  # hide another address.
  others="$(grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$scratch" | grep -vxF '127.0.0.1' || true)"
  if [ -n "$others" ]; then
    echo "it names an IPv4 address other than 127.0.0.1"
  fi
  if [ -n "$STAGING" ] && grep -F -- "$STAGING" "$scratch" >/dev/null; then
    echo "it names the capture's sandbox directory"
  fi
  if [ -n "$SERVER_PASSWORD" ] && grep -F -- "$SERVER_PASSWORD" "$scratch" >/dev/null; then
    echo "it holds the run's OPENCODE_SERVER_PASSWORD"
  fi
  rm -f "$scratch"
}

# commit_staged checks every fixture staged in $1 and, only when all pass,
# moves them into $2. A refused capture writes nothing.
commit_staged() {
  local from="$1" to="$2" name reasons refused=0
  for name in "${FIXTURES[@]}"; do
    [ -f "$from/$name" ] || die "refused: $name was not staged; no fixture was written"
    reasons="$(check_file "$from/$name")"
    if [ -n "$reasons" ]; then
      while IFS= read -r reason; do
        echo "capture-opencode-fixture.sh: refused: the staged $name: $reason" >&2
      done <<<"$reasons"
      refused=1
    fi
  done
  [ "$refused" -eq 0 ] || die "refused: no fixture was written"
  mkdir -p "$to"
  for name in "${FIXTURES[@]}"; do
    mv "$from/$name" "$to/$name"
  done
}

run_check() {
  local file reasons failed=0
  [ "$#" -gt 0 ] || die "--check needs at least one file"
  for file in "$@"; do
    [ -f "$file" ] || die "--check: no such file: $file"
    reasons="$(check_file "$file")"
    if [ -n "$reasons" ]; then
      while IFS= read -r reason; do
        echo "capture-opencode-fixture.sh: $file: $reason" >&2
      done <<<"$reasons"
      failed=1
    fi
  done
  [ "$failed" -eq 0 ] || exit 1
  echo "capture-opencode-fixture.sh: $# file(s) hold no credential shape"
}

run_self_test() {
  local planted name
  new_staging
  mkdir -p "$STAGING/stage" "$STAGING/out"
  for name in "${FIXTURES[@]}"; do
    [ -f "$TESTDATA/$name" ] || die "--self-test: the committed $name is missing"
    cp "$TESTDATA/$name" "$STAGING/stage/$name"
  done
  # Built at run time, so no credential-shaped literal is committed here.
  planted="gh""p_$(printf '%036d' 1624)"
  printf '{"type":"text","sessionID":"ses_fixture0000000000000000001","part":{"type":"text","text":"token %s"}}\n' \
    "$planted" >>"$STAGING/stage/opencode_stream_research_sample.jsonl"
  echo "capture-opencode-fixture.sh: --self-test: planted a credential in the staged research sample; the write must refuse it" >&2
  commit_staged "$STAGING/stage" "$STAGING/out"
  echo "capture-opencode-fixture.sh: --self-test FAILED: the planted credential passed the check" >&2
  exit 0
}

# capture_one runs one stage against the stub and stages its redacted stdout
# and stderr as $3.jsonl and $3.stderr.
capture_one() {
  local script="$1" permission="$2" name="$3" prompt="$4"
  local box="$STAGING/$name" base_url rc opencode_dir git_dir
  mkdir -p "$box"/{home,config/opencode,data,cache,state,tmp,repo}
  (
    cd "$box/repo"
    git init -q
    printf 'def add(a, b):\n    return a + b\n' >calc.py
    git add calc.py
    git -c user.name=fixture -c user.email=fixture@example.invalid commit -qm init
  )

  "$STAGING/stub-provider" --script "$script" --listen 127.0.0.1:0 --max-requests 10 --idle-timeout 60s \
    >"$box/stub.out" 2>"$box/stub.err" &
  STUB_PID=$!
  for _ in $(seq 1 100); do
    [ -s "$box/stub.out" ] && break
    kill -0 "$STUB_PID" 2>/dev/null || die "the stub provider exited before it bound"
    sleep 0.05
  done
  base_url="$(jq -r .base_url <"$box/stub.out")"
  case "$base_url" in
  http://127.0.0.1:*/v1) ;;
  *) die "the stub provider did not bind to 127.0.0.1" ;;
  esac

  jq -n --arg url "$base_url" --argjson permission "$permission" '{
    "$schema": "https://opencode.ai/config.json",
    share: "disabled",
    autoupdate: false,
    enabled_providers: ["lmstudio"],
    provider: {lmstudio: {
      npm: "@ai-sdk/openai-compatible",
      env: [],
      options: {baseURL: $url, apiKey: ""},
      models: {"qwen/qwen3.8-27b": {name: "qwen/qwen3.8-27b"}}
    }},
    agent: {title: {disable: true}},
    permission: $permission
  }' >"$box/config/opencode/opencode.json"

  opencode_dir="$(dirname "$(command -v opencode)")"
  git_dir="$(dirname "$(command -v git)")"
  set +e
  printf '%s' "$prompt" | env -i \
    PATH="$opencode_dir:$git_dir:/usr/bin:/bin" \
    HOME="$box/home" TMPDIR="$box/tmp" \
    XDG_CONFIG_HOME="$box/config" XDG_DATA_HOME="$box/data" \
    XDG_CACHE_HOME="$box/cache" XDG_STATE_HOME="$box/state" \
    OPENCODE_DISABLE_MODELS_FETCH=1 OPENCODE_DISABLE_AUTOUPDATE=1 \
    OPENCODE_DISABLE_LSP_DOWNLOAD=1 OPENCODE_DISABLE_DEFAULT_PLUGINS=1 \
    OPENCODE_DISABLE_SHARE=1 OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1 \
    OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1 OPENCODE_DISABLE_EXTERNAL_SKILLS=1 \
    OPENCODE_SERVER_PASSWORD="$SERVER_PASSWORD" \
    perl -e 'alarm 90; exec @ARGV' -- \
    opencode run --format json --print-logs --log-level ERROR \
    -m lmstudio/qwen/qwen3.8-27b --dir "$box/repo" \
    >"$box/raw.stdout" 2>"$box/raw.stderr"
  rc=$?
  set -e

  kill "$STUB_PID" 2>/dev/null || true
  wait "$STUB_PID" 2>/dev/null || true
  if kill -0 "$STUB_PID" 2>/dev/null; then
    die "the stub provider (pid $STUB_PID) is still running after its kill"
  fi
  STUB_PID=""
  [ "$rc" -eq 0 ] || die "opencode run for $name exited $rc; no fixture was written"

  local roots
  roots="$(jq -cn --arg a "$STAGING" --arg b "$box" '[$a, $b]')"
  jq -c -s --arg mode stream --argjson roots "$roots" -f "$FILTER" "$box/raw.stdout" >"$STAGING/stage/$name.jsonl"
  jq -j -R -s --arg mode stderr --argjson roots "$roots" -f "$FILTER" "$box/raw.stderr" >"$STAGING/stage/$name.stderr"
}

# build_stub builds the stub provider into $STAGING from a staged copy of its
# sources, whose scripts.json adds bash-heredoc-then-stop: bash-then-stop
# calling HEREDOC_COMMAND. The stub needs nothing but the standard library.
build_stub() {
  local src="$STAGING/stub-src" f
  mkdir -p "$src/cmd/stub-provider" "$src/internal/stubprovider"
  for f in "$REPO_ROOT"/cmd/stub-provider/*.go "$REPO_ROOT"/internal/stubprovider/*.go; do
    case "$f" in
    *_test.go) ;;
    */cmd/stub-provider/*) cp "$f" "$src/cmd/stub-provider/" ;;
    *) cp "$f" "$src/internal/stubprovider/" ;;
    esac
  done
  jq --arg cmd "$HEREDOC_COMMAND" \
    '.["bash-heredoc-then-stop"] = (.["bash-then-stop"] | .turns[0].tool_call.arguments.command = $cmd)' \
    "$REPO_ROOT/internal/stubprovider/scripts.json" >"$src/internal/stubprovider/scripts.json"
  printf 'module github.com/nightgauge/nightgauge\n\n%s\n' "$(grep -m1 '^go ' "$REPO_ROOT/go.mod")" >"$src/go.mod"
  (cd "$src" && GOWORK=off GOFLAGS=-mod=mod go build -o "$STAGING/stub-provider" ./cmd/stub-provider)
}

run_capture() {
  local out="$1" version
  for tool in opencode go jq git perl; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
  done
  new_staging
  mkdir -p "$STAGING/stage" "$STAGING/probe"
  version="$(env -i PATH="/usr/bin:/bin" HOME="$STAGING/probe" XDG_CONFIG_HOME="$STAGING/probe" \
    XDG_DATA_HOME="$STAGING/probe" XDG_CACHE_HOME="$STAGING/probe" XDG_STATE_HOME="$STAGING/probe" \
    perl -e 'alarm 30; exec @ARGV' -- "$(command -v opencode)" --version </dev/null | tr -d '[:space:]')"
  [ "$version" = "$PINNED_VERSION" ] ||
    die "opencode $version is installed; the fixtures record $PINNED_VERSION. Re-verify ADR-022's observations, then raise PINNED_VERSION and the README together"

  build_stub
  SERVER_PASSWORD="fixture-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"

  capture_one tool-edit-stop '{"edit":"allow","bash":"deny","webfetch":"deny","external_directory":"deny"}' \
    edit "Change add so that it subtracts in calc.py."
  capture_one bash-then-stop '{"bash":"ask","edit":"deny","webfetch":"deny","external_directory":"deny"}' \
    reject "Run calc.py."
  capture_one bash-heredoc-then-stop '{"bash":"ask","edit":"deny","webfetch":"deny","external_directory":"deny"}' \
    heredoc "Run the script."

  # The shapes the tests are written against, checked before anything moves.
  [ "$(jq -s '[.[] | select(.type == "step_finish")] | length' "$STAGING/stage/edit.jsonl")" = 2 ] ||
    die "refused: the edit capture does not have exactly two step_finish events"
  local name
  for name in reject heredoc; do
    [ "$(jq -s '[.[] | select(.type == "tool_use" and .part.state.status == "error")] | length' "$STAGING/stage/$name.jsonl")" = 1 ] ||
      die "refused: the $name capture does not have one rejected tool_use event"
  done
  grep -F 'permission requested: bash (' "$STAGING/stage/reject.stderr" | grep -F 'auto-rejecting' >/dev/null ||
    die "refused: the reject capture's stderr has no auto-reject line"
  # The heredoc's notice starts on one line and ends on a later one.
  if ! { head -n 1 "$STAGING/stage/heredoc.stderr" | grep -F 'permission requested: bash (' | grep -vF 'auto-rejecting' >/dev/null &&
    [ "$(grep -c '' "$STAGING/stage/heredoc.stderr")" -gt 1 ] &&
    tail -n 1 "$STAGING/stage/heredoc.stderr" | grep -E '\); auto-rejecting$' >/dev/null; }; then
    die "refused: the heredoc capture's stderr does not spread one auto-reject notice over several lines"
  fi

  mv "$STAGING/stage/edit.jsonl" "$STAGING/stage/opencode_stream_research_sample.jsonl"
  mv "$STAGING/stage/reject.jsonl" "$STAGING/stage/opencode_auto_reject_stream.jsonl"
  mv "$STAGING/stage/reject.stderr" "$STAGING/stage/opencode_auto_reject_stderr.txt"
  mv "$STAGING/stage/heredoc.jsonl" "$STAGING/stage/opencode_auto_reject_heredoc_stream.jsonl"
  mv "$STAGING/stage/heredoc.stderr" "$STAGING/stage/opencode_auto_reject_heredoc_stderr.txt"
  rm -f "$STAGING/stage/edit.stderr"
  commit_staged "$STAGING/stage" "$out"
  echo "captured opencode $version into $out: ${FIXTURES[*]}"
}

case "${1:-}" in
--self-test)
  run_self_test
  ;;
--check)
  shift
  run_check "$@"
  ;;
--out)
  [ -n "${2:-}" ] || die "--out needs a directory"
  run_capture "$2"
  ;;
"")
  run_capture "$TESTDATA"
  ;;
*)
  die "unknown argument: $1 (use --out DIR, --check FILE..., or --self-test)"
  ;;
esac
