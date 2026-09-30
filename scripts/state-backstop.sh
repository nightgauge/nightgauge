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
#   snapshot <dir>            record both roots into <dir>
#   compare <before> <after>  exit 1 naming each difference that fails
#   verify <before> <after>   snapshot into <after>, then compare
#
# The two roots are held to different standards, because a live editor daemon
# or a pipeline run legitimately writes to STATE while the gate runs:
#
#   - the legacy root ($HOME/.nightgauge), minus its state/ subdirectory:
#     every path, with the sha256 of each regular file and the target of each
#     link. Nothing in a gate run may add, remove or change anything there.
#   - the default STATE for this HOME (what the CLI resolves with no override:
#     ~/.nightgauge/state on macOS, ~/.local/state/nightgauge on Linux): paths
#     only, three levels deep, so a pipeline worktree's checkout is never
#     walked. A REMOVED path fails; an added one is printed as information.
#
# NIGHTGAUGE_STATE_HOME and XDG_STATE_HOME are ignored on purpose: the data at
# risk is the default location's, whatever the gate's environment overrides.
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

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# One line per entry under the legacy root, state/ excluded:
# "<rel>\t<kind>" where kind is dir, link:<target>, file:<sha256> or other.
list_legacy() { # list_legacy <root>
  local root="$1" p rel
  if [ ! -d "$root" ]; then
    echo "ABSENT"
    return 0
  fi
  find "$root" -mindepth 1 -path "$root/state" -prune -o -print 2>/dev/null |
    LC_ALL=C sort |
    while IFS= read -r p; do
      rel="${p#"$root"/}"
      if [ -L "$p" ]; then
        printf '%s\tlink:%s\n' "$rel" "$(readlink "$p")"
      elif [ -d "$p" ]; then
        printf '%s\tdir\n' "$rel"
      elif [ -f "$p" ]; then
        printf '%s\tfile:%s\n' "$rel" "$(sha256_of "$p" 2>/dev/null || echo unreadable)"
      else
        printf '%s\tother\n' "$rel"
      fi
    done
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
  local dir="$1" legacy state
  legacy="$(legacy_root)"
  state="$(default_state_root)"
  mkdir -p "$dir" || return 2
  printf '%s\n' "$legacy" >"$dir/legacy.root"
  printf '%s\n' "$state" >"$dir/state.root"
  list_legacy "$legacy" >"$dir/legacy.list"
  list_state "$state" >"$dir/state.list"
}

compare() { # compare <before> <after>
  local before="$1" after="$2" legacy state fail=0 line rel
  legacy="$(cat "$before/legacy.root")"
  state="$(cat "$before/state.root")"

  local removed added changed
  # Legacy root: any difference fails. comm needs sorted input; both lists are
  # sorted by path with LC_ALL=C, and a changed file differs on the same path.
  removed="$(LC_ALL=C comm -23 "$before/legacy.list" "$after/legacy.list")"
  added="$(LC_ALL=C comm -13 "$before/legacy.list" "$after/legacy.list")"
  if [ -n "$removed$added" ]; then
    fail=1
    echo "✗ the gate run changed $legacy (outside state/):"
    if [ "$(head -1 "$after/legacy.list")" = "ABSENT" ]; then
      echo "    the directory itself is gone"
    fi
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      rel="${line%%	*}"
      if printf '%s\n' "$added" | cut -f1 | grep -qxF -- "$rel"; then
        echo "    changed: $legacy/$rel"
      elif [ "$rel" != "ABSENT" ]; then
        echo "    removed: $legacy/$rel"
      fi
    done <<<"$removed"
    changed="$(printf '%s\n' "$removed" | cut -f1)"
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      rel="${line%%	*}"
      [ "$rel" = "ABSENT" ] && continue
      printf '%s\n' "$changed" | grep -qxF -- "$rel" && continue
      echo "    added:   $legacy/$rel"
    done <<<"$added"
  fi

  # Default STATE: a removed path fails; an added one is information.
  removed="$(LC_ALL=C comm -23 "$before/state.list" "$after/state.list" | grep -vx ABSENT || true)"
  added="$(LC_ALL=C comm -13 "$before/state.list" "$after/state.list" | grep -vx ABSENT || true)"
  if [ "$(head -1 "$before/state.list")" != "ABSENT" ] && [ "$(head -1 "$after/state.list")" = "ABSENT" ]; then
    fail=1
    echo "✗ the gate run removed the machine-state directory $state"
  elif [ -n "$removed" ]; then
    fail=1
    echo "✗ the gate run removed paths from the machine-state directory $state:"
    printf '%s\n' "$removed" | sed "s|^|    removed: $state/|"
  fi
  if [ -n "$added" ]; then
    echo "  (information) paths added to $state during the run:"
    printf '%s\n' "$added" | head -20 | sed "s|^|    added:   $state/|"
  fi

  if [ "$fail" -ne 0 ]; then
    echo ""
    echo "Nothing was restored or deleted. A test or a process it spawned reached"
    echo "the real machine state (#2311): find it before running the gate again. If"
    echo "a process outside the gate (a live editor daemon, a pipeline run) made the"
    echo "change, check that before re-running."
    return 1
  fi
  echo "✓ $legacy and $state are as the run found them"
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
