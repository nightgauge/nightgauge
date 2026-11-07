#!/usr/bin/env bash
#
# state-backstop.sh — prove a gate run left the developer's machine state alone
# (#2311).
#
# A local gate run moved a developer's legacy ~/.nightgauge data into a test's
# temporary state directory, and test cleanup deleted it: a test overrode STATE
# but left HOME real, and a CLI it spawned ran the one-time migration. The
# product guard and the test-hermeticity lint are the fixes. This is the
# backstop that makes the next leak of that kind loud instead of silent: the
# gate records the two roots before its steps and fails if the run changed
# them, naming what changed.
#
# It RECORDS ONLY. It never restores, moves or deletes anything: after a leak,
# the operator decides what to do with the evidence.
#
#   snapshot <dir>            record every root into <dir>
#   compare <before> <after>  exit 1 naming each difference that fails
#   verify <before> <after>   snapshot into <after>, then compare
#
# The roots are held to different standards, because a live editor daemon or
# a pipeline run legitimately writes to STATE while the gate runs:
#
#   - the legacy root ($HOME/.nightgauge), minus its state/ subdirectory:
#     every path, with the sha256 of each regular file and the target of each
#     link. Nothing in a gate run may add, remove or change anything there.
#   - the default STATE for this HOME (what the CLI resolves with no override:
#     ~/.nightgauge/state on macOS, ~/.local/state/nightgauge on Linux): paths
#     only, three levels deep, so a pipeline worktree's checkout is never
#     walked. A REMOVED path fails; an added one is printed as information.
#     One exception (#2701): a pipeline worktree, worktrees/<hash>/<name>,
#     that the pipeline's own cleanup removed during the run. Every removal
#     path (Go CleanupWorktree and the worktree sweep, the extension's
#     WorktreeManager.cleanup) first appends "<epoch-seconds>\t<name>" to
#     the append-only ledger worktrees/<hash>/.removed-worktrees. The removal
#     is excused, and printed as information, only when that ledger holds a
#     line naming exactly <name> with an epoch no older than the snapshot's
#     start. Anything else removed, ledger or not, still fails.
#   - the agent homes the extension installs skills into (#2935):
#     ~/.codex/skills, ~/.grok/skills, ~/.config/opencode and
#     ~/.claude/plugins. Only Nightgauge's part of each, held to the legacy
#     root's standard: the nightgauge skill directories with _shared/ and
#     the unprefixed skills, OpenCode's commands/nightgauge-*,
#     plugins/nightgauge.js and plugins/nightgauge/, and the nightgauge
#     entries of Claude's plugin registries. A host smoke run with a real
#     HOME re-synced the worktree's skills into them, and nothing watched.
#
# NIGHTGAUGE_STATE_HOME and XDG_STATE_HOME are ignored on purpose: the data
# at risk is the default location's, whatever the gate's environment
# overrides. An exported agent-home override (CODEX_HOME, GROK_HOME,
# XDG_CONFIG_HOME, CLAUDE_CONFIG_DIR) is watched in addition to the default.
# A root that is absent is recorded as absent; one that disappears fails.
#
# Run by scripts/ci-local.sh around its steps; tested by
# scripts/test-state-backstop.sh.
set -uo pipefail

legacy_root() { printf '%s/.nightgauge\n' "$HOME"; }

default_state_root() {
  case "$(uname -s)" in
    Linux) printf '%s/.local/state/nightgauge\n' "$HOME" ;;
    MINGW* | MSYS* | CYGWIN*)
      if [ -n "${LOCALAPPDATA:-}" ]; then
        printf '%s/nightgauge/state\n' "$LOCALAPPDATA"
      else
        printf '%s/AppData/Local/nightgauge/state\n' "$HOME"
      fi
      ;;
    *) printf '%s/.nightgauge/state\n' "$HOME" ;;
  esac
}

# The agent homes, default locations under HOME, one "<kind>\t<root>" per
# line. Only what Nightgauge installs there is watched (#2935): Claude Code
# and Codex rewrite their own caches, stores and .system/ skills whenever a
# session starts, and a pipeline slot starts one while the gate runs.
# An exported CODEX_HOME, GROK_HOME, XDG_CONFIG_HOME or CLAUDE_CONFIG_DIR is
# the operator's real install too, and ci-local.sh never sets them, so a
# leaking test that inherits the gate's environment writes there: watched
# as well when set and not the default.
agent_roots() {
  printf '%s\t%s\n' skills "$HOME/.codex/skills" skills "$HOME/.grok/skills" \
    opencode "$HOME/.config/opencode" claude "$HOME/.claude/plugins"
  local var sub kind def
  while read -r var sub kind def; do
    [ -n "${!var:-}" ] && [ "${!var}" != "$HOME/$def" ] &&
      printf '%s\t%s\n' "$kind" "${!var}/$sub"
  done <<'ROOTS'
CODEX_HOME skills skills .codex
GROK_HOME skills skills .grok
XDG_CONFIG_HOME opencode opencode .config
CLAUDE_CONFIG_DIR plugins claude .claude
ROOTS
  return 0
}

