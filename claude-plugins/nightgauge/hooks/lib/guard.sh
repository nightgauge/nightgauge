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
#      from `git rev-parse --show-toplevel`). Development only, see below.
#   3. $CANONICAL_REPO/bin/nightgauge — the canonical repo when invoked
#      from inside a git worktree (`git rev-parse --git-common-dir` → `.git`
#      of the parent worktree). Development only, see below.
#   3a. ~/.nightgauge/bin/nightgauge — the per-user shim (#2765). The
#      extension links it to its own bundled binary on every activation, in
#      whichever editor runs the extension, so hooks resolve from VS Code,
#      VSCodium, Cursor, Windsurf, Insiders and any terminal alike. No
#      editor-specific path is needed to find it.
#   4. ~/.vscode/extensions/nightgauge.nightgauge-[0-9]*/dist/bin/nightgauge
#      — the binary that ships with the VSCode extension. Every user of the
#      extension has a current copy here, so this is the most reliable
#      machine-local fallback. When several bundles are on disk, the one VSCode
#      RECORDS as installed wins (#356) — see the selection rule below.
#   5. ~/go/bin/nightgauge — `go install` default path, common dev setup.
#
# Steps 2-3 gate (#2758): this plugin is user-scoped, so any repository can
# supply a bin/nightgauge, and the `forge auth token` call below would run it
# at source time with credentials in reach. Both steps therefore require
# NIGHTGAUGE_DEV_REPO_BIN=1, set outside the repository and tested before any
# git command, AND a verified core checkout (_ng_repo_bin_trusted).
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
# resolution order ($NIGHTGAUGE_BIN → PATH → gated repo → gated canonical →
# ~/go/bin) stays mirrored in both. This file INTENTIONALLY diverges by keeping
# step 3a (the per-user shim) and step 4 (the ~/.vscode/extensions glob):
# guard.sh is Claude-Code-only and must serve the standalone-terminal-Claude
# case where no host exports $NIGHTGAUGE_BIN.
# Skills must stay portable Markdown (Claude/Codex/Cursor/Gemini) and therefore
# OMIT that VSCode-extension-specific path — the `nightgauge preflight
# skill-portability` gate enforces it. When changing a SHARED step here, mirror
# it in PREFLIGHT.md; the vscode glob is the sole guard.sh-only fallback.
#
# Missing-binary policy (#2765): each wrapper declares what it does when no
# binary resolves by setting `NIGHTGAUGE_HOOK_ON_MISSING_BINARY` BEFORE
# sourcing this file. Every policy exits 0 and writes nothing to stderr on the
# default path, so none of them surfaces as a `stop-hook-error` (#3262).
#   - `skip` (default; an unknown value also means skip) → `[hook-skipped]`
#     side-channel line, exit 0. Best-effort observers: format-on-save,
#     version-check, test-quality, notify, skill-usage-log, inject-context,
#     check-dependencies, and stop-verification, which must skip because
#     blocking a stop without the binary would trap the session.
#   - `announce` → print ONE `{"systemMessage": ...}` line on stdout and exit
#     0. Used only by the SessionStart hook binary-check.sh, so the user sees
#     one visible warning per session that the gates are not running.
#   - `gate` → in a repository that opted in (`_ng_repo_opted_in`), print a
#     PreToolUse deny decision on stdout and exit 0 (`[hook-denied]` in the
#     side-channel log). Claude Code only blocks a PreToolUse call on a deny
#     decision or exit 2, never on exit 1. Elsewhere it behaves like `skip`.
#     Used by workflow-gate and prompt-sanitize.
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
# below (plus the step-3a shim) against this file's resolution order. If you change a step here,
# update binary_resolve.go and its parity test in the same PR.

