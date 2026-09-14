#!/usr/bin/env bash
# capture-opencode-failure-fixture.sh — captures how opencode 1.18.30 reports
# a failed model request, as the evidence the OpenCode terminal-kind rules are
# written against (#1631), redacts it, and refuses to write any capture that
# still holds a credential.
#
#   bash scripts/capture-opencode-failure-fixture.sh [--out DIR]
#
# Writes, into internal/terminalkind/testdata/opencode/ unless --out names
# another directory, one `<name>.stderr` (what `--print-logs --log-level ERROR`
# printed) and one `<name>.jsonl` (the `--format json` stream) per leg:
#
#   overflow-openai        the stub provider's `overflow` script (#1618): a 400
#                          whose message carries the OpenAI-compatible
#                          `context_length_exceeded` code and
#                          `maximum context length is N tokens`
#   overflow-lmstudio      a 400 whose message holds the fragment OpenCode's
#   overflow-ollama        overflow recogniser attributes to LM Studio, to
#   overflow-llamacpp      Ollama, and to the llama.cpp server
#   provider-error         the stub's `error` script: a 500 that is not an
#                          overflow (the negative control)
#   auth                   a 401 with an empty body
#   server-down            a provider block pointed at a closed loopback port
#   model-not-configured   `-m` naming a model the provider block does not list
#
# and `lmstudio-unknown-model.json` when an LM Studio answers on
# 127.0.0.1:1234: the model id a one-token request named and the model id that
# answered it. That leg sends no prompt through OpenCode and is skipped when no
# LM Studio is listening.
#
# The overflow-lmstudio, -ollama and -llamacpp messages are sentences built
# around a fragment of OpenCode's own overflow recogniser, not captures of
# those servers: which fragment belongs to which server is what OpenCode's
# source says (internal/terminalkind/testdata/opencode/README.md). What the
# capture records is real: how opencode 1.18.30 carries such a message to its
# stderr and its stream, and whether it names the failure ContextOverflowError.
#
# How a capture runs, and why (the same sandbox as
# scripts/capture-opencode-fixture.sh):
#   - Every model server is on 127.0.0.1: the repository's stub provider
#     (cmd/stub-provider), or a one-status loopback server this script starts
#     with python3. No hosted provider and no model server on another machine
#     takes part.
#   - OpenCode runs under `env -i` with a throwaway HOME and throwaway XDG
#     config, data, cache and state directories, and a config naming only that
#     server as a complete provider block that binds no API-key variable.
#   - Every OpenCode run is bounded by a 150 s alarm, and every server's pid is
#     killed and checked dead after each leg and on every exit.
#   - The pinned OpenCode version is the one ADR-022's observations were made
#     on; another version is refused rather than recorded.
#
# Redaction is internal/execution/testdata/redact-opencode.jq (#1624), then the
# ids and clocks OpenCode prints (session, message, run and error-ref ids,
# timestamps, the response's date header) are replaced by fixed placeholders so
# a re-capture differs only where OpenCode's behaviour does. Every staged file
# is then checked by `scripts/capture-opencode-fixture.sh --check`, and for the
# sandbox path, and the capture is refused, nothing written, if any fails.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$REPO_ROOT/internal/terminalkind/testdata/opencode"
FILTER="$REPO_ROOT/internal/execution/testdata/redact-opencode.jq"
CHECKER="$REPO_ROOT/scripts/capture-opencode-fixture.sh"
PINNED_VERSION="1.18.30"
MODEL_ID="qwen/qwen3.8-27b"

die() {
  echo "capture-opencode-failure-fixture.sh: $*" >&2
  exit 1
}

STAGING=""
SERVER_PID=""
stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "capture-opencode-failure-fixture.sh: server pid $SERVER_PID is still running after its kill" >&2
      return 1
    fi
    SERVER_PID=""
  fi
}
cleanup() {
  stop_server || true
  [ -z "$STAGING" ] || rm -rf "$STAGING"
  return 0
}
trap cleanup EXIT

case "${1:-}" in
"") ;;
--out)
  [ -n "${2:-}" ] || die "--out needs a directory"
  OUT="$2"
  ;;
*) die "unknown argument: $1 (use --out DIR)" ;;
esac

for tool in opencode go jq git perl python3 curl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
done
STAGING="$(mktemp -d "${TMPDIR:-/tmp}/opencode-failure-fixture.XXXXXX")"
STAGING="$(cd "$STAGING" && pwd -P)"
mkdir -p "$STAGING/stage" "$STAGING/probe"

version="$(env -i PATH="/usr/bin:/bin" HOME="$STAGING/probe" XDG_CONFIG_HOME="$STAGING/probe" \
  XDG_DATA_HOME="$STAGING/probe" XDG_CACHE_HOME="$STAGING/probe" XDG_STATE_HOME="$STAGING/probe" \
  perl -e 'alarm 30; exec @ARGV' -- "$(command -v opencode)" --version </dev/null | tr -d '[:space:]')"
[ "$version" = "$PINNED_VERSION" ] ||
  die "opencode $version is installed; the fixtures record $PINNED_VERSION. Re-verify ADR-022's observations, then raise PINNED_VERSION and the README together"

