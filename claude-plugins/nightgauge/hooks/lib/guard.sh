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
#      machine-local fallback. When several bundles are installed, the NEWEST
#      one wins (#356) — see the selection rule below.
#   5. ~/go/bin/nightgauge — `go install` default path, common dev setup.
#
# Newest-bundle rule and staleness signal (#356): VSCode keeps older extension
# versions on disk until it restarts, so step 4's glob routinely matches more
# than one bundle. Pre-#356 the FIRST match won; bash globs are collation
# sorted and the extension stamps an epoch-suffixed `0.1.<seconds>` version, so
# "first" meant "oldest" — every hook in every repo outside a nightgauge
# checkout silently ran a superseded binary, with no signal anywhere. Selection
# is now by bundle version, compared component-wise and numerically (see
# `_ng_version_gt`). The bundle version is the only totally ordered signal
# available without an exec: the binary's own stamp is a `git describe` string
# and the plugin version is hand-maintained, and neither is comparable to the
# other or to the bundle version. Modification time is NOT usable either — a
# real capture (internal/doctor/testdata/vscode-bundles/) has the older-
# versioned bundle carrying the LATER mtime.
#
# When the selected bundle is nevertheless not the newest one installed (the
# newer bundle's binary exists but is not executable — partial install, lost
# exec bit), guard.sh emits ONE line naming both bundle versions and the
# resolved path. It is routed exactly like the skip notice: side-channel log by
# default, stderr only under `NIGHTGAUGE_HOOK_SILENT=false`. It never touches
# stdout, which carries the hook's JSON contract (#354/#355), and it never
# defaults to stderr, which the Claude CLI turns into a `stop-hook-error`
# notification (#3262). Nothing here execs the binary: this hook runs on EVERY
# tool call, and guard.sh already pays for one exec below.
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
#
# Parity contract (#277): internal/doctor/binary_resolve.go's ResolveBinary()
# is the canonical Go-side implementation of this same cascade (used by
# `nightgauge doctor`'s binary self-check), and
# internal/doctor/binary_resolve_test.go pins the five filesystem-based steps
# below against this file's resolution order. If you change a step here,
# update binary_resolve.go and its parity test in the same PR.

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