NIGHTGAUGE_HOOK_ON_MISSING_BINARY="${NIGHTGAUGE_HOOK_ON_MISSING_BINARY:-skip}"
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
  _ng_scan_matches="$(LC_ALL=C grep -a -o -E '"relativeLocation"[[:space:]]*:[[:space:]]*"nightgauge\.nightgauge-[0-9][^"]*"' "$_ng_index_file" 2>/dev/null || true)"

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

# _ng_repo_bin_trusted succeeds when $1 is a verified Nightgauge core checkout
# holding an executable bin/nightgauge. Same rule as repoBinTrusted in
# internal/doctor/binary_resolve.go (#2758); callers test the setting first.
_ng_repo_bin_trusted() {
  local root="$1" origin
  [ -n "$root" ] && [ -x "$root/bin/nightgauge" ] && [ ! -d "$root/bin/nightgauge" ] || return 1
  grep -qx 'module github.com/nightgauge/nightgauge' "$root/go.mod" 2>/dev/null || return 1
  origin="$(git -C "$root" config --get remote.origin.url 2>/dev/null)" || return 1
  case "${origin%.git}" in
    git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
      ssh://git@github.com/nightgauge/nightgauge) return 0 ;;
  esac
  return 1
}

# _ng_repo_opted_in succeeds when the repository holding the working directory
# opted in to Nightgauge: .nightgauge/config.yaml (or the legacy config.json
# the loader still reads) is a regular file at the git
# toplevel (the directory itself outside git) or at a worktree's canonical
# checkout. Same rule as RepoOptedIn in internal/hooks/optin.go (#2765); it
# lives here because the `gate` policy must decide without the binary.
_ng_repo_opted_in() {
  local top common
  top="$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
  [ -n "$top" ] || top="$(pwd)"
  [ -f "$top/.nightgauge/config.yaml" ] || [ -f "$top/.nightgauge/config.json" ] && return 0
  common="$(git rev-parse --git-common-dir 2>/dev/null)" || return 1
  [ -n "$common" ] || return 1
  common="$(cd "$common/.." 2>/dev/null && pwd)" || return 1
  [ -f "$common/.nightgauge/config.yaml" ] || [ -f "$common/.nightgauge/config.json" ]
}

# 2-3. Same-repo build, then the canonical repo when invoked from a worktree
#      (`git rev-parse --git-common-dir` returns its .git). Development only.
if [ -z "$NIGHTGAUGE_BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  if _ng_repo_bin_trusted "$REPO_ROOT"; then
    NIGHTGAUGE_BINARY="$REPO_ROOT/bin/nightgauge"
  else
    GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
    if [ -n "$GIT_COMMON_DIR" ]; then
      CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
      if _ng_repo_bin_trusted "$CANONICAL_REPO"; then
        NIGHTGAUGE_BINARY="$CANONICAL_REPO/bin/nightgauge"
      fi
    fi
  fi
fi

# 3a. Per-user shim written by the extension on activation (#2765).
if [ -z "$NIGHTGAUGE_BINARY" ] && [ -f "$HOME/.nightgauge/bin/nightgauge" ] && \
  [ -x "$HOME/.nightgauge/bin/nightgauge" ]; then
  NIGHTGAUGE_BINARY="$HOME/.nightgauge/bin/nightgauge"
fi

# 4. VSCode extension-bundled binary (every extension user has a fresh copy).
#    Selects the bundle VSCode RECORDS as installed, never the biggest-parsing
#    version (#356); falls back to the first glob match with no record.
_ng_recorded_dir=""
_ng_recorded_version=""
_ng_selected_version=""
_ng_bundle_count=0
_ng_divergence=""
_ng_rejected=""
# Code-signature gate for step 4 (#2776). On Darwin a bundle binary runs only
# when codesign verifies it against this pinned requirement (Apple anchor,
# Developer ID intermediate and leaf, Nightgauge team). A bare --verify is not
# enough: an ad-hoc re-signed tampered binary passes it. The string equals
# bundleCodeRequirement in internal/doctor/binary_resolve.go (parity test).
# Other platforms carry no platform signature, so selection is unchanged.
_ng_bundle_requirement='anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "RZJPN7Y7BG"'
_ng_bundle_verified() {
  [ "$(uname -s 2>/dev/null)" = "Darwin" ] || return 0
  codesign --verify --strict "-R=$_ng_bundle_requirement" "$1" >/dev/null 2>&1
}
# Verifies $1 (path) for version $2; records a refusal and returns 1.
_ng_bundle_accept() {
  _ng_bundle_verified "$1" && return 0
  _ng_rejected="${_ng_rejected:+$_ng_rejected, }$2"
  return 1
}
if [ -z "$NIGHTGAUGE_BINARY" ]; then
  _ng_recorded_path=""
  _ng_first_runnable=""
  _ng_first_runnable_version=""
  # Plain glob expansion, no `find`; the trailing /dist/bin path is the
  # canonical location set by the extension's build script. An unmatched glob
  # expands to the literal pattern, which the -f test rejects.
  for candidate in "$HOME"/.vscode/extensions/nightgauge.nightgauge-[0-9]*/dist/bin/nightgauge; do
    [ -f "$candidate" ] || continue
    _ng_bundle_dir="${candidate%/dist/bin/nightgauge}"
    _ng_bundle_base="${_ng_bundle_dir##*/}"
    _ng_bundle_version="${_ng_bundle_base#nightgauge.nightgauge-}"
    [ -n "$_ng_bundle_version" ] || continue
    _ng_bundle_count=$((_ng_bundle_count + 1))
  done

  # The record is only consulted when the glob actually matched a bundle:
  # with nothing to choose between there is nothing for the record to decide,
  # and a machine with no extension installed must pay nothing at all for
  # step 4. The recorded directory is a validated plain name, so it is tested
  # directly rather than re-globbed.
  if [ "$_ng_bundle_count" -gt 0 ]; then
    _ng_read_recorded_bundle_dir
    if [ -n "$_ng_recorded_dir" ]; then
      _ng_recorded_version="${_ng_recorded_dir#nightgauge.nightgauge-}"
    fi
    if [ -n "$_ng_recorded_version" ]; then
      _ng_recorded_candidate="$HOME/.vscode/extensions/$_ng_recorded_dir/dist/bin/nightgauge"
      if [ -f "$_ng_recorded_candidate" ] && [ -x "$_ng_recorded_candidate" ]; then
        _ng_recorded_path="$_ng_recorded_candidate"
      fi
      unset _ng_recorded_candidate
    fi
  fi

  # Lazy verification in selection order: the recorded bundle first, then the
  # first executable glob match that verifies, skipping the recorded one.
  if [ -n "$_ng_recorded_path" ] && \
    ! _ng_bundle_accept "$_ng_recorded_path" "$_ng_recorded_version"; then
    _ng_skip_path="$_ng_recorded_path"
    _ng_recorded_path=""
  fi
  if [ -z "$_ng_recorded_path" ] && [ "$_ng_bundle_count" -gt 0 ]; then
    for candidate in "$HOME"/.vscode/extensions/nightgauge.nightgauge-[0-9]*/dist/bin/nightgauge; do
      [ -f "$candidate" ] && [ -x "$candidate" ] || continue
      [ "$candidate" = "${_ng_skip_path:-}" ] && continue
      _ng_bundle_dir="${candidate%/dist/bin/nightgauge}"
      _ng_bundle_base="${_ng_bundle_dir##*/}"
      _ng_bundle_version="${_ng_bundle_base#nightgauge.nightgauge-}"
      [ -n "$_ng_bundle_version" ] || continue
      if _ng_bundle_accept "$candidate" "$_ng_bundle_version"; then
        _ng_first_runnable="$candidate"
        _ng_first_runnable_version="$_ng_bundle_version"
        break
      fi
    done
  fi
  unset _ng_skip_path

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

# Signature rejection (#2776): reported right after step 4 — before step 5 and
# the missing-binary exit, so it is never lost — whether or not a bundle was
# finally selected. Routed and deduped like the other [stale-binary] lines;
# never stdout. It supersedes the record/unrecorded divergence below.
if [ -n "$_ng_rejected" ]; then
  if [ -n "$NIGHTGAUGE_BINARY" ]; then
    _ng_rejected_outcome="bundle $_ng_selected_version, running $NIGHTGAUGE_BINARY"
  else
    _ng_rejected_outcome="no bundle"
  fi
  _ng_rejected_message="[stale-binary] VSCode extension bundle $_ng_rejected was not run: its code signature did not verify against the Nightgauge Developer ID requirement; step 4 resolved $_ng_rejected_outcome"
  if [ "$NIGHTGAUGE_HOOK_SILENT" = "false" ]; then
    echo "$_ng_rejected_message" >&2
  elif ! _ng_message_already_last "$_ng_rejected_message"; then
    _log_to_side_channel "$_ng_rejected_message"
  fi
  _ng_divergence=""
  unset _ng_rejected_outcome _ng_rejected_message
fi
unset _ng_rejected _ng_bundle_requirement

# 5. `go install` default location.
if [ -z "$NIGHTGAUGE_BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ]; then
  NIGHTGAUGE_BINARY="$HOME/go/bin/nightgauge"
fi

if [ -z "$NIGHTGAUGE_BINARY" ] || [ ! -x "$NIGHTGAUGE_BINARY" ]; then
  _ng_install_hint="Install it with 'go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest', put it on PATH, or open a workspace with the Nightgauge extension, which links ~/.nightgauge/bin/nightgauge."
  case "$NIGHTGAUGE_HOOK_ON_MISSING_BINARY" in
    announce)
      # The one visible warning per session. A fixed string, so no JSON
      # escaping is needed; stdout only (#3262).
      printf '{"systemMessage":"Nightgauge: the nightgauge binary was not found, so the Nightgauge hooks are NOT enforcing their gates in this session. %s"}\n' "$_ng_install_hint"
      _log_to_side_channel "[hook-skipped] nightgauge binary not found — announced to the session"
      exit 0
      ;;
    gate)
      if _ng_repo_opted_in; then
        printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Nightgauge: this repository opted in (.nightgauge/config.yaml), but the nightgauge binary was not found, so its safety gate cannot run. %s"}}\n' "$_ng_install_hint"
        _log_to_side_channel "[hook-denied] nightgauge binary not found — gate denied the tool call in an opted-in repository"
        exit 0
      fi
      ;;
  esac
  unset _ng_install_hint

  # Graceful skip: route the diagnostic to the side-channel log
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