(cd "$REPO_ROOT" && go build -o "$STAGING/stub-provider" ./cmd/stub-provider)

# The one-status server: every request gets STATUS and BODY (a file, possibly
# empty), on 127.0.0.1, and the server stops by itself after 180 s.
cat >"$STAGING/status_server.py" <<'PYEOF'
import http.server, sys, threading
status, body_path, port_path = int(sys.argv[1]), sys.argv[2], sys.argv[3]
body = open(body_path, "rb").read()
class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self):
        length = int(self.headers.get("content-length") or 0)
        if length:
            self.rfile.read(length)
        self.send_response(status)
        if body:
            self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    do_GET = do_POST = reply
    def log_message(self, *args):
        pass
server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
open(port_path, "w").write(str(server.server_address[1]))
threading.Timer(180, server.shutdown).start()
server.serve_forever()
PYEOF

# wait_for FILE: until the server has written its address, or it died.
wait_for() {
  local file="$1"
  for _ in $(seq 1 100); do
    [ -s "$file" ] && return 0
    kill -0 "$SERVER_PID" 2>/dev/null || die "the model server exited before it bound"
    sleep 0.05
  done
  die "the model server did not bind within 5 s"
}

# start_stub SCRIPT: the stub provider on 127.0.0.1; sets BASE_URL. Not run
# in a command substitution, so SERVER_PID is this shell's.
BASE_URL=""
start_stub() {
  local out="$STAGING/stub-$1.out"
  "$STAGING/stub-provider" --script "$1" --listen 127.0.0.1:0 --max-requests 20 --idle-timeout 150s \
    >"$out" 2>"$STAGING/stub-$1.err" &
  SERVER_PID=$!
  wait_for "$out"
  local url
  url="$(jq -r .base_url <"$out")"
  case "$url" in
  http://127.0.0.1:*/v1) BASE_URL="$url" ;;
  *) die "the stub provider did not bind to 127.0.0.1" ;;
  esac
}

# start_status STATUS MESSAGE: the one-status server; MESSAGE, when set, is
# served as an OpenAI-compatible error body. Sets BASE_URL.
start_status() {
  local status="$1" message="$2" body="$STAGING/body-$RANDOM" port="$STAGING/port-$RANDOM"
  if [ -n "$message" ]; then
    jq -cn --arg m "$message" '{error: {message: $m, type: "invalid_request_error"}}' >"$body"
  else
    : >"$body"
  fi
  python3 "$STAGING/status_server.py" "$status" "$body" "$port" &
  SERVER_PID=$!
  wait_for "$port"
  BASE_URL="http://127.0.0.1:$(cat "$port")/v1"
}

# closed_port: a loopback port nothing listens on.
closed_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

# capture NAME BASE_URL MODEL: one `opencode run` against BASE_URL, staged,
# redacted, as NAME.stderr and NAME.jsonl. The run must exit 1.
capture() {
  local name="$1" url="$2" model="$3" box="$STAGING/$1" rc opencode_dir
  mkdir -p "$box"/{home,config/opencode,data,cache,state,tmp,repo}
  (
    cd "$box/repo"
    git init -q
    printf 'fixture\n' >README.md
    git add README.md
    git -c user.name=fixture -c user.email=fixture@example.invalid commit -qm init
  )
  jq -n --arg url "$url" --arg model "$MODEL_ID" '{
    "$schema": "https://opencode.ai/config.json",
    share: "disabled",
    autoupdate: false,
    enabled_providers: ["lmstudio"],
    provider: {lmstudio: {
      npm: "@ai-sdk/openai-compatible",
      env: [],
      options: {baseURL: $url, apiKey: ""},
      models: {($model): {name: $model}}
    }},
    agent: {title: {disable: true}},
    permission: {bash: "deny", edit: "deny", webfetch: "deny", external_directory: "deny"}
  }' >"$box/config/opencode/opencode.json"

  opencode_dir="$(dirname "$(command -v opencode)")"
  set +e
  printf 'Say hello.' | env -i \
    PATH="$opencode_dir:/usr/bin:/bin" \
    HOME="$box/home" TMPDIR="$box/tmp" \
    XDG_CONFIG_HOME="$box/config" XDG_DATA_HOME="$box/data" \
    XDG_CACHE_HOME="$box/cache" XDG_STATE_HOME="$box/state" \
    OPENCODE_DISABLE_MODELS_FETCH=1 OPENCODE_DISABLE_AUTOUPDATE=1 \
    OPENCODE_DISABLE_LSP_DOWNLOAD=1 OPENCODE_DISABLE_DEFAULT_PLUGINS=1 \
    OPENCODE_DISABLE_SHARE=1 OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1 \
    OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1 OPENCODE_DISABLE_EXTERNAL_SKILLS=1 \
    perl -e 'alarm 150; exec @ARGV' -- \
    opencode run --format json --print-logs --log-level ERROR \
    -m "$model" --dir "$box/repo" \
    >"$box/raw.stdout" 2>"$box/raw.stderr"
  rc=$?
  set -e
  stop_server || die "a model server outlived its leg"
  [ "$rc" -eq 1 ] || die "opencode run for $name exited $rc, not 1; no fixture was written"

  local roots
  roots="$(jq -cn --arg a "$STAGING" --arg b "$box" '[$a, $b]')"
  jq -c -s --arg mode stream --argjson roots "$roots" -f "$FILTER" "$box/raw.stdout" | normalize >"$STAGING/stage/$name.jsonl"
  jq -j -R -s --arg mode stderr --argjson roots "$roots" -f "$FILTER" "$box/raw.stderr" | normalize >"$STAGING/stage/$name.stderr"
  echo "captured $name (exit $rc)"
}

