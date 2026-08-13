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
#      machine-local fallback. When several bundles are on disk, the one VSCode
#      RECORDS as installed wins (#356) — see the selection rule below.
#   5. ~/go/bin/nightgauge — `go install` default path, common dev setup.
#
# Step 4 selection authority (#356): VSCode keeps superseded extension
# directories on disk until it restarts, so step 4's glob routinely matches
# more than one bundle. Pre-#356 the FIRST match won; bash globs are collation
# sorted and the extension stamps an epoch-suffixed `0.1.<seconds>` version, so
# "first" meant "oldest" — every hook in every repo outside a nightgauge
# checkout silently ran a superseded binary, with no signal anywhere.
#
# The authority is ~/.vscode/extensions/extensions.json, which VSCode writes
# and which names exactly one directory (`relativeLocation`) per installed
# extension. If that directory's dist/bin/nightgauge is executable, it IS the
# answer. Ranking the parsed version numbers instead is wrong three separate
# ways, all reachable here: a maintainer dev-install is permanently
# `0.1.<epoch>` (dev-install.sh derives it from package.json, which stays 0.1.0
# on main) and loses to any leftover 0.2.x release directory; RC bundles are
# `0.2.0-rc.22` / `-rc.23`, which every dotted-numeric comparator ties; and a
# `*.vsctmp` partial-install orphan can out-parse the real install. So this
# file NEVER orders versions — a recorded downgrade resolves to the older
# bundle, silently, because that is what is installed. Versions appear only as
# opaque display strings inside diagnostics.
#
# Fallback: no usable record (file absent, zero or several nightgauge entries
# in it) or a recorded bundle that cannot be run → the first executable glob
# match, exactly as before #356.
#
# Divergence signal: guard.sh emits ONE line — naming the recorded version, the
# resolved version and the resolved path — when the recorded bundle could not
# be used, or when there is no record and several bundles are on disk. A
# confirmed resolution, and a single unrecorded bundle, stay silent. The line
# is routed exactly like the skip notice: side-channel log by default, stderr
# only under `NIGHTGAUGE_HOOK_SILENT=false`. It never touches stdout, which
# carries the hook's JSON contract (#354/#355), and it never defaults to
# stderr, which the Claude CLI turns into a `stop-hook-error` notification
# (#3262). Because the condition is STANDING — a leftover bundle directory or a
# lost exec bit persists for days — the line is not re-appended when it is
# already the last line of the side-channel log; that check costs one `tail`
# and only ever runs on the already-diverging path.
#
# Reading the record costs ONE `grep -o` over a small file, and only when step 4
# is reached AND the glob matched at least one bundle. That fork was measured
# rather than assumed (see _ng_read_recorded_bundle_dir): ~2ms, linear in file
# size. The builtin-only scan written to avoid it was QUADRATIC — 20-900ms per
# invocation on ordinary extensions.json sizes, paid 2-3x per tool call — so
# "no fork" was the expensive option by two orders of magnitude.
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
  { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$message" >> "$NIGHTGAUGE_HOOK_LOG"; } 2>/dev/null || true
}

# _ng_message_already_last succeeds when the LAST line of the side-channel log
# already carries exactly this message (the timestamp _log_to_side_channel
# prepends contains no spaces, so stripping the first field leaves the message).
#
# The `[stale-binary]` condition is STANDING: a leftover bundle directory
# survives days, a lost exec bit never heals itself, and hooks.json fires 2-3
# guard.sh-sourcing wrappers per tool call — so an undeduped signal writes the
# same ~250-byte line thousands of times into an unrotated log, which is
# exactly the "noise, not signal" the healthy path is silent to avoid. One
# `tail` pays for that, and only on the already-diverging path; the healthy
# path never calls this.
_ng_message_already_last() {
  local message="$1"
  local last_line
  [ -f "$NIGHTGAUGE_HOOK_LOG" ] && [ -r "$NIGHTGAUGE_HOOK_LOG" ] || return 1
  last_line="$(tail -n 1 "$NIGHTGAUGE_HOOK_LOG" 2>/dev/null || true)"
  [ "${last_line#* }" = "$message" ]
}

