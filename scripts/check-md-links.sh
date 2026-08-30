#!/usr/bin/env bash
#
# check-md-links.sh — validate cross-document reference integrity across the
# human-facing documentation corpus (root-level *.md + docs/**).
#
# This guards against the dangling doc/reference rot fixed in #135 (removed
# strategy docs, wrong relative depths, archived-repo issue links) regressing.
#
# Scope + config rationale — why external http(s) URLs and image assets are
# skipped — live in .markdown-link-check.json. Generated plugin copies
# (claude-plugins/**) and portable skill templates (skills/**) are intentionally
# OUT of scope: their relative links resolve against a different tree depth or
# the consuming repo, not this one.
#
# --- An errored request is not a dead link (#1004) ---------------------------
#
# `markdown-link-check` reports `Status: 0` for a request that never completed —
# DNS failure, timeout, TLS reset, connection refused, or a bot filter dropping
# the runner before any response. That is not an HTTP status and it is not
# evidence about the document: it is the CHECK failing to run, and it used to
# fail the job identically to a genuine 404. It fired on PR #1003 against two
# URLs that PR never touched, both of which answered from a developer machine at
# the same moment, and a re-run of the identical job on the identical tree
# passed. A required check that depends on third-party bot policy is not a gate.
#
# Config alone cannot fix it: `aliveStatusCodes` already covers 401/403/429 (a
# bot-walled host passes), but adding `0` would also accept NXDOMAIN, which is
# exactly the class the config's `docs.openai.com/codex` ignore entry documents
# as genuinely dead. So every `Status: 0` URL is RE-PROBED here with curl and
# sorted into three classes:
#
#   dead                     an HTTP 404/410, or curl exit 6 (could not resolve
#                            host) after retries. NXDOMAIN is deterministic and
#                            is a real broken citation. FATAL.
#   unreachable-from-runner  any other curl transport failure (7 refused,
#                            28 timeout, 35 TLS, 56 reset) after retries. The
#                            check could not run. Reported by URL and curl exit
#                            code; NOT fatal.
#   alive-after-reprobe      any HTTP response other than 404/410 — a slow or
#                            bot-walled host. NOT fatal.
#
# Internal and relative links never enter this path. The checker reports those
# with a real status (a missing file is `Status: 400`), and any non-alive status
# there stays fatal unconditionally — those are what this gate exists for, they
# cost nothing to verify, and they are entirely under this repo's control.
#
# Wired into .github/workflows/lint.yml and scripts/ci-local.sh (#135).
# Regression suite: scripts/test-check-md-links.sh.
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

CONFIG="${MD_LINK_CHECK_CONFIG:-.markdown-link-check.json}"
MLC_VERSION="3.14.2"

# Install the pinned checker into a throwaway prefix. This deliberately avoids
# both a committed devDependency (keeps the lean root lockfile untouched) and
# `npx` auto-install confirmation semantics, which are not honored on every
# runner ("npx canceled ... no YES option"). Plain `npm install` is
# non-interactive by default and behaves identically locally and in CI.
#
# MD_LINK_CHECK_BIN lets the regression suite reuse one install across its
# fixtures instead of paying the npm round-trip per case.
TMP_PREFIX=""
cleanup() { [ -n "$TMP_PREFIX" ] && rm -rf "$TMP_PREFIX"; return 0; }
trap cleanup EXIT

MLC="${MD_LINK_CHECK_BIN:-}"
if [ -z "$MLC" ]; then
  TMP_PREFIX="$(mktemp -d)"
  echo "Installing markdown-link-check@${MLC_VERSION} (throwaway prefix)…"
  if ! npm install --no-save --no-audit --no-fund --loglevel=error \
    --prefix "$TMP_PREFIX" "markdown-link-check@${MLC_VERSION}" >/dev/null 2>&1; then
    echo "✗ Failed to install markdown-link-check@${MLC_VERSION}" >&2
    exit 1
  fi
  MLC="$TMP_PREFIX/node_modules/.bin/markdown-link-check"
fi
if [ ! -x "$MLC" ]; then
  echo "✗ markdown-link-check binary not found at $MLC" >&2
  exit 1
fi

# Corpus enumerated from git so untracked scratch files are never scanned.
# MD_LINK_CHECK_FILES lets the regression suite point the gate at fixtures.
if [ -n "${MD_LINK_CHECK_FILES:-}" ]; then
  FILES="$MD_LINK_CHECK_FILES"
else
  FILES="$(
    {
      git ls-files '*.md' | grep -E '^[^/]+\.md$'
      git ls-files 'docs/*.md' 'docs/**/*.md'
    } | sort -u
  )"
fi