# normalize replaces the ids and clocks a run prints with fixed placeholders.
normalize() {
  sed -E \
    -e 's/ses_[0-9A-Za-z]{8,}/ses_fixture0000000000000000001/g' \
    -e 's/msg_[0-9A-Za-z]{8,}/msg_fixture0000000000000000001/g' \
    -e 's/err_[0-9A-Za-z]{8,}/err_fixture/g' \
    -e 's/run=[0-9a-f]{8}/run=00000000/g' \
    -e 's/timestamp=[0-9T:.-]+Z/timestamp=2026-01-01T00:00:00.000Z/g' \
    -e 's/"timestamp":[0-9]+/"timestamp":1767225600000/g' \
    -e 's/"date":"[^"]*"/"date":"Thu, 01 Jan 2026 00:00:00 GMT"/g' \
    -e 's/"server":"BaseHTTP[^"]*"/"server":"fixture"/g'
}

start_stub overflow
capture overflow-openai "$BASE_URL" "lmstudio/$MODEL_ID"
start_status 400 "The prompt is greater than the context length of the loaded model."
capture overflow-lmstudio "$BASE_URL" "lmstudio/$MODEL_ID"
start_status 400 "prompt too long; exceeded max context length by 1024 tokens"
capture overflow-ollama "$BASE_URL" "lmstudio/$MODEL_ID"
start_status 400 "the request exceeds the available context size, try increasing it"
capture overflow-llamacpp "$BASE_URL" "lmstudio/$MODEL_ID"
start_stub error
capture provider-error "$BASE_URL" "lmstudio/$MODEL_ID"
start_status 401 ""
capture auth "$BASE_URL" "lmstudio/$MODEL_ID"
capture server-down "http://127.0.0.1:$(closed_port)/v1" "lmstudio/$MODEL_ID"
start_stub bash-then-stop
capture model-not-configured "$BASE_URL" "lmstudio/qwen/not-a-configured-model"

# The shapes the tests are written against, checked before anything moves.
for name in overflow-openai overflow-lmstudio overflow-ollama overflow-llamacpp; do
  [ "$(jq -s '[.[] | select(.type == "error" and .error.name == "ContextOverflowError")] | length > 0' "$STAGING/stage/$name.jsonl")" = true ] ||
    die "refused: OpenCode did not name the $name failure ContextOverflowError"
  grep -F 'AI_APICallError: ' "$STAGING/stage/$name.stderr" >/dev/null ||
    die "refused: the $name stderr has no AI_APICallError line"
done
for name in provider-error auth server-down; do
  [ "$(jq -s '[.[] | select(.type == "error" and .error.name == "APIError")] | length > 0' "$STAGING/stage/$name.jsonl")" = true ] ||
    die "refused: OpenCode did not name the $name failure APIError"
done
grep -F 'ProviderModelNotFoundError: Model not found: lmstudio/qwen/not-a-configured-model' \
  "$STAGING/stage/model-not-configured.stderr" >/dev/null ||
  die "refused: the model-not-configured stderr has no ProviderModelNotFoundError"

# LM Studio's answer to a model id it does not have, when one is listening.
if curl -s -m 5 -o /dev/null http://127.0.0.1:1234/v1/models; then
  curl -s -m 60 http://127.0.0.1:1234/v1/chat/completions -H 'content-type: application/json' \
    -d '{"model":"qwen/nightgauge-missing-model","messages":[{"role":"user","content":"hi"}],"max_tokens":1}' |
    jq '{requested: "qwen/nightgauge-missing-model", served: .model, error: .error}' \
      >"$STAGING/stage/lmstudio-unknown-model.json"
  echo "captured lmstudio-unknown-model"
else
  echo "no LM Studio on 127.0.0.1:1234: lmstudio-unknown-model.json not captured" >&2
fi

refused=0
for file in "$STAGING"/stage/*; do
  if grep -F -- "$STAGING" "$file" >/dev/null; then
    echo "capture-opencode-failure-fixture.sh: refused: $(basename "$file") names the capture's sandbox directory" >&2
    refused=1
  fi
done
bash "$CHECKER" --check "$STAGING"/stage/* || refused=1
[ "$refused" -eq 0 ] || die "refused: no fixture was written"
mkdir -p "$OUT"
mv "$STAGING"/stage/* "$OUT/"
echo "captured opencode $version into $OUT"