# _ng_read_recorded_bundle_dir sets `_ng_recorded_dir` to the bundle directory
# name VSCode records as this extension's install, or "" when there is no
# usable record.
#
# ~/.vscode/extensions/extensions.json is a JSON array of installed extensions;
# each entry carries `"relativeLocation": "<publisher>.<name>-<version>[-<target>]"`,
# the directory VSCode actually installed into. This extracts the ONE entry
# whose relativeLocation names a nightgauge bundle. Zero matches (not installed,
# file missing, file mangled) and MULTIPLE matches both mean "no record" — fall
# back rather than guess. Multiple entries are not believed to be a state
# VSCode produces for one extension id, but that is an assumption, not a
# guarantee: profiles and pinned installs are plausible producers, and the
# fallback consequence there is first-executable-glob-match plus a
# `[stale-binary]` line — the pre-#356 selection, now at least visible.
#
# ONE fork, deliberately. `jq` is not a dependency and never will be here, and
# the earlier attempt at "no fork at all" — read the file with a builtin loop,
# then walk it with `case` + `${var#*pat}` — is QUADRATIC in file size, because
# every step re-copies and re-glob-matches the whole remaining text. Measured,
# not assumed: /bin/bash 3.2.57, `env -i`, cwd outside any git repo, this
# function in isolation (200 calls) and the real workflow-gate.sh wrapper
# end-to-end (60 runs) against synthetic indexes of each size.
#
#   entries / size   builtin scan   this `grep -o`   wrapper delta vs main
#    14 /  11 KB        11.7 ms         3.2 ms         +14 ms  /  +4 ms
#    60 /  47 KB       109.0 ms         3.4 ms        +115 ms  /  +4 ms
#   120 /  95 KB       373.4 ms         3.9 ms        +384 ms  /  +4 ms
#
# So the fork this used to avoid is the cheap half of the trade by two orders
# of magnitude, and it is FLAT in file size where the builtin scan is not — and
# hooks.json fires 2-3 guard.sh-sourcing wrappers per tool call, so every number
# above is paid several times over per tool call. `grep -a -o -E` is in BSD and
# GNU userland alike. `-a` makes NUL-containing input an explicit text scan, and
# `LC_ALL=C` makes invalid bytes and `[[:space:]]` deterministic regardless of
# the caller's locale. Stderr is redirected because #3262 forbids ANY stderr
# write on the default silent path, and a grep failure degrades to "no record",
# which is the safe fallback.
# Written to the macOS system bash floor (3.2.57): no arrays, no `mapfile`, no
# `${var,,}`, no `[[ =~ ]]`.
#
# Mirrors readRecordedBundleDir() in internal/doctor/binary_resolve.go, which
# applies the same rules; the parity tests pin the two together against the
# same fixtures, including the large one (#277).
#
# `_ng_newline` is a literal newline used as a `case` pattern below; it is set
# once here and must never be empty, since an empty pattern matches everything.
_ng_newline='
'