# The first path components under a skills root that are Nightgauge's: a
# nightgauge-* name, a directory carrying the install marker, or one of the
# unprefixed directories the installers write (bundledSkills.ts UNPREFIXED,
# with the _shared/ includes that carry no marker).
nightgauge_skill_dirs() { # nightgauge_skill_dirs <skills-root>
  local d
  for d in "$1"/*; do
    [ -d "$d" ] || continue
    case "${d##*/}" in
      nightgauge* | _shared | smart-setup | update-docs | pr-preflight) printf '%s\n' "${d##*/}" ;;
      *) [ -e "$d/.nightgauge-installed" ] && printf '%s\n' "${d##*/}" ;;
    esac
  done
  return 0
}

# Keep the lines of a list_strict listing whose path is under one of the
# given first components (newline-separated), or equal to it.
keep_under() { # keep_under <prefix> <names>
  # Names go through the environment: awk -v rejects a literal newline.
  KEEP_NAMES="$2" LC_ALL=C awk -v prefix="$1" '
    BEGIN { n = split(ENVIRON["KEEP_NAMES"], a, "\n"); for (i = 1; i <= n; i++) if (a[i] != "") keep[prefix a[i]] = 1 }
    {
      rel = substr($0, 1, index($0, "\t") - 1)
      if (substr(rel, 1, length(prefix)) != prefix) next
      head = substr(rel, length(prefix) + 1); sub(/\/.*/, "", head)
      if ((prefix head) in keep) print
    }'
}

# The Nightgauge entries of a Claude plugin registry file, one line each:
# "<file>#<key>\tentry:<the fields an install owns>". Timestamps are left
# out: Claude refreshes them on its own.
claude_entries() { # claude_entries <plugins-root> <file>
  local file="$1/$2" out
  [ -f "$file" ] || return 0
  if ! out="$(jq -r --arg f "$2" '
      (if has("plugins") then .plugins else . end)
      | to_entries[] | select(.key | test("nightgauge"))
      | "\($f)#\(.key)\tentry:" + ([.value] | flatten
          | map({scope, installPath, version, gitCommitSha, projectPath, source, installLocation}
                | with_entries(select(.value != null))) | tojson)' "$file" 2>/dev/null)"; then
    printf '%s\tunparseable:%s\n' "$2" "$({ sha256sum 2>/dev/null || shasum -a 256; } <"$file" | cut -c1-64)"
    return 0
  fi
  [ -n "$out" ] && printf '%s\n' "$out"
  return 0
}

# The watched part of one agent root. A symlinked root's link target is
# recorded, so re-pointing it fails as well. A root holding nothing of
# Nightgauge's reads as ABSENT: an agent creating its home for its own files
# mid-run is not a leak.
list_agent() { # list_agent <kind> <root>
  local kind="$1" root="$2" body
  if [ ! -d "$root" ]; then
    echo "ABSENT"
    return 0
  fi
  body="$(list_agent_body "$kind" "$root")"
  if [ -L "$root" ]; then
    printf '.\troot:%s\n' "$(readlink "$root")"
  elif [ -z "$body" ]; then
    echo "ABSENT"
    return 0
  fi
  [ -n "$body" ] && printf '%s\n' "$body"
  return 0
}

list_agent_body() { # list_agent_body <kind> <root>
  local kind="$1" root="$2"
  case "$kind" in
    skills) list_strict "$root" | keep_under "" "$(nightgauge_skill_dirs "$root")" ;;
    opencode)
      {
        list_strict "$root" | keep_under "skills/" "$(nightgauge_skill_dirs "$root/skills")"
        list_strict "$root" | LC_ALL=C awk -F '\t' \
          '$1 ~ /^commands\/nightgauge-/ || $1 == "plugins/nightgauge.js" || $1 ~ /^plugins\/nightgauge(\/|$)/'
      } | LC_ALL=C sort
      ;;
    claude)
      {
        claude_entries "$root" installed_plugins.json
        claude_entries "$root" known_marketplaces.json
      } | LC_ALL=C sort
      ;;
  esac
}