# _ng_version_gt returns 0 (true) when bundle version $1 sorts strictly after
# bundle version $2, comparing dotted components left to right as integers.
# Missing or non-numeric components count as 0.
#
# Each argument is first reduced to its dotted numeric prefix by trimming
# everything from the FIRST `-`. VS Code names platform-specific extension
# directories `<publisher>.<name>-<version>-<targetPlatform>`, and
# .github/workflows/release.yml packages every released VSIX with
# `vsce package --target <t>`, so a released bundle dir is
# `nightgauge.nightgauge-vscode-0.2.1-darwin-arm64` and the extracted version is
# `0.2.1-darwin-arm64`. Without the trim the final component `1-darwin-arm64` is
# not all-digits, collapses to 0, and every pair of releases differing only in
# patch compares EQUAL — so the strict `>` selection below keeps the FIRST glob
# match (the older bundle) and #356 survives untouched on every non-dev install.
# Only dev-install.sh's untargeted `0.1.<epoch>` builds were unaffected. vsce
# requires a strict `x.y.z` version and rejects semver prerelease tags, so the
# first `-` is always the target-platform boundary.
#
# Written to the macOS system bash floor (3.2.57): no arrays, no `mapfile`, no
# `${var,,}`, no `[[ =~ ]]`. `sort -V` is deliberately avoided too — it exists
# on macOS but is not guaranteed across BSD userlands, and this is three lines
# of parameter expansion. Mirrors compareBundleVersions() /
# bundleVersionNumericPrefix() in internal/doctor/binary_resolve.go.
_ng_version_gt() {
  _ng_vg_a="${1%%-*}"
  _ng_vg_b="${2%%-*}"
  while [ -n "$_ng_vg_a" ] || [ -n "$_ng_vg_b" ]; do
    _ng_vg_a_part="${_ng_vg_a%%.*}"
    _ng_vg_b_part="${_ng_vg_b%%.*}"
    case "$_ng_vg_a" in *.*) _ng_vg_a="${_ng_vg_a#*.}" ;; *) _ng_vg_a="" ;; esac
    case "$_ng_vg_b" in *.*) _ng_vg_b="${_ng_vg_b#*.}" ;; *) _ng_vg_b="" ;; esac
    case "$_ng_vg_a_part" in '' | *[!0-9]*) _ng_vg_a_part=0 ;; esac
    case "$_ng_vg_b_part" in '' | *[!0-9]*) _ng_vg_b_part=0 ;; esac
    if [ "$_ng_vg_a_part" -gt "$_ng_vg_b_part" ]; then
      return 0
    fi
    if [ "$_ng_vg_a_part" -lt "$_ng_vg_b_part" ]; then
      return 1
    fi
  done
  return 1
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
#    Selects the NEWEST installed bundle, never the first glob match (#356).
_ng_bundle_selected_version=""
_ng_bundle_newest_version=""
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  # Plain glob expansion, no `find`; the trailing /dist/bin path is the
  # canonical location set by the extension's build script. An unmatched glob
  # expands to the literal pattern, which the -f test rejects.
  for candidate in "$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge; do
    [ -f "$candidate" ] || continue
    _ng_bundle_dir="${candidate%/dist/bin/nightgauge}"
    _ng_bundle_version="${_ng_bundle_dir##*/nightgauge.nightgauge-vscode-}"
    [ -n "$_ng_bundle_version" ] || continue

    # Track the newest bundle PRESENT (runnable or not) so a newer-but-broken
    # install is still reported rather than silently skipped.
    if [ -z "$_ng_bundle_newest_version" ] || _ng_version_gt "$_ng_bundle_version" "$_ng_bundle_newest_version"; then
      _ng_bundle_newest_version="$_ng_bundle_version"
    fi

    [ -x "$candidate" ] || continue
    if [ -z "$_ng_bundle_selected_version" ] || _ng_version_gt "$_ng_bundle_version" "$_ng_bundle_selected_version"; then
      _ng_bundle_selected_version="$_ng_bundle_version"
      NIGHTGAUGE_BINARY="$candidate"
    fi
  done
  unset _ng_bundle_dir _ng_bundle_version candidate
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

# Staleness signal (#356). Fires only when step 4 selected a bundle AND a newer
# bundle is installed — i.e. the newer bundle's binary exists but is not
# executable, so the hooks are about to run a superseded binary. A correctly
# resolved newest bundle stays silent: this code runs on every tool call and a
# per-call log line would be noise, not signal.
#
# Routing is the same as the skip notice and for the same reason (#3262):
# side-channel log by default, stderr only in verbose mode. Never stdout — that
# belongs to the hook's JSON contract (#354/#355).
#
# The gate is `_ng_version_gt newest selected` — a NUMERIC strictly-newer test,
# not string inequality. Both versions were CHOSEN with the numeric comparator,
# so testing them as strings mixes two orderings: two bundles that tie
# numerically but differ textually (the same release packaged for two target
# platforms) let the newest-tracker latch onto whichever the glob yielded first
# and fire this warning BACKWARDS, naming an older bundle as "newer" on a
# machine that is running the newest binary. Same comparator both places.
if [ -n "$_ng_bundle_selected_version" ] &&
  [ -n "$_ng_bundle_newest_version" ] &&
  _ng_version_gt "$_ng_bundle_newest_version" "$_ng_bundle_selected_version"; then
  _ng_stale_message="[stale-binary] hooks resolved VSCode extension bundle $_ng_bundle_selected_version but newer bundle $_ng_bundle_newest_version is installed (its binary is not executable); running $NIGHTGAUGE_BINARY"
  if [ "$NIGHTGAUGE_HOOK_SILENT" = "false" ]; then
    echo "$_ng_stale_message" >&2
  else
    _log_to_side_channel "$_ng_stale_message"
  fi
  unset _ng_stale_message
fi
unset _ng_bundle_selected_version _ng_bundle_newest_version

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
