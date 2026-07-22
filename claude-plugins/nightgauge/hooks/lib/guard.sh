#!/bin/bash
# Shared guard — locates the nightgauge binary.
#
# Source this from hook wrappers BEFORE invoking $NIGHTGAUGE_BINARY:
#   source "$SCRIPT_DIR/lib/guard.sh"
#
# Resolution order (#3234, #4029):
#   0. $NIGHTGAUGE_BIN — exported by the host that spawns the skill/CLI
#      (the VSCode extension's skillRunner, or the Go binary in auto/CLI mode).
#      Honored first so resolution is identical to the skill cascade (#4029).
#   1. PATH lookup (`command -v nightgauge`)
#   2. $REPO_ROOT/bin/nightgauge (same-repo build, where REPO_ROOT comes
#      from `git rev-parse --show-toplevel`)
#   3. $CANONICAL_REPO/bin/nightgauge — the canonical repo when invoked
#      from inside a git worktree (`git rev-parse --git-common-dir` → `.git`
#      of the parent worktree). Pre-#3234 every concurrent-mode pipeline run
#      that triggered the Stop hook hard-failed here because worktrees do not
#      inherit `bin/` build artifacts. The agent then went silent for 100+
#      minutes until skillRunner stall-killed the stage.
#   4. ~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge
#      — the binary that ships with the VSCode extension. Every user of the
#      extension has a current copy here, so this is the most reliable
#      machine-local fallback.
#   5. ~/go/bin/nightgauge — `go install` default path, common dev setup.
#
# Sync contract with skills/_shared/PREFLIGHT.md (#3262 → #4029): the SHARED
# resolution order ($NIGHTGAUGE_BIN → PATH → repo → canonical → ~/go/bin)
# stays mirrored in both. This file INTENTIONALLY diverges by keeping step 4
# (the ~/.vscode/extensions glob): guard.sh is Claude-Code-only and must serve
# the standalone-terminal-Claude case where no host exports $NIGHTGAUGE_BIN.
# Skills must stay portable Markdown (Claude/Codex/Cursor/Gemini) and therefore
# OMIT that VSCode-extension-specific path — the `nightgauge preflight
# skill-portability` gate enforces it. When changing a SHARED step here, mirror
# it in PREFLIGHT.md; the vscode glob is the sole guard.sh-only fallback.
#
# Skip-or-fail policy:
#   - When the binary cannot be resolved, behavior depends on the calling
#     hook's `NIGHTGAUGE_HOOK_BLOCKING` setting (set BEFORE sourcing):
#       - `true`  → exit 1 with the error message on stderr (load-bearing,
#                   e.g. file protection — a missing protector is a real
#                   safety violation that the user MUST see).
#       - `false` (default) → write a `[hook-skipped]` line to the side-
#                   channel log file and exit 0. Stop verification, format-
#                   on-save, etc. are best-effort observers; their failure
#                   must not block the assistant turn AND must not surface
#                   to the parent agent as a `stop-hook-error` notification
#                   (#3262).
#
# Silent-by-default rationale (#3262): the Claude CLI surfaces hook stderr
# to the parent agent as a `stop-hook-error` notification regardless of exit
# code. Pre-#3262 the graceful-skip path wrote `[hook-skipped] …` to stderr;
# during the autonomous run of #3224 those notifications occasionally caused
# the LLM to interpret "stop now" and exit early. `NIGHTGAUGE_HOOK_SILENT=true`
# (default) routes the skip notice to a side-channel log instead. Set
# `NIGHTGAUGE_HOOK_SILENT=false` to restore the old verbose behavior
# when debugging hooks.
#
# Side-channel log path: `${NIGHTGAUGE_HOOK_LOG:-$HOME/.nightgauge/hook-warnings.log}`.
# Documented as a contract in docs/STOP_HOOK_AUDIT.md.
#
# @see Issue #3234 — Stop hook hard-fails on missing binary in worktree mode.
# @see Issue #3262 — Residual stop-hook-error sources after PR #3234.

NIGHTGAUGE_HOOK_BLOCKING="${NIGHTGAUGE_HOOK_BLOCKING:-false}"
NIGHTGAUGE_HOOK_SILENT="${NIGHTGAUGE_HOOK_SILENT:-true}"
NIGHTGAUGE_HOOK_LOG="${NIGHTGAUGE_HOOK_LOG:-$HOME/.nightgauge/hook-warnings.log}"

# _log_to_side_channel appends a single timestamped line to the side-channel
# log, creating the parent directory on first use. Failures are swallowed —
# the log is best-effort diagnostic data, never a failure surface.
_log_to_side_channel() {
  local message="$1"
  local log_dir
  log_dir="$(dirname "$NIGHTGAUGE_HOOK_LOG")"
  mkdir -p "$log_dir" 2>/dev/null || return 0
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$message" >> "$NIGHTGAUGE_HOOK_LOG" 2>/dev/null || true
}

# 0. Host-exported binary (#4029) — the skillRunner / Go auto-CLI host resolves
#    the binary authoritatively and exports it. Honored first so guard.sh and the
#    skill PREFLIGHT cascade resolve identically. Ignore a stale/non-exec value.
NIGHTGAUGE_BINARY="${NIGHTGAUGE_BIN:-}"
if [ -n "$NIGHTGAUGE_BINARY" ] && [ ! -x "$NIGHTGAUGE_BINARY" ]; then
  NIGHTGAUGE_BINARY=""
fi