# reprobe_url URL -> prints "<class> <detail>", where class is one of
# dead | unreachable-from-runner | alive-after-reprobe.
#
# `--retry 3 --retry-all-errors` is what makes the verdict a property of the
# host rather than of one unlucky packet: a transport failure has to reproduce
# across four attempts before it is reported at all, and only NXDOMAIN — which
# is deterministic — is allowed to be called dead from a transport failure.
reprobe_url() {
  local url="$1" code rc
  code="$(curl -sS -A "nightgauge-link-check" -o /dev/null \
    -w '%{http_code}' --max-time "${MD_LINK_CHECK_TIMEOUT:-20}" \
    --retry "${MD_LINK_CHECK_RETRIES:-3}" --retry-all-errors \
    -L "$url" 2>/dev/null)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    case "$code" in
      404 | 410) printf 'dead HTTP %s\n' "$code" ;;
      *) printf 'alive-after-reprobe HTTP %s\n' "$code" ;;
    esac
    return
  fi
  if [ "$rc" -eq 6 ]; then
    # Could not resolve host. Deterministic, and a real broken citation.
    printf 'dead could not resolve host (curl exit 6)\n'
    return
  fi
  printf 'unreachable-from-runner curl exit %s\n' "$rc"
}

FAIL=0
FAILED_FILES=""
COUNT=0
N_DEAD=0
N_UNREACHABLE=0
N_ALIVE=0
DEAD_LINES=""
UNREACHABLE_LINES=""
ALIVE_LINES=""

while IFS= read -r f; do
  [ -z "$f" ] && continue
  COUNT=$((COUNT + 1))
  # stdin from /dev/null so the checker never consumes the piped file list.
  OUT="$("$MLC" --config "$CONFIG" --quiet "$f" </dev/null 2>&1)"
  RC=$?
  [ "$RC" -eq 0 ] && continue

  printf '%s\n' "$OUT"

  # Every reported failure for this file, split by whether the checker got an
  # answer at all. `Status: 0` means it did not.
  ERRORED="$(printf '%s\n' "$OUT" | sed -n 's/.*\[✖\] \(.*\) → Status: 0$/\1/p')"
  ANSWERED="$(printf '%s\n' "$OUT" | sed -n 's/.*\[✖\] \(.*\) → Status: \([0-9]*\)$/\1 \2/p' | grep -v ' 0$')"

  FILE_FATAL=0

  # A link the checker got a real status for is this gate's own verdict —
  # internal/relative links land here (a missing file reads Status: 400) and are
  # always fatal.
  if [ -n "$ANSWERED" ]; then
    FILE_FATAL=1
    while IFS= read -r line; do
      [ -z "$line" ] && continue
      N_DEAD=$((N_DEAD + 1))
      DEAD_LINES="${DEAD_LINES}"$'\n'"    ${f}: ${line% *} (Status: ${line##* })"
    done <<EOF
$ANSWERED
EOF
  fi

  if [ -n "$ERRORED" ]; then
    while IFS= read -r url; do
      [ -z "$url" ] && continue
      verdict="$(reprobe_url "$url")"
      class="${verdict%% *}"
      detail="${verdict#* }"
      case "$class" in
        dead)
          FILE_FATAL=1
          N_DEAD=$((N_DEAD + 1))
          DEAD_LINES="${DEAD_LINES}"$'\n'"    ${f}: ${url} (${detail})"
          ;;
        unreachable-from-runner)
          N_UNREACHABLE=$((N_UNREACHABLE + 1))
          UNREACHABLE_LINES="${UNREACHABLE_LINES}"$'\n'"    ${f}: ${url} (${detail})"
          ;;
        alive-after-reprobe)
          N_ALIVE=$((N_ALIVE + 1))
          ALIVE_LINES="${ALIVE_LINES}"$'\n'"    ${f}: ${url} (${detail})"
          ;;
      esac
    done <<EOF
$ERRORED
EOF
  fi

  # A file whose only failures re-probed as alive or unreachable is NOT a
  # failure. The checker's exit code alone cannot say that, which is the whole
  # defect (#1004).
  if [ "$FILE_FATAL" -eq 1 ]; then
    FAIL=1
    FAILED_FILES="${FAILED_FILES}"$'\n'"  - ${f}"
  fi
done <<EOF
$FILES
EOF

echo ""
echo "-------------------------------------------------------------------------"
# One line per class, always — a red run has to say WHICH class it found
# without anyone opening the raw log, and a green run has to disclose what it
# could not verify.
echo "Link classes: ${N_DEAD} dead, ${N_UNREACHABLE} unreachable-from-runner, ${N_ALIVE} alive-after-reprobe"
if [ -n "$DEAD_LINES" ]; then
  echo "  dead (fatal — a missing document or an unresolvable host):${DEAD_LINES}"
fi
if [ -n "$UNREACHABLE_LINES" ]; then
  echo "  unreachable-from-runner (NOT fatal — the check could not run):${UNREACHABLE_LINES}"
fi
if [ -n "$ALIVE_LINES" ]; then
  echo "  alive-after-reprobe (NOT fatal — the host answered on re-probe):${ALIVE_LINES}"
fi
echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "✓ Markdown link check passed — ${COUNT} files, no dead links."
else
  echo "✗ Markdown link check found dead links in:${FAILED_FILES}"
  echo ""
  echo "Fix the links above. If a link is legitimately external/private or an"
  echo "asset, adjust the scope or ignore rules in ${CONFIG}."
fi
exit "$FAIL"