# One line per entry under a strictly held root, <prune> (a direct child
# name) excluded when given:
# "<rel>\t<kind>" where kind is dir, link:<target>, file:<sha256> or other.
list_strict() { # list_strict <root> [prune]
  local root="$1" prune="${2:-}" p rel hashes
  if [ ! -d "$root" ]; then
    echo "ABSENT"
    return 0
  fi
  local -a skip=()
  [ -n "$prune" ] && skip=(-path "$root/$prune" -prune -o)
  # Hash every regular file in one batch, not a process per file: the agent
  # homes hold thousands of files (#2935). "<sha256>  <path>" per line.
  hashes="$(mktemp "${TMPDIR:-/tmp}/state-backstop.XXXXXX")" || return 2
  find -H "$root" -mindepth 1 ${skip[@]+"${skip[@]}"} -type f -print0 2>/dev/null |
    xargs -0 sh -c 'if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi' sh \
      >"$hashes" 2>/dev/null
  find -H "$root" -mindepth 1 ${skip[@]+"${skip[@]}"} -print 2>/dev/null |
    LC_ALL=C sort |
    while IFS= read -r p; do
      rel="${p#"$root"/}"
      if [ -L "$p" ]; then
        printf '%s\tlink:%s\n' "$rel" "$(readlink "$p")"
      elif [ -d "$p" ]; then
        printf '%s\tdir\n' "$rel"
      elif [ -f "$p" ]; then
        printf '%s\tfile\n' "$rel"
      else
        printf '%s\tother\n' "$rel"
      fi
    done |
    LC_ALL=C awk -v root="$root/" -v hashes="$hashes" '
      BEGIN {
        while ((getline line < hashes) > 0) {
          h = substr(line, 1, 64); p = substr(line, 67)
          if (index(p, root) == 1) sum[substr(p, length(root) + 1)] = h
        }
      }
      {
        n = split($0, f, "\t")
        if (f[n] == "file") {
          rel = substr($0, 1, length($0) - 5)
          print rel "\tfile:" ((rel in sum) ? sum[rel] : "unreadable")
        } else print
      }'
  rm -f "$hashes"
}

# One relative path per line under STATE, three levels deep.
list_state() { # list_state <root>
  local root="$1" p
  if [ ! -d "$root" ]; then
    echo "ABSENT"
    return 0
  fi
  find "$root" -mindepth 1 -maxdepth 3 -print 2>/dev/null |
    LC_ALL=C sort |
    while IFS= read -r p; do
      printf '%s\n' "${p#"$root"/}"
    done
}

snapshot() { # snapshot <dir>
  local dir="$1" legacy state root n=0
  legacy="$(legacy_root)"
  state="$(default_state_root)"
  mkdir -p "$dir" || return 2
  date +%s >"$dir/started"
  printf '%s\n' "$legacy" >"$dir/legacy.root"
  printf '%s\n' "$state" >"$dir/state.root"
  list_strict "$legacy" state >"$dir/legacy.list"
  list_state "$state" >"$dir/state.list"
  local kind
  while IFS=$'\t' read -r kind root; do
    n=$((n + 1))
    printf '%s\n' "$root" >"$dir/agent-$n.root"
    list_agent "$kind" "$root" >"$dir/agent-$n.list"
  done < <(agent_roots)
}

# compare_strict <root> <label> <before.list> <after.list>: any difference
# fails, naming each path. Returns 1 on a difference.
compare_strict() {
  local root="$1" label="$2" bl="$3" al="$4" removed added was now rel
  # comm needs sorted input; both lists are sorted by path with LC_ALL=C, and
  # a changed file differs on the same path.
  removed="$(LC_ALL=C comm -23 "$bl" "$al")"
  added="$(LC_ALL=C comm -13 "$bl" "$al")"
  [ -z "$removed$added" ] && return 0
  echo "✗ the gate run changed $root$label:"
  if [ "$(head -1 "$al")" = "ABSENT" ]; then
    echo "    the directory itself is gone"
  fi
  # A path on both sides changed, one only before was removed, and one only
  # after was added: comm over the two path lists. Not a grep per path, as
  # this was: that piped the whole added list into grep -q under pipefail,
  # where grep exiting at its match SIGPIPEs the writer, so a changed file
  # read as removed every time the list was large (#2360). It also spawned
  # a process for every line of a mass leak.
  was="$(cut -f1 <<<"$removed" | grep -vx -e ABSENT -e '' | LC_ALL=C sort -u)"
  now="$(cut -f1 <<<"$added" | grep -vx -e ABSENT -e '' | LC_ALL=C sort -u)"
  while IFS= read -r rel; do
    [ -n "$rel" ] && echo "    changed: $root/$rel"
  done < <(LC_ALL=C comm -12 <(printf '%s\n' "$was") <(printf '%s\n' "$now"))
  while IFS= read -r rel; do
    [ -n "$rel" ] && echo "    removed: $root/$rel"
  done < <(LC_ALL=C comm -23 <(printf '%s\n' "$was") <(printf '%s\n' "$now"))
  while IFS= read -r rel; do
    [ -n "$rel" ] && echo "    added:   $root/$rel"
  done < <(LC_ALL=C comm -13 <(printf '%s\n' "$was") <(printf '%s\n' "$now"))
  return 1
}