# 1. PATH lookup
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  NIGHTGAUGE_BINARY="$(command -v nightgauge 2>/dev/null || true)"
fi

# 2. Same-repo build (or worktree's own bin if anyone bothered to drop a binary
#    there, which is unusual but harmless to check).
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  if [ -x "$REPO_ROOT/bin/nightgauge" ]; then
    NIGHTGAUGE_BINARY="$REPO_ROOT/bin/nightgauge"
  fi
fi

# 3. Canonical repo when invoked from a worktree.
#    `git rev-parse --git-common-dir` returns the .git of the canonical repo
#    when called from inside a worktree (and the worktree's own .git when not
#    in one — same as --show-toplevel for that case, which we already
#    handled).
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    # GIT_COMMON_DIR is the `.git` directory; its parent is the canonical
    # working tree.
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    if [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ]; then
      NIGHTGAUGE_BINARY="$CANONICAL_REPO/bin/nightgauge"
    fi
  fi
fi

# 4. VSCode extension-bundled binary (every extension user has a fresh copy).
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  # `set -- glob` to expand without invoking find; the trailing /dist/bin path
  # is the canonical location set by the extension's build script.
  for candidate in "$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge; do
    if [ -x "$candidate" ]; then
      NIGHTGAUGE_BINARY="$candidate"
      break
    fi
  done
fi

# 5. `go install` default location.
if [ -z "$NIGHTGAUGE_BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ]; then
  NIGHTGAUGE_BINARY="$HOME/go/bin/nightgauge"
fi

if [ -z "$NIGHTGAUGE_BINARY" ] || [ ! -x "$NIGHTGAUGE_BINARY" ]; then
  if [ "$NIGHTGAUGE_HOOK_BLOCKING" = "true" ]; then
    # Load-bearing hook: stderr stays loud regardless of silent mode — the
    # user MUST see this. Mirror to the side-channel log too so the diagnostic
    # is preserved if the user later wants to inspect hook history.
    echo "ERROR: nightgauge binary not found." >&2
    echo "  This hook is marked as load-bearing — refusing to run without the binary." >&2
    echo "  Build with:    go build -o bin/nightgauge ./cmd/nightgauge" >&2
    echo "  Or install:    go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
    echo "  Or add the binary to your PATH." >&2
    _log_to_side_channel "[hook-blocked] nightgauge binary not found — load-bearing hook refused to run"
    exit 1
  fi

  # Non-blocking graceful skip: route the diagnostic to the side-channel log
  # (silent default) or stderr (verbose mode). Stderr would surface to the
  # parent agent as a `stop-hook-error` notification (#3262) — the silent
  # path eliminates that noise while preserving the diagnostic for users who
  # debug hooks.
  if [ "$NIGHTGAUGE_HOOK_SILENT" = "false" ]; then
    echo "[hook-skipped] nightgauge binary not found — skipping non-blocking hook (#3234)" >&2
  else
    _log_to_side_channel "[hook-skipped] nightgauge binary not found — skipping non-blocking hook"
  fi
  exit 0
fi

export NIGHTGAUGE_BINARY

# Per-repo GitHub token resolution.
#
# Export GH_TOKEN/GITHUB_TOKEN for the *current repo* so every `gh` call made by
# this hook's shell authenticates as that repo's configured user instead of the
# machine-global `gh auth` active account. `nightgauge forge auth token`
# resolves per-repo (config github_auth.token → github_user-scoped `gh auth token
# --user <github_user>`, which is authoritative over ambient env), so two
# workspaces owned by different GitHub users each get their own token without any
# PAT stored on disk. GH_TOKEN has the highest precedence in the gh CLI
# (GH_TOKEN > GITHUB_TOKEN > keyring/active account).
#
# Authority rule (#4068): for a repo that declares a per-repo identity
# (github_user), that identity is AUTHORITATIVE over the ambient env. An ambient
# (wrong-user) GH_TOKEN — injected by a runner whose active account is the wrong
# user — would otherwise silently shadow the configured identity (the
# Acme-Community → octocat bug). So for a CONFIGURED-IDENTITY repo we
# resolve EVEN WHEN GH_TOKEN is already set, with the ambient GH_TOKEN/GITHUB_TOKEN
# STRIPPED from the resolver's env so the binary reads the keyring entry for the
# configured user, not the shadowing ambient token; when the resolved token
# DIFFERS from the ambient one we override GH_TOKEN/GITHUB_TOKEN.
#
# `forge auth token --identity-only` emits a token ONLY when the repo configures
# a github_user, and prints NOTHING otherwise. That keeps the inverse promise:
# for a repo with NO configured identity (the common single-identity / CI case)
# the resolver returns empty and we leave a correctly-injected ambient token
# untouched — never clobbering it with the machine's default gh account.
#
# Fail-safe by construction:
#   - No configured identity → empty output → ambient value preserved unchanged.
#   - Never fails the hook: resolution errors are swallowed, the ambient token
#     (if any) is preserved, and the hook continues.
# @see forge auth token --identity-only / forge auth assert; docs/CONFIGURATION.md
_ib_repo_token="$(env -u GH_TOKEN -u GITHUB_TOKEN "$NIGHTGAUGE_BINARY" forge auth token --identity-only 2>/dev/null || true)"
if [ -n "$_ib_repo_token" ] && [ "$_ib_repo_token" != "${GH_TOKEN:-}" ]; then
  export GH_TOKEN="$_ib_repo_token"
  export GITHUB_TOKEN="$_ib_repo_token"
fi
unset _ib_repo_token