_ng_read_recorded_bundle_dir() {
  _ng_recorded_dir=""
  _ng_index_file="$HOME/.vscode/extensions/extensions.json"
  # `-r` alone is TRUE for a directory; `-f` is what keeps a directory (or a
  # symlink to one) at that path from reaching a reader at all.
  if [ ! -f "$_ng_index_file" ] || [ ! -r "$_ng_index_file" ]; then
    unset _ng_index_file
    return 0
  fi

  # Whitespace is tolerated around the colon; the value is bounded by the
  # closing quote, so nothing past the recorded directory name can be captured.
  _ng_scan_matches="$(LC_ALL=C grep -a -o -E '"relativeLocation"[[:space:]]*:[[:space:]]*"nightgauge\.nightgauge-vscode-[^"]*"' "$_ng_index_file" 2>/dev/null || true)"

  # Command substitution strips trailing newlines, so exactly one match is a
  # string with no embedded newline — the match itself cannot contain one,
  # since `[[:space:]]` never crosses a line in grep.
  case "$_ng_scan_matches" in
    '') ;;
    *"$_ng_newline"*) ;;
    *)
      _ng_scan_candidate="${_ng_scan_matches%\"}"
      _ng_scan_candidate="${_ng_scan_candidate##*\"}"
      # A record steers a filesystem path: accept a plain directory name
      # only — no separators, no traversal.
      case "$_ng_scan_candidate" in
        */* | *\\* | *..*) ;;
        *) _ng_recorded_dir="$_ng_scan_candidate" ;;
      esac
      ;;
  esac

  unset _ng_index_file _ng_scan_matches _ng_scan_candidate
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
#    Selects the bundle VSCode RECORDS as installed, never the biggest-parsing
#    version (#356); falls back to the first glob match with no record.
_ng_recorded_dir=""
_ng_recorded_version=""
_ng_selected_version=""
_ng_bundle_count=0
_ng_divergence=""
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  _ng_recorded_path=""
  _ng_first_runnable=""
  _ng_first_runnable_version=""
  # Plain glob expansion, no `find`; the trailing /dist/bin path is the
  # canonical location set by the extension's build script. An unmatched glob
  # expands to the literal pattern, which the -f test rejects.
  for candidate in "$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge; do
    [ -f "$candidate" ] || continue
    _ng_bundle_dir="${candidate%/dist/bin/nightgauge}"
    _ng_bundle_base="${_ng_bundle_dir##*/}"
    _ng_bundle_version="${_ng_bundle_base#nightgauge.nightgauge-vscode-}"
    [ -n "$_ng_bundle_version" ] || continue
    _ng_bundle_count=$((_ng_bundle_count + 1))

    if [ -z "$_ng_first_runnable" ] && [ -x "$candidate" ]; then
      _ng_first_runnable="$candidate"
      _ng_first_runnable_version="$_ng_bundle_version"
    fi
  done

  # The record is only consulted when the glob actually matched a bundle:
  # with nothing to choose between there is nothing for the record to decide,
  # and a machine with no extension installed must pay nothing at all for
  # step 4. The recorded directory is a validated plain name, so it is tested
  # directly rather than re-globbed.
  if [ "$_ng_bundle_count" -gt 0 ]; then
    _ng_read_recorded_bundle_dir
    if [ -n "$_ng_recorded_dir" ]; then
      _ng_recorded_version="${_ng_recorded_dir#nightgauge.nightgauge-vscode-}"
    fi
    if [ -n "$_ng_recorded_version" ]; then
      _ng_recorded_candidate="$HOME/.vscode/extensions/$_ng_recorded_dir/dist/bin/nightgauge"
      if [ -f "$_ng_recorded_candidate" ] && [ -x "$_ng_recorded_candidate" ]; then
        _ng_recorded_path="$_ng_recorded_candidate"
      fi
      unset _ng_recorded_candidate
    fi
  fi

  if [ -n "$_ng_recorded_path" ]; then
    NIGHTGAUGE_BINARY="$_ng_recorded_path"
    _ng_selected_version="$_ng_recorded_version"
  elif [ -n "$_ng_first_runnable" ]; then
    NIGHTGAUGE_BINARY="$_ng_first_runnable"
    _ng_selected_version="$_ng_first_runnable_version"
    if [ -n "$_ng_recorded_dir" ]; then
      _ng_divergence="record-unusable"
    elif [ "$_ng_bundle_count" -gt 1 ]; then
      _ng_divergence="unrecorded"
    fi
  fi
  unset _ng_bundle_dir _ng_bundle_base _ng_bundle_version candidate
  unset _ng_recorded_path _ng_first_runnable _ng_first_runnable_version
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

# Divergence signal (#356). Fires only when step 4 resolved a bundle that
# VSCode's install record does NOT confirm:
#   - `record-unusable`: extensions.json names a bundle whose binary is absent
#     or not executable, so the hooks ran a different one.
#   - `unrecorded`: there is no usable record and several bundles are on disk,
#     so the selection is a guess between real candidates.
# A confirmed resolution — and a single unrecorded bundle, which has no
# alternative — stays silent: this code runs on every tool call and a per-call
# log line would be noise, not signal.
#
# The versions in the message are opaque display strings. Nothing here orders
# them: the whole point of #356's redesign is that "installed" is a fact VSCode
# records, not a number to be ranked (a recorded downgrade is healthy).
#
# Routing is the same as the skip notice and for the same reason (#3262):
# side-channel log by default, stderr only in verbose mode. Never stdout — that
# belongs to the hook's JSON contract (#354/#355). Both divergences are
# STANDING conditions, so the log append is skipped when the identical message
# is already the log's last line — the signal stays a signal instead of
# becoming a per-tool-call transcript of one unchanging fact.
if [ -n "$_ng_divergence" ]; then
  if [ "$_ng_divergence" = "record-unusable" ]; then
    _ng_divergence_message="[stale-binary] VSCode records extension bundle $_ng_recorded_version as installed, but its bundled binary is missing or not executable; hooks resolved bundle $_ng_selected_version instead, running $NIGHTGAUGE_BINARY"
  else
    _ng_divergence_message="[stale-binary] no usable VSCode install record for the nightgauge extension (~/.vscode/extensions/extensions.json); $_ng_bundle_count bundles on disk, hooks resolved bundle $_ng_selected_version, running $NIGHTGAUGE_BINARY"
  fi
  if [ "$NIGHTGAUGE_HOOK_SILENT" = "false" ]; then
    echo "$_ng_divergence_message" >&2
  elif ! _ng_message_already_last "$_ng_divergence_message"; then
    _log_to_side_channel "$_ng_divergence_message"
  fi
  unset _ng_divergence_message
fi
unset _ng_recorded_dir _ng_recorded_version _ng_selected_version _ng_bundle_count _ng_divergence

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