# pipeline_removed <state> <started> <removed-paths>: print the removed paths
# the pipeline's cleanup recorded in its removal ledger at or after <started>
# (see the header). An empty <started> excuses nothing.
pipeline_removed() {
  local state="$1" started="$2" rel hash name ledger
  [ -n "$started" ] || return 0
  while IFS= read -r rel; do
    case "$rel" in worktrees/*/*) ;; *) continue ;; esac
    hash="${rel#worktrees/}"
    name="${hash#*/}"
    hash="${hash%%/*}"
    case "$name" in */* | .removed-worktrees | "") continue ;; esac
    ledger="$state/worktrees/$hash/.removed-worktrees"
    [ -f "$ledger" ] || continue
    LC_ALL=C awk -F '\t' -v n="$name" -v s="$started" \
      '$2 == n && $1 ~ /^[0-9]+$/ && $1 + 0 >= s + 0 { found = 1 } END { exit !found }' "$ledger" &&
      printf '%s\n' "$rel"
  done <<<"$3"
  return 0
}

compare() { # compare <before> <after>
  local before="$1" after="$2" legacy state fail=0 removed added f root roots=""
  legacy="$(cat "$before/legacy.root")"
  state="$(cat "$before/state.root")"

  # Legacy root and agent homes: any difference fails.
  compare_strict "$legacy" " (outside state/)" "$before/legacy.list" "$after/legacy.list" || fail=1
  for f in "$before"/agent-*.root; do
    [ -f "$f" ] || continue
    root="$(cat "$f")"
    roots="$roots, $root"
    compare_strict "$root" "" "${f%.root}.list" "$after/$(basename "${f%.root}").list" || fail=1
  done

  # Default STATE: a removed path fails; an added one is information.
  removed="$(LC_ALL=C comm -23 "$before/state.list" "$after/state.list" | grep -vx ABSENT || true)"
  local started="" excused=""
  [ -f "$before/started" ] && started="$(cat "$before/started")"
  if [ -n "$removed" ]; then
    excused="$(pipeline_removed "$state" "$started" "$removed")"
    [ -n "$excused" ] &&
      removed="$(LC_ALL=C comm -23 <(printf '%s\n' "$removed") <(printf '%s\n' "$excused") | grep -v '^$' || true)"
  fi
  added="$(LC_ALL=C comm -13 "$before/state.list" "$after/state.list" | grep -vx ABSENT || true)"
  if [ "$(head -1 "$before/state.list")" != "ABSENT" ] && [ "$(head -1 "$after/state.list")" = "ABSENT" ]; then
    fail=1
    echo "✗ the gate run removed the machine-state directory $state"
  elif [ -n "$removed" ]; then
    fail=1
    echo "✗ the gate run removed paths from the machine-state directory $state:"
    printf '%s\n' "$removed" | sed "s|^|    removed: $state/|"
  fi
  if [ -n "$excused" ]; then
    echo "  (information) pipeline worktrees removed by the pipeline's cleanup during the run:"
    printf '%s\n' "$excused" | sed "s|^|    removed: $state/|"
  fi
  if [ -n "$added" ]; then
    echo "  (information) paths added to $state during the run:"
    head -n 20 <<<"$added" | sed "s|^|    added:   $state/|"
  fi

  if [ "$fail" -ne 0 ]; then
    echo ""
    echo "Nothing was restored or deleted. A test or a process it spawned reached"
    echo "the real machine state (#2311, #2935): find it before running the gate"
    echo "again. If a process outside the gate (a live editor daemon, a pipeline"
    echo "run, a Claude Code session refreshing its plugins) made the change, check"
    echo "that before re-running."
    return 1
  fi
  echo "✓ $legacy, $state$roots are as the run found them"
  return 0
}

case "${1:-}" in
  snapshot) snapshot "${2:?snapshot <dir>}" ;;
  compare) compare "${2:?compare <before> <after>}" "${3:?compare <before> <after>}" ;;
  verify)
    snapshot "${3:?verify <before> <after>}" || exit 2
    compare "${2:?verify <before> <after>}" "$3"
    ;;
  *)
    echo "usage: $0 snapshot <dir> | compare <before> <after> | verify <before> <after>" >&2
    exit 2
    ;;
esac
