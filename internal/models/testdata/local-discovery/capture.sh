#!/usr/bin/env bash
# Re-captures the local model server responses local_test.go reads, redacted.
#
#   bash internal/models/testdata/local-discovery/capture.sh
#
# Needs an LM Studio server and an Ollama server on this machine. Each root can
# be moved with an environment variable, but only to another loopback port:
#
#   LMSTUDIO_ROOT         default http://127.0.0.1:1234
#   OLLAMA_ROOT           default http://127.0.0.1:11434
#   OLLAMA_MODEL          an Ollama model whose Modelfile sets no num_ctx
#                         (default qwen3:0.6b)
#   OLLAMA_MODEL_NUM_CTX  an Ollama model whose Modelfile sets num_ctx
#                         (default qwen3-ctx32k; README.md says how it was made)
#
# Writes lmstudio-api-v0-models.json (GET /api/v0/models, whitespace aside),
# lmstudio-api-v1-models.json (GET /api/v1/models, whitespace aside; the
# committed one is transcribed from LM Studio's docs until this re-captures it),
# ollama-api-show-num-ctx.json and ollama-api-show-no-num-ctx.json (POST
# /api/show for each Ollama model, trimmed as below). Update README.md's
# provenance table in the same change, and re-run `go test ./internal/models/`.
#
# Redaction, and why each step exists:
#   - A root that is not 127.0.0.1 or localhost is refused before any request:
#     a capture is of a server on this machine, never of one on the network.
#   - Ollama's `license`, `modelfile`, `template`, `tensors` and `modified_at`
#     are dropped. Discovery reads none of them, they are most of the
#     response, `modelfile` names the blob path under the capturing user's
#     model directory, and `modified_at` carries the machine's UTC offset.
#   - The capturing user's home directory is replaced with `~`.
#   - The capture is refused if it names any IPv4 address other than 127.0.0.1:
#     nothing machine-specific belongs in a committed fixture.
#   - Each file is formatted with the repository's Prettier, which the format
#     check holds every JSON file to, so a re-capture of the same responses
#     is byte-identical. Only whitespace changes.
#
# Every capture is made and checked in a private staging directory, and moved
# into this directory only after all of them pass, so a refused capture never
# reaches a committed path. The staging directory is removed on every exit.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
lmstudio_root="${LMSTUDIO_ROOT:-http://127.0.0.1:1234}"
ollama_root="${OLLAMA_ROOT:-http://127.0.0.1:11434}"
ollama_model="${OLLAMA_MODEL:-qwen3:0.6b}"
ollama_model_num_ctx="${OLLAMA_MODEL_NUM_CTX:-qwen3-ctx32k}"

for root in "$lmstudio_root" "$ollama_root"; do
  case "$root" in
    http://127.0.0.1:[0-9]* | http://localhost:[0-9]*) ;;
    *)
      echo "capture.sh: a root must be http://127.0.0.1:<port> or http://localhost:<port>; no request was sent" >&2
      exit 1
      ;;
  esac
done

staging="$(mktemp -d "${TMPDIR:-/tmp}/local-discovery-capture.XXXXXX")"
trap 'rm -rf "$staging"' EXIT

# Bounded, no redirect followed, no proxy, http only.
fetch() {
  curl --silent --show-error --fail --max-time 5 --max-redirs 0 --proto '=http' --noproxy '*' "$@"
}

fetch "$lmstudio_root/api/v0/models" >"$staging/lmstudio-api-v0-models.json"
fetch "$lmstudio_root/api/v1/models" >"$staging/lmstudio-api-v1-models.json"

show() {
  fetch -X POST -H 'Content-Type: application/json' \
    --data "{\"model\":\"$1\"}" "$ollama_root/api/show" |
    python3 -c '
import json, sys
body = json.load(sys.stdin)
for dropped in ("license", "modelfile", "template", "tensors", "modified_at"):
    body.pop(dropped, None)
json.dump(body, sys.stdout, indent=2)
'
}

show "$ollama_model_num_ctx" >"$staging/ollama-api-show-num-ctx.json"
show "$ollama_model" >"$staging/ollama-api-show-no-num-ctx.json"

names="lmstudio-api-v0-models.json lmstudio-api-v1-models.json ollama-api-show-num-ctx.json ollama-api-show-no-num-ctx.json"
for name in $names; do
  f="$staging/$name"
  sed -i.bak -e "s#${HOME}#~#g" "$f" && rm -f "$f.bak"
  if [ -n "$(tail -c 1 "$f")" ]; then
    printf '\n' >>"$f"
  fi
  (cd "$repo" && npx --no-install prettier --stdin-filepath "internal/models/testdata/local-discovery/$name") <"$f" >"$f.fmt"
  mv "$f.fmt" "$f"
  # Every dotted quad on its own line, so 127.0.0.1 on the same line cannot
  # hide another address. No `grep -q`: its early exit would SIGPIPE the first
  # grep, and pipefail would turn a found address into a pass.
  others="$(grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' "$f" | grep -vxF '127.0.0.1' || true)"
  if [ -n "$others" ]; then
    echo "capture.sh: the $name capture names an IPv4 address other than 127.0.0.1; no fixture was written" >&2
    exit 1
  fi
done

for name in $names; do
  mv "$staging/$name" "$here/$name"
done
echo "capture.sh: wrote $names"
