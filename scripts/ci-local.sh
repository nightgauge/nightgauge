#!/usr/bin/env bash
# CI-parity validation runner — mirrors the steps in `.github/workflows/ci.yml`
# so developers and pipeline skills can verify a change locally before pushing.
#
# Prints a summary of each check and exits non-zero on the first failure so the
# caller (shell, skill, CI hook) can fail loudly. The motivating incident: a
# format-drift PR slipped past feature-dev because its validation swallowed
# non-zero exits.
#
# ── Concurrency: CONCURRENT GATES ARE SUPPORTED, ON A SHARED BUDGET (#1983) ──
#
# Running several gates at once, one per worktree, is a capability this
# workspace is designed around: ADR-013 writes branches in parallel and each one
# gates itself, and #855 made the expensive publication-boundary family
# concurrency-safe on purpose. Serialising the gate would remove that, so it is
# NOT what this script does. THE ANSWER IS NOT A LOCK.
#
# WHAT REGRESSED, because the property held from #855 (2026-08-24) until it
# quietly stopped: #1217/#1219 (2026-08-30) took this gate from `189% cpu,
# 14m38s` — serial, three of which coexist on a 12-core box without noticing
# each other — to `413% cpu, 7m10s`, by running eleven read-only steps as a
# bounded concurrent group. The bound, `CI_LOCAL_JOBS=4`, is PER PROCESS. Three
# gates therefore ask for 12 heavy slots, including three `go test ./...`, three
# `go test -race ./...` and three full vitest runs. Measured result: load 58 on
# 12 cores, and children killed before they could record an exit code. Nothing
# about the tree raced; the MACHINE ran out. #1219's own audit was per-gate and
# never considered a second gate, and #1697 (a shared sandbox root) is the same
# omission one layer down.
#
# WHAT THIS DOES — the job budget is machine-wide instead of per-process. Slots
# live in one directory keyed on the repository's shared git dir, so N gates
# share CI_LOCAL_JOBS heavy steps in total and each of them still overlaps its
# own serial spine. Gates stay parallel; the box stops being oversubscribed.
# `CI_LOCAL_JOBS` sets the machine-wide number. A slot is reclaimed only when
# its owner pid is dead, never on age (#1697's cleanup deleted a LIVE sandbox on
# an age rule).
#
# WHAT WAS REJECTED — a single-instance lock. It would contradict #855 and the
# program office's standing guidance ("do not serialize on the gate"), and it
# would answer a resource-accounting bug by removing a capability. Also rejected:
# hermetic per-run sandboxes as the general fix, since they address tree sharing,
# which is not what failed here, and they leave the oversubscription untouched.
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

LIST_STEPS=0
# `--slot-probe [seconds]` takes one heavy-step slot from the machine-wide
# budget, reports how many were in use when it got one, holds it for `seconds`
# (default 0) and hands it back. It exists so the concurrency contract is
# testable in seconds instead of by running two 7-minute gates, and it doubles
# as the operator's answer to "what else is running right now?".
SLOT_PROBE=0
SLOT_PROBE_HOLD=0
RELEASE_PROBE=0
# `--group-probe <count> <seconds>` runs `count` trivial steps through the REAL
# `run_group`/`run_group_wait` pair and reports the greatest number of slots ever
# held at once, plus the failure accounting. It is the regression test for the
# two properties that broke: the budget is machine-wide (revert the throttle to
# `jobs -rp` and the observed maximum exceeds CI_LOCAL_JOBS), and a grouped child
# that dies without recording an exit code is reported as an INFRASTRUCTURE
# error rather than as a check that asserted false.
GROUP_PROBE=0
GROUP_PROBE_HOLD=2
# `--changed` (#1985) is the OPT-IN change-scoped fast path. Default OFF, and it
# must stay that way: the repository rule is "run the complete local gate once
# before every push", and a silently-scoped default would weaken that rule
# everywhere while looking identical in the output. With no flag this script
# behaves exactly as it did before #1985 — every step, unconditionally.
CHANGED_SCOPE=0
# `--scope-probe` prints the derived change scope and the Go decision the gate
# WOULD make with the flags it was given, and runs no steps. Same purpose as
# `--slot-probe`: make the contract assertable in milliseconds instead of by
# running a 10-minute gate twice. It deliberately does NOT imply `--changed`, so
# `--scope-probe` alone is the direct assertion that the DEFAULT scopes nothing.
SCOPE_PROBE=0
# `--summary-probe` drives the real skip/verdict reporting with one passing step
# and one skipped step, and exits. It is the regression test for the property
# that a skipped step is a THIRD state: revert the verdict to the unconditional
# "✓ All CI-parity checks passed." and this probe's output says so.
SUMMARY_PROBE=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --list-steps) LIST_STEPS=1; shift ;;
    --changed) CHANGED_SCOPE=1; shift ;;
    --scope-probe) SCOPE_PROBE=1; shift ;;
    --summary-probe) SUMMARY_PROBE=1; shift ;;
    --slot-probe)
      SLOT_PROBE=1
      shift
      case "${1:-}" in [0-9]*) SLOT_PROBE_HOLD="$1"; shift ;; esac
      ;;
    --release-probe) RELEASE_PROBE=1; shift ;;
    --group-probe)
      shift
      case "${1:-}" in [0-9]*) GROUP_PROBE="$1"; shift ;; *) GROUP_PROBE=4 ;; esac
      case "${1:-}" in [0-9]*) GROUP_PROBE_HOLD="$1"; shift ;; esac
      ;;
    *) shift ;;
  esac
done

# ── The change scope, and the Go decision (#1985) ────────────────────────────
#
# Only consulted under `--changed`. Without the flag every function here is
# defined and never called, and the gate runs exactly what it ran before.
#
# WHY. PR CI re-runs the complete suite on every pull request regardless, so the
# local gate's full run buys the answer earlier, not extra safety. For a diff
# that provably cannot reach Go it buys nothing at all, while holding heavy slots
# other gates on this machine are waiting for (see the budget below). A local
# false negative therefore costs a CI round trip, never a bad merge — which is
# what makes this a fast path rather than a loosening of the standard.
#
# WHAT IS NEVER SKIPPED, whatever the scope: the generated-file drift checks, the
# changelog contract, the publication boundary, and the credential scan. They are
# cheap and their failure modes are not confined to the language that changed.
#
# FAIL CLOSED. Every branch that cannot answer "did a Go input change?" — no
# `origin/main`, a git that will not run, an empty derivation — answers "run
# them". The only way to skip is a positively-derived empty Go intersection.

# The changed path set: the branch against origin/main PLUS the working tree.
# The working tree half is not optional — this gate is run BECAUSE you have
# uncommitted work, and scoping on the committed diff alone would skip the Go
# suites for an unstaged `.go` edit.
changed_paths() {
  {
    git diff --name-only origin/main...HEAD 2>/dev/null || true
    git diff --name-only HEAD 2>/dev/null || true
    git diff --name-only --cached 2>/dev/null || true
    git ls-files --others --exclude-standard 2>/dev/null || true
  } | sed '/^$/d' | sort -u
}

# Directories holding a tracked `.go` file. A non-Go file that sits in a Go
# package directory — `internal/terminalkind/table.json`, a `testdata/` golden —
# is a Go input even though its extension says otherwise.
go_package_dirs() {
  git ls-files -- '*.go' 2>/dev/null |
    awk '{ if (index($0, "/")) { sub(/\/[^\/]*$/, ""); print } else { print "." } }' |
    sort -u
}

# `//go:embed` targets, resolved against the directory of the file that declares
# them. This is how `internal/adaptercompat/manifests/*.json` and
# `internal/setup/templates/*` are found: they are compiled INTO the binary, so
# editing one changes Go behaviour while touching no `.go` file.
go_embed_globs() {
  git grep -I -n -- '//go:embed' -- '*.go' 2>/dev/null |
    while IFS= read -r hit; do
      local file rest dir pat
      file="${hit%%:*}"
      rest="${hit#*//go:embed }"
      dir="$(dirname "$file")"
      for pat in $rest; do
        case "$pat" in ''|'//'*) continue ;; esac
        printf '%s/%s\n%s/%s/*\n' "$dir" "$pat" "$dir" "$pat"
      done
    done | sort -u
}

# Codegen inputs AND outputs, derived rather than hardcoded.
#
# Two sources, both read from the tree so a new generator is picked up without
# editing this script:
#   1. Makefile recipes that `go run ./cmd/...` — every path-shaped token on the
#      recipe's lines that exists in the tree.
#   2. Path-shaped string literals in the generator sources themselves
#      (`cmd/*gen*/`, `*/codegen/`), which is where the flag DEFAULTS live —
#      `cmd/ipc-codegen/main.go` names
#      `packages/nightgauge-vscode/src/services/IpcClient.generated.ts`, and
#      `internal/terminalkind/codegen/codegen.go` names the SDK module and the
#      behaviour golden it renders.
#
# Outputs are deliberately included alongside inputs. A diff that touches only
# `IpcClient.generated.ts` is either a hand-edit (which the always-run drift
# check catches) or the visible half of a Go change; in both cases "only TS
# changed" is the wrong conclusion, and running the Go suites is the cheap side
# of that bet.
go_codegen_paths() {
  {
    awk '
      /^\t.*go run \.\// { c = 1 }
      c { print; if ($0 !~ /\\$/) c = 0 }
    ' Makefile 2>/dev/null | tr ' \t' '\n\n' | sed 's/\\$//'
    git grep -I -h -oE '"[a-zA-Z0-9_][a-zA-Z0-9_./-]*\.[a-zA-Z0-9]+"' -- \
      'cmd/*gen*/*.go' '*/codegen/*.go' 2>/dev/null | tr -d '"'
  } | sed '/^$/d' | sort -u | while IFS= read -r p; do
    [ -f "$p" ] && printf '%s\n' "$p"
  done
}

GO_SCOPE_DECIDED=0
GO_SCOPE_RUN=1
GO_SCOPE_REASON=""
CHANGED_COUNT=0

# Decide once. Sets GO_SCOPE_RUN (1 run, 0 skip) and GO_SCOPE_REASON.
decide_go_scope() {
  [ "$GO_SCOPE_DECIDED" -eq 1 ] && return 0
  GO_SCOPE_DECIDED=1
  GO_SCOPE_RUN=1

  if [ "$CHANGED_SCOPE" -eq 0 ]; then
    GO_SCOPE_REASON="--changed was not given; the gate is running in full"
    return 0
  fi
  if ! git rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
    GO_SCOPE_REASON="origin/main is not available, so the changed set cannot be derived — running everything"
    return 0
  fi

  local changed pkg_dirs embed_globs codegen_paths p d hit=""
  changed="$(changed_paths)"
  if [ -z "$changed" ]; then
    GO_SCOPE_REASON="the changed set came back EMPTY, which is indistinguishable from a broken derivation — running everything"
    return 0
  fi
  CHANGED_COUNT="$(printf '%s\n' "$changed" | wc -l | tr -d ' ')"

  pkg_dirs="$(go_package_dirs)"
  embed_globs="$(go_embed_globs)"
  codegen_paths="$(go_codegen_paths)"
  if [ -z "$pkg_dirs" ]; then
    GO_SCOPE_REASON="no Go package directories were derived, which cannot be right — running everything"
    return 0
  fi

  while IFS= read -r p; do
    [ -n "$p" ] || continue
    case "$p" in
      *.go|go.mod|go.sum|*/go.mod|*/go.sum) hit="$p (a Go source or module file)"; break ;;
    esac
    d="$(dirname "$p")"
    if printf '%s\n' "$pkg_dirs" | grep -qxF -- "$d"; then
      hit="$p (inside the Go package directory $d)"; break
    fi
    if printf '%s\n' "$codegen_paths" | grep -qxF -- "$p"; then
      hit="$p (a Go codegen input or output)"; break
    fi
    local g
    while IFS= read -r g; do
      [ -n "$g" ] || continue
      # $g is a glob on purpose: `//go:embed manifests/*.json` must match the
      # files it covers, so the pattern is deliberately unquoted here.
      # shellcheck disable=SC2254
      case "$p" in $g) hit="$p (embedded into a Go binary by //go:embed $g)"; break ;; esac
    done <<EOF
$embed_globs
EOF
    [ -n "$hit" ] && break
  done <<EOF
$changed
EOF

  if [ -n "$hit" ]; then
    GO_SCOPE_REASON="a Go input changed: $hit"
    GO_SCOPE_RUN=1
  else
    GO_SCOPE_REASON="none of the $CHANGED_COUNT changed path(s) is a Go source, module file, //go:embed target, file in a Go package directory, or Go codegen input/output"
    GO_SCOPE_RUN=0
  fi
  return 0
}

if [ "$SCOPE_PROBE" -eq 1 ]; then
  decide_go_scope
  scope_probe_paths="$(changed_paths)"
  if [ -n "$scope_probe_paths" ]; then
    echo "changed paths ($(printf '%s\n' "$scope_probe_paths" | wc -l | tr -d ' ')):"
    printf '%s\n' "$scope_probe_paths" | sed 's/^/  /'
  else
    echo "changed paths (0):"
  fi
  if [ "$GO_SCOPE_RUN" -eq 1 ]; then
    echo "go suites: RUN"
  else
    echo "go suites: SKIP"
  fi
  echo "reason: $GO_SCOPE_REASON"
  exit 0
fi

# --- Preflight: every path this gate consumes must EXIST (#983) --------------
#
# Every step below used to be wrapped in `if [ -f <the script it runs> ]; then`
# with no `else` and no warning. If the file was deleted, renamed or moved, the
# step vanished from the gate and `ci-local.sh` still exited 0 — a gate that
# silently stops gating, in the one place the reduction is invisible: its own
# green exit. An existence check is not a consumption check (#975); every path
# here is a TRACKED file in this repository, and an absent tracked file is an
# error, not a configuration to degrade gracefully around.
#
# So the guards are gone and the requirement is asserted ONCE, up front, by
# name. A missing path fails in under a second instead of after the full gate,
# and says which path is missing.
#
# Keep these lists in step with the `run_step` calls below. `--list-steps` plus
# scripts/ci-local-steps.txt (asserted by scripts/test-ci-local-inventory.sh)
# is what makes a drift between the two impossible to land silently.
REQUIRED_FILES=(
  go.mod
  scripts/test-ci-local-inventory.sh
  scripts/ci-local-steps.txt
  scripts/test-branch-merged-check.sh
  scripts/test-post-merge-check.sh
  scripts/test-publish-vsix-set.sh
  scripts/test-sign-macos-binaries.sh
  scripts/test-scrub-evidence.sh
  docker/clean-install/scrub-evidence.sh
  scripts/test-capture-cli-help.sh
  scripts/capture-cli-help.sh
  scripts/test-adapter-canary.sh
  scripts/adapter-canary.sh
  scripts/test-ci-change-class.sh
  scripts/npm-audit-check.js
  scripts/validate-skill-metadata.sh
  scripts/test-validate-skill-metadata.sh
  scripts/publication-boundary-check.py
  scripts/test-publication-boundary.sh
  scripts/test-publication-boundary-rename.py
  scripts/test-publication-boundary-ceiling.py
  scripts/test-publication-boundary-erosion.py
  scripts/test-publication-boundary-attribution.sh
  scripts/test-publication-boundary-hermeticity.sh
  scripts/check-band-vocabulary.py
  scripts/test-band-vocabulary-check.sh
  scripts/check-visibility-prose.py
  scripts/test-workflow-refs-check.sh
  scripts/test-marketplace-channel.sh
  scripts/test-verify-release-channels.sh
  scripts/verify-release-channels.sh
  scripts/check-workflow-refs.py
  scripts/validate-proposal-artifact.mjs
  scripts/apply-proposal-artifact.sh
  scripts/test-validate-proposal-artifact.sh
  .github/scripts/cla-check.test.mjs
  scripts/test-measure-cache-boundary-loss.sh
  scripts/validate-phase-markers.ts
  packages/nightgauge-vscode/scripts/generate-package-contributions.ts
  packages/nightgauge-vscode/scripts/check-engine-types.mjs
  scripts/check-md-links.sh
  scripts/test-check-md-links.sh
  scripts/check-agent-guidance.sh
  skills/smart-setup/scripts/check-agent-guidance.sh
  scripts/test-agent-guidance-check.sh
  scripts/check-changelog.sh
  scripts/test-check-changelog.sh
  scripts/test-mirror-drift-gate.sh
  scripts/test-install-agent-skills-targets.sh
  scripts/test-issue-body-contract.sh
  scripts/check-issue-body-contract.py
  scripts/test-skill-echo-json.sh
  scripts/check-skill-echo-json.py
  scripts/install-agent-skills.sh
  scripts/test-mirror-link-check.sh
  scripts/check-mirror-links.py
  scripts/check-go-test-skips.py
  scripts/go-test-skip-allowlist.txt
  scripts/go-test-json-echo.py
  scripts/lib/ci_local_failures.sh
  scripts/test-ci-local-concurrency.sh
  scripts/test-ci-local-changed-scope.sh
)

# Makefile targets the gate invokes. `[ -f Makefile ] && grep -q '^t:' Makefile`
# was the compound form of the same defect: it skipped silently on a missing
# Makefile OR a renamed target.
REQUIRED_MAKE_TARGETS=(
  generate-ipc-client
  check-terminal-kind-table
  check-platform-operations
)

# package.json scripts the gate invokes. Same shape again: `grep -q '"lint"'
# package.json` skipped the step when the script was renamed.
REQUIRED_NPM_SCRIPTS=(
  lint
  format:check
  build
  test
)

MISSING=()
for path in "${REQUIRED_FILES[@]}"; do
  [ -e "$path" ] || MISSING+=("missing required file: $path")
done
if [ ! -f Makefile ]; then
  MISSING+=("missing required file: Makefile")
else
  for target in "${REQUIRED_MAKE_TARGETS[@]}"; do
    grep -q "^${target}:" Makefile || MISSING+=("missing required Makefile target: ${target}")
  done
fi
if [ ! -f package.json ]; then
  MISSING+=("missing required file: package.json")
else
  for script in "${REQUIRED_NPM_SCRIPTS[@]}"; do
    grep -q "\"${script}\":" package.json || MISSING+=("missing required package.json script: ${script}")
  done
fi
if [ "${#MISSING[@]}" -gt 0 ]; then
  {
    echo "✗ ci-local.sh preflight failed — this gate cannot run as written."
    printf '  - %s\n' "${MISSING[@]}"
    echo ""
    echo "Every path above is a tracked file, Makefile target or package.json"
    echo "script that a step below invokes. A missing one used to make its step"
    echo "disappear from the gate silently (#983); it is now a hard failure."
    echo "Restore the path, or delete its step AND its entry in"
    echo "scripts/ci-local-steps.txt together."
  } >&2
  exit 1
fi

# ── The machine-wide heavy-step budget ───────────────────────────────────────
#
# One slot directory per concurrently-running grouped step, shared by every gate
# on this machine for this repository. Keyed on `--git-common-dir`, so every
# worktree of this repository draws on one budget — they are the gates that
# actually collide — while an unrelated checkout elsewhere keeps its own.
#
# `mkdir` is the primitive on purpose: atomic on every filesystem this repo is
# cloned onto, and `flock(1)` does not exist on macOS.
MAIN_PID=$$
# The MACHINE-WIDE number of concurrent grouped steps. Before #1983 this bounded
# each process separately, so three gates ran 12 heavy jobs on 12 cores and the
# kernel started killing children. There is nothing to gain past a handful of
# slots even for a single gate: #1219 measured the critical path at one 144s
# step.
CI_LOCAL_JOBS="${CI_LOCAL_JOBS:-4}"
# Seconds to wait for a slot before proceeding anyway with a warning. The budget
# is advisory — the gate's CORRECTNESS does not depend on it, only the box's
# health does — so exceeding it beats hanging.
CI_LOCAL_SLOT_WAIT="${CI_LOCAL_SLOT_WAIT:-1800}"
SLOT_ROOT=""
GROUP_SLOTS=()
# Identifies THIS gate for the life of the process, so a release can tell "my
# slot" from "a slot that has since been handed to someone else". Slot paths are
# numbered 1..CI_LOCAL_JOBS and therefore REUSED: a grouped child removes its own
# slot the moment it finishes, another gate can take that same path immediately,
# and this gate's exit-time sweep still has the path in GROUP_SLOTS. Without an
# owner stamp that sweep deletes the other gate's LIVE slot, silently shrinking
# the machine-wide budget it is the whole point of this mechanism to hold — and
# only when gates overlap, which is the only case it exists for.
GATE_ID="$$-$(date +%s)"

slot_key() {
  local common
  common="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  [ -n "$common" ] || common="$REPO_ROOT"
  case "$common" in /*) ;; *) common="$REPO_ROOT/$common" ;; esac
  printf '%s' "$common" | cksum | awk '{print $1}'
}

# A slot whose owner is gone is not a slot. Reclaimed ONLY on a dead pid, never
# on age: #1697 was an age-based cleanup that deleted a LIVE sandbox, and the
# same mistake here would hand two gates the same slot.
slot_reclaim_dead() {
  local slot pid reclaimed=0
  for slot in "$SLOT_ROOT"/*; do
    [ -d "$slot" ] || continue
    pid=""
    [ -r "$slot/pid" ] && pid="$(cat "$slot/pid" 2>/dev/null || true)"
    if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
      rm -rf "$slot" 2>/dev/null && reclaimed=1
    fi
  done
  [ "$reclaimed" -eq 1 ]
}

slots_in_use() {
  local slot n=0
  for slot in "$SLOT_ROOT"/*; do
    [ -d "$slot" ] && n=$((n + 1))
  done
  printf '%s\n' "$n"
}

# Take one slot, blocking until the machine-wide budget allows it. Prints the
# slot's path on stdout; the caller's child owns it and removes it when done.
slot_acquire() {
  mkdir -p "$SLOT_ROOT"
  local i waited=0 announced=0
  while : ; do
    i=1
    while [ "$i" -le "$CI_LOCAL_JOBS" ]; do
      if mkdir "$SLOT_ROOT/$i" 2>/dev/null; then
        # `owner` is written once and never rewritten; `pid` is rewritten by the
        # grouped child so a killed child's slot becomes reclaimable. They answer
        # different questions: `pid` is "is the holder alive", `owner` is "is
        # this still the same holder".
        printf '%s\n' "$GATE_ID" > "$SLOT_ROOT/$i/owner"
        printf '%s\n' "$$" > "$SLOT_ROOT/$i/pid"
        printf '%s\n' "$SLOT_ROOT/$i"
        return 0
      fi
      i=$((i + 1))
    done
    slot_reclaim_dead && continue
    if [ "$announced" -eq 0 ]; then
      echo "  (waiting for a heavy-step slot: $(slots_in_use)/$CI_LOCAL_JOBS in use" \
           "machine-wide, shared with any other gate for this repository)" >&2
      announced=1
    fi
    sleep 1
    waited=$((waited + 1))
    if [ "$waited" -ge "$CI_LOCAL_SLOT_WAIT" ]; then
      echo "  ! waited ${waited}s for a heavy-step slot and gave up waiting;" \
           "proceeding OVER the machine-wide budget of $CI_LOCAL_JOBS." >&2
      printf '\n'
      return 0
    fi
  done
}

# Release a slot ONLY if this gate still owns it. A path we no longer own is
# either already gone or now another gate's live slot; in both cases the correct
# action is nothing.
slot_release() { # slot_release <slot-dir>
  [ -n "${1:-}" ] || return 0
  [ -d "$1" ] || return 0
  local owner=""
  [ -r "$1/owner" ] && owner="$(cat "$1/owner" 2>/dev/null || true)"
  [ "$owner" = "$GATE_ID" ] || return 0
  rm -rf "$1" 2>/dev/null || true
  return 0
}

# Hand back every slot this gate still holds. Called on interrupt and at exit:
# a leaked slot would shrink the budget for the next gate until its pid file
# aged into a dead pid, and the pid would be OURS, so nothing else could reclaim
# it while we lived.
release_own_slots() {
  [ -n "${GROUP_SLOTS+x}" ] || return 0
  local slot
  for slot in ${GROUP_SLOTS[@]+"${GROUP_SLOTS[@]}"}; do
    slot_release "$slot"
  done
  GROUP_SLOTS=()
  return 0
}

# Report the neighbours rather than hide them: "why is my gate slower than the
# 7m10s in the comments" has exactly one common answer.
announce_neighbours() {
  mkdir -p "$SLOT_ROOT" 2>/dev/null || true
  slot_reclaim_dead >/dev/null 2>&1 || true
  local n
  n="$(slots_in_use)"
  if [ "$n" -gt 0 ]; then
    echo "Note: $n of $CI_LOCAL_JOBS machine-wide heavy-step slots are already in use" \
         "by another gate for this repository. Concurrent gates are supported and" \
         "share this budget (#1983); expect a longer wall clock, not a failure."
  fi
}

# ── Orphan reaping ───────────────────────────────────────────────────────────
#
# `go test` children outliving the gate that spawned them were observed
# alongside #1983. The repository rule is that a background process's PID is
# captured at spawn, killed BY PID, and verified dead — `jobs` from a later
# shell does not see them and `pkill go` is not this gate's business.
#
# Descendants are enumerated BEFORE anything is signalled: once a `go test`
# parent dies its children reparent to init and `pgrep -P` can no longer find
# them.
descendant_pids() { # descendant_pids <pid>
  local pid="$1" kid
  for kid in $(pgrep -P "$pid" 2>/dev/null || true); do
    descendant_pids "$kid"
    printf '%s\n' "$kid"
  done
}

reap_group_children() {
  # The traps are armed before the group arrays are declared, so this must
  # tolerate their absence rather than dying under `set -u`.
  [ -n "${GROUP_PIDS+x}" ] || return 0
  [ "${#GROUP_PIDS[@]}" -eq 0 ] && return 0
  local pid all="" p survivors=""
  for pid in "${GROUP_PIDS[@]}"; do
    kill -0 "$pid" 2>/dev/null || continue
    all="$all $(descendant_pids "$pid" | tr '\n' ' ') $pid"
  done
  [ -n "${all// /}" ] || return 0
  echo "  (reaping ${#GROUP_PIDS[@]} concurrent step(s) and their children)" >&2
  for p in $all; do kill -TERM "$p" 2>/dev/null || true; done
  sleep 1
  for p in $all; do
    kill -0 "$p" 2>/dev/null && kill -KILL "$p" 2>/dev/null || true
  done
  sleep 1
  for p in $all; do
    kill -0 "$p" 2>/dev/null && survivors="$survivors $p"
  done
  if [ -n "${survivors// /}" ]; then
    echo "  ! these child pids survived TERM and KILL and are still running:$survivors" >&2
  fi
  return 0
}

on_exit() {
  # Only the main shell cleans up: a grouped step runs in a subshell that
  # inherits this trap, and letting it release the lock would hand the gate to
  # a second instance mid-run.
  [ "${BASHPID:-$$}" = "$MAIN_PID" ] || return 0
  reap_group_children
  release_own_slots
  return 0
}

on_signal() { # on_signal <name>
  [ "${BASHPID:-$$}" = "$MAIN_PID" ] || exit 1
  echo "" >&2
  echo "✗ ci-local.sh interrupted by SIG$1 — stopping concurrent steps." >&2
  reap_group_children
  release_own_slots
  exit 130
}

# `--list-steps` arms nothing: it runs no steps, and ci-local.sh invokes it on
# itself as its own first step.
# CI_LOCAL_SLOT_ROOT exists for scripts/test-ci-local-concurrency.sh: the arms
# below assert the budget's behaviour, and pointing them at the REAL shared root
# would make them depend on whatever else is running on the box.
SLOT_ROOT="${CI_LOCAL_SLOT_ROOT:-${TMPDIR:-/tmp}/nightgauge-ci-local-slots-$(slot_key)}"
if [ "$LIST_STEPS" -eq 0 ]; then
  trap on_exit EXIT
  trap 'on_signal INT' INT
  trap 'on_signal TERM' TERM
fi
if [ "$SLOT_PROBE" -eq 1 ]; then
  # Take one slot exactly as a grouped step does, report what the budget looked
  # like at that moment, hold it, hand it back. This is how
  # scripts/test-ci-local-concurrency.sh asserts the budget is machine-wide
  # without running two 7-minute gates.
  probe_slot="$(slot_acquire)"
  GROUP_SLOTS+=("$probe_slot")
  echo "slot acquired: $probe_slot in_use: $(slots_in_use) budget: $CI_LOCAL_JOBS"
  [ "$SLOT_PROBE_HOLD" != "0" ] && sleep "$SLOT_PROBE_HOLD"
  release_own_slots
  echo "slot released"
  exit 0
fi
if [ "$RELEASE_PROBE" -eq 1 ]; then
  # Assert the release guard in one process, because the race it prevents cannot
  # be staged from outside: it needs a slot path this gate still has in
  # GROUP_SLOTS whose `owner` has since become someone else's. Take a slot, forge
  # a foreign owner onto it (what a reused path looks like after another gate
  # claimed it), then release and report whether the slot survived.
  rp_slot="$(slot_acquire)"
  GROUP_SLOTS+=("$rp_slot")
  printf '%s\n' "$rp_slot" > /dev/null
  echo "release probe: acquired $rp_slot"
  printf 'some-other-gate-9999\n' > "$rp_slot/owner"
  release_own_slots
  if [ -d "$rp_slot" ]; then
    echo "release probe: foreign slot SURVIVED"
  else
    echo "release probe: foreign slot DELETED"
  fi
  # Now the same slot, still ours, must be released normally — otherwise the
  # guard would be trivially satisfied by never releasing anything.
  rm -rf "$rp_slot" 2>/dev/null || true
  rp_own="$(slot_acquire)"
  GROUP_SLOTS=("$rp_own")
  release_own_slots
  if [ -d "$rp_own" ]; then
    echo "release probe: own slot LEAKED"
  else
    echo "release probe: own slot released"
  fi
  rm -rf "$rp_own" 2>/dev/null || true
  exit 0
fi
if [ "$LIST_STEPS" -eq 0 ]; then
  announce_neighbours
fi

FAIL_COUNT=0
# Per-step wall clock, parallel arrays (#1217).
STEP_SECONDS=()
STEP_LABELS=()
GATE_STARTED=$SECONDS
FAILED_STEPS=()
FAILED_LOGS=()
# Parallel to FAILED_STEPS: "assert" (a check ran and said no) or "infra" (the
# harness could not run the check — a git lock, no temp space, a child killed
# under load). #1983: those are different diagnoses and the summary used to
# print both as "exit 1". An infra failure says NOTHING about the diff, and
# reporting it as a check failure is how a red gate gets dismissed as flaky.
FAILED_KINDS=()
INFRA_COUNT=0
# SKIPPED is a THIRD state, alongside passed and failed, in the same spirit as
# #1983's INFRASTRUCTURE ERROR: "this check did not run" is a different fact from
# "this check said yes", and collapsing the two is how a gate reports green over
# something it never looked at. A skipped step is never counted as a pass, never
# silently omitted, and is named with its reason in the summary of every run that
# has one (#1985).
SKIPPED_STEPS=()
SKIPPED_REASONS=()
SKIP_COUNT=0

# Every step's output is captured as well as streamed. Without this a failure
# that does not reproduce is unidentifiable after the fact: the run scrolls
# past, `npm` only logs its own exit code (never the test runner's stdout), and
# a caller that pipes to `tail` discards the very lines naming the failing
# test. Recovering "which test failed" must never depend on having guessed the
# right pipeline beforehand.
LOG_DIR="${CI_LOCAL_LOG_DIR:-$REPO_ROOT/.ci-local-logs}"
if [ "$LIST_STEPS" -eq 0 ] && [ "$SLOT_PROBE" -eq 0 ] && [ "$RELEASE_PROBE" -eq 0 ]; then
  mkdir -p "$LOG_DIR"
  rm -f "$LOG_DIR"/*.log 2>/dev/null || true
fi

# `strip_ansi`, `failure_markers` and `classify_failure` live in a sourced lib
# so scripts/test-ci-local-concurrency.sh can assert them without running the
# whole gate — the reason the ANSI defect they fix survived (#1983).
# shellcheck source=scripts/lib/ci_local_failures.sh
. "$REPO_ROOT/scripts/lib/ci_local_failures.sh"

# The exit code a child never got to write. Used for the case where a grouped
# step's exit code is missing entirely.
CI_LOCAL_INFRA_EXIT=2

record_failure() { # record_failure <label> <log> <kind>
  FAIL_COUNT=$((FAIL_COUNT + 1))
  FAILED_STEPS+=("$1")
  FAILED_LOGS+=("$2")
  FAILED_KINDS+=("$3")
  [ "$3" = "infra" ] && INFRA_COUNT=$((INFRA_COUNT + 1))
  return 0
}

# skip_step <label> <reason> — record a step as NOT RUN.
#
# It still prints its label under `--list-steps`, so the step-inventory guard
# (#983) sees the same inventory whether or not the run was scoped. That is the
# point: #983 exists because a step that VANISHES is invisible in a green exit,
# and a scoped gate must not reintroduce that by another door. A step may be
# skipped, loudly, by name; a step may never disappear.
skip_step() { # skip_step <label> <reason>
  if [ "$LIST_STEPS" -eq 1 ]; then
    printf '%s\n' "$1"
    return 0
  fi
  echo ""
  echo "⊘ $1"
  echo "  SKIPPED — $2"
  echo "  NOT RUN, and therefore NOT PASSED. CI will run it on the pull request."
  SKIPPED_STEPS+=("$1")
  SKIPPED_REASONS+=("$2")
  SKIP_COUNT=$((SKIP_COUNT + 1))
  STEP_SECONDS+=("0")
  STEP_LABELS+=("$1 [SKIPPED]")
  return 0
}

# The skipped block prints on PASS and on FAIL, before either verdict, and is
# never folded into a count of passes (#1985).
print_skip_summary() {
  [ "$SKIP_COUNT" -eq 0 ] && return 0
  local i
  echo ""
  echo "⊘ $SKIP_COUNT step(s) were SKIPPED by --changed. They did NOT run, so they"
  echo "  asserted NOTHING about your diff — they are not passes:"
  for i in "${!SKIPPED_STEPS[@]}"; do
    echo "  ⊘ ${SKIPPED_STEPS[$i]}"
    echo "      because: ${SKIPPED_REASONS[$i]}"
  done
  echo ""
  echo "  This was a PARTIAL gate. The complete gate is \`bash scripts/ci-local.sh\`"
  echo "  with no flags, which is what the repository rule means by \"run the"
  echo "  complete local gate once before every push\". PR CI runs every step"
  echo "  above regardless, so a miss here costs a CI round trip, not a bad merge."
  return 0
}

# The final verdict, factored out so `--summary-probe` drives the REAL thing
# rather than a copy that could drift from it (#1985). Returns the exit code.
print_final_verdict() {
  local i matches
  if [ "$FAIL_COUNT" -eq 0 ]; then
    if [ "$SKIP_COUNT" -gt 0 ]; then
      echo ""
      echo "✓ Every CI-parity check that RAN passed — but $SKIP_COUNT was/were SKIPPED (above)."
      echo "  This is NOT \"all checks passed\"."
    else
      echo "✓ All CI-parity checks passed."
    fi
    return 0
  else
    if [ "$INFRA_COUNT" -gt 0 ]; then
      echo "✗ $FAIL_COUNT check(s) failed — $INFRA_COUNT of them INFRASTRUCTURE errors:"
    else
      echo "✗ $FAIL_COUNT check(s) failed:"
    fi
    for i in "${!FAILED_STEPS[@]}"; do
      if [ "${FAILED_KINDS[$i]}" = "infra" ]; then
        echo "  ! ${FAILED_STEPS[$i]}  [INFRASTRUCTURE — the check could not run]"
      else
        echo "  - ${FAILED_STEPS[$i]}"
      fi
      echo "      full output: ${FAILED_LOGS[$i]}"
      # Pull the failing assertions up to the summary. A vitest failure can sit
      # thousands of lines above the exit line, so "scroll up" is not a usable
      # instruction — and is exactly how a failure escapes identification.
      #
      # ANSI is stripped first: colour-printing suites put an escape sequence
      # between the indent and the ✗, so this grep matched none of them (#1983).
      matches="$(failure_markers "${FAILED_LOGS[$i]}" 15 || true)"
      if [ -n "$matches" ]; then
        printf '%s\n' "$matches" | sed 's/^/      /'
      else
        # NEVER SAY ONLY "SEE THE LOG ABOVE" (#1983). A non-zero exit with no
        # recognised marker is the case an operator cannot act on, so print the
        # tail of the log here rather than describing the absence of a message.
        echo "      no failure marker matched — last 20 lines of the log:"
        strip_ansi "${FAILED_LOGS[$i]}" | tail -20 | sed 's/^/      | /'
      fi
    done
    if [ "$INFRA_COUNT" -gt 0 ]; then
      echo ""
      echo "The ! step(s) above are INFRASTRUCTURE errors: the check could not run, so"
      echo "it asserted nothing about your diff either way. The usual cause is another"
      echo "gate or build running at the same time — see \"Concurrency\" at the top of"
      echo "this script. Re-run the gate on an idle machine before reading these as"
      echo "failures of your change, and never as \"flaky\": an unexplained red is a"
      echo "bug in this gate and wants an issue."
    fi
    if [ "$FAIL_COUNT" -gt "$INFRA_COUNT" ]; then
      echo ""
      echo "Fix the failures before pushing. Most format/lint failures are auto-fixable:"
      echo "  npm run format"
      echo "  npm run lint -- --fix"
    fi
    return 1
  fi
}

run_step() {
  local label="$1"
  shift
  # `--list-steps` prints the inventory the gate WILL run, in execution order,
  # and runs nothing. scripts/test-ci-local-inventory.sh diffs it against the
  # checked-in scripts/ci-local-steps.txt, so a deleted or reworded step goes
  # red — an assertion against the list the script actually runs, which a
  # substring grep for `run_step` structurally cannot make.
  if [ "$LIST_STEPS" -eq 1 ]; then
    printf '%s\n' "$label"
    return 0
  fi
  local slug log code started elapsed
  slug="$(printf '%s' "$label" | tr -c '[:alnum:]' '-' | tr -s '-' | sed 's/^-//; s/-$//')"
  log="$LOG_DIR/${slug}.log"
  echo ""
  echo "▶ $label"
  echo "  \$ $*"
  started=$SECONDS
  # `tee` keeps the terminal output live; PIPESTATUS[0] preserves the command's
  # own exit code, which a bare pipeline would otherwise replace with tee's.
  "$@" 2>&1 | tee "$log"
  code=${PIPESTATUS[0]}
  elapsed=$((SECONDS - started))
  # Per-step wall clock (#1217). Without it "the gate is slow" is unactionable:
  # 47 sequential steps and no way to tell which three of them are the cost.
  STEP_SECONDS+=("$elapsed")
  STEP_LABELS+=("$label")
  if [ "$code" -eq 0 ]; then
    echo "  ✓ $label (${elapsed}s)"
  else
    local kind
    kind="$(classify_failure "$log" "$code")"
    if [ "$kind" = "infra" ]; then
      echo "  ! $label (INFRASTRUCTURE ERROR, exit $code, ${elapsed}s)"
    else
      echo "  ✗ $label (exit $code, ${elapsed}s)"
    fi
    record_failure "$label" "$log" "$kind"
  fi
}

# ── Parallel group runner (#1217) ───────────────────────────────────────────
#
# The gate was 836s of steps in 836s of wall clock — purely serial, at 189% CPU
# on a machine with far more cores. The fix is not "run everything with &": most
# steps here are ordered on purpose. `make generate-ipc-client` REWRITES a
# tracked file that a later step checks for drift; `npm run build` writes the
# dist/ that the test steps import; the mirror regeneration rewrites files the
# mirror gate then diffs; the publication-boundary scan reads the whole tracked
# tree and must not race a writer. Parallelising any of those produces exactly
# the order-dependent flake this gate exists to prevent — and some of those
# failures would be FALSE GREEN, which is worse than slow.
#
# So only steps that are provably read-only w.r.t. the working tree join a
# group. Each one either shells into a `mktemp -d` sandbox seeded from
# `git archive HEAD` (every regression suite here does) or only reads. The
# membership rule is written at the group's declaration site, not here — a
# reader must be able to see WHY a given step is safe to run concurrently.
#
# Failure handling is the load-bearing part. `run_step` accumulates failures in
# shell arrays, and a backgrounded step runs in a SUBSHELL whose array writes
# are discarded on exit — so a naive port loses failures silently and the gate
# reports green over a red step. Exit codes therefore travel through FILES, and
# the parent re-reads them after `wait`. `run_group_wait` is what appends to
# FAILED_STEPS, in the parent shell, in declared order.
GROUP_LABELS=()
GROUP_PIDS=()
GROUP_CODEFILES=()
GROUP_LOGS=()
GROUP_STARTS=()


# CI_LOCAL_JOBS is declared with the slot budget above, because since #1983 it
# bounds the MACHINE rather than this process.

# Escape hatch: CI_LOCAL_SERIAL=1 runs every grouped step inline, in declared
# order, exactly as before. For bisecting a failure whose interleaving matters,
# and as the answer to "is this new runner lying to me?".
CI_LOCAL_SERIAL="${CI_LOCAL_SERIAL:-0}"

run_group() {
  local label="$1"
  shift
  if [ "$LIST_STEPS" -eq 1 ]; then
    printf '%s\n' "$label"
    return 0
  fi
  if [ "$CI_LOCAL_SERIAL" = "1" ]; then
    run_step "$label" "$@"
    return 0
  fi

  # Throttle against the MACHINE-WIDE budget, not this shell's job table
  # (#1983). `jobs -rp` counts only our own children, so three gates each
  # admitted four heavy steps and the box ran twelve — three `go test ./...`,
  # three `-race` passes and three vitest runs on 12 cores, at load 58, where
  # children were killed before they could record an exit code. Blocking here
  # keeps gates concurrent and the box merely busy.
  local slot
  slot="$(slot_acquire)"
  GROUP_SLOTS+=("$slot")

  local slug log codefile
  slug="$(printf '%s' "$label" | tr -c '[:alnum:]' '-' | tr -s '-' | sed 's/^-//; s/-$//')"
  log="$LOG_DIR/${slug}.log"
  codefile="$LOG_DIR/${slug}.exitcode"
  rm -f "$codefile"
  echo "▶ $label (started, concurrent)"
  # The exit code goes to a FILE, not a variable: this subshell's variables die
  # with it. `$!` is captured immediately so the parent can wait on this exact
  # child rather than on `jobs`, whose table does not survive into the parent's
  # later commands.
  # The child records its OWN duration next to its own exit code. Measuring it
  # in the parent after `wait` timed queue-to-group-end, so every grouped step
  # reported the group's total and the summary was useless for finding the
  # expensive one.
  # The child OWNS its slot: it rewrites the pid file with its own pid, so if it
  # is killed the slot becomes reclaimable by any gate, and it removes the slot
  # when it finishes so the budget frees up without waiting for `run_group_wait`.
  ( local_start=$SECONDS
    [ -n "$slot" ] && printf '%s\n' "$BASHPID" > "$slot/pid" 2>/dev/null
    "$@" > "$log" 2>&1
    printf '%s\n' "$?" > "$codefile"
    printf '%s\n' "$((SECONDS - local_start))" > "$codefile.secs"
    [ -n "$slot" ] && slot_release "$slot"
    true ) &
  GROUP_PIDS+=("$!")
  GROUP_LABELS+=("$label")
  GROUP_CODEFILES+=("$codefile")
  GROUP_LOGS+=("$log")
  GROUP_STARTS+=("$SECONDS")
}

# Wait for every started group step, then fold the results into the same
# FAIL_COUNT / FAILED_STEPS the serial path uses, in DECLARED order so output is
# reproducible regardless of which step happened to finish first.
run_group_wait() {
  [ "$LIST_STEPS" -eq 1 ] && return 0
  [ "${#GROUP_PIDS[@]}" -eq 0 ] && return 0

  local i pid code elapsed
  for pid in "${GROUP_PIDS[@]}"; do
    wait "$pid" 2>/dev/null || true
  done

  for i in "${!GROUP_LABELS[@]}"; do
    if [ -r "${GROUP_CODEFILES[$i]}.secs" ]; then
      elapsed="$(cat "${GROUP_CODEFILES[$i]}.secs" 2>/dev/null || echo 0)"
    else
      elapsed=$((SECONDS - GROUP_STARTS[i]))
    fi
    # A missing exit-code file means the child died without writing one (killed,
    # disk full, a `set -e` in an unexpected place). Treat it as FAILURE, never
    # as success: an unobservable step is the false-green case this whole
    # mechanism exists to avoid.
    local unrecorded=0
    if [ -r "${GROUP_CODEFILES[$i]}" ]; then
      code="$(cat "${GROUP_CODEFILES[$i]}" 2>/dev/null || echo 1)"
    else
      # THE CASE THAT COST A DIAGNOSTIC CYCLE (#1983). A child killed under
      # concurrent load — OOM, an exhausted process table — never writes its
      # exit code, and this branch synthesised `exit 1`. Downstream that is
      # indistinguishable from a suite that asserted false, so the operator
      # reads a red required-looking gate whose log is nothing but passes.
      # It is an INFRASTRUCTURE failure and now says so by name.
      code="$CI_LOCAL_INFRA_EXIT"
      unrecorded=1
    fi
    [ -n "$code" ] || code=1

    cat "${GROUP_LOGS[$i]}" 2>/dev/null || true
    STEP_SECONDS+=("$elapsed")
    STEP_LABELS+=("${GROUP_LABELS[$i]}")
    if [ "$code" -eq 0 ] 2>/dev/null; then
      echo "  ✓ ${GROUP_LABELS[$i]} (${elapsed}s, concurrent)"
    else
      local kind
      if [ "$unrecorded" -eq 1 ]; then
        kind=infra
        echo "  ! ${GROUP_LABELS[$i]} (INFRASTRUCTURE ERROR, ${elapsed}s, concurrent):"
        echo "      its child died without recording an exit code — killed, out of"
        echo "      memory, or out of process slots. The step did not report a result,"
        echo "      so NOTHING has been asserted about your diff. Re-run the gate."
      else
        kind="$(classify_failure "${GROUP_LOGS[$i]}" "$code")"
        if [ "$kind" = "infra" ]; then
          echo "  ! ${GROUP_LABELS[$i]} (INFRASTRUCTURE ERROR, exit $code, ${elapsed}s, concurrent)"
        else
          echo "  ✗ ${GROUP_LABELS[$i]} (exit $code, ${elapsed}s, concurrent)"
        fi
      fi
      record_failure "${GROUP_LABELS[$i]}" "${GROUP_LOGS[$i]}" "$kind"
    fi
  done

  # Every child has exited, so every slot it owned is gone. Drop our record of
  # them so `release_own_slots` cannot delete a slot a LATER child now owns.
  release_own_slots
  GROUP_LABELS=(); GROUP_PIDS=(); GROUP_CODEFILES=(); GROUP_LOGS=(); GROUP_STARTS=()
}

if [ "$GROUP_PROBE" -gt 0 ]; then
  OBS="$LOG_DIR/group-probe.observations"
  : > "$OBS"
  i=1
  while [ "$i" -le "$GROUP_PROBE" ]; do
    # Each child records how many slots were held the moment it began, so the
    # maximum below is observed from inside the budget rather than inferred.
    run_group "group probe $i" \
      env SLOT_ROOT="$SLOT_ROOT" OBS="$OBS" HOLD="$GROUP_PROBE_HOLD" \
      sh -c 'ls -d "$SLOT_ROOT"/*/ 2>/dev/null | wc -l | tr -d " " >> "$OBS"; sleep "$HOLD"'
    i=$((i + 1))
  done
  # Reproduce the failure this issue is about: a grouped child killed before it
  # can write its exit code. `kill -KILL` on the SUBSHELL, by the pid captured at
  # spawn, is exactly what the kernel did under load 58.
  if [ "${CI_LOCAL_PROBE_KILL_CHILD:-0}" = "1" ] && [ "${#GROUP_PIDS[@]}" -gt 0 ]; then
    # The LAST child spawned: the earlier ones may already have finished while a
    # later one queued for a slot, and killing a finished child proves nothing.
    kill -KILL "${GROUP_PIDS[$((${#GROUP_PIDS[@]} - 1))]}" 2>/dev/null || true
  fi
  run_group_wait
  echo "group probe: max concurrent slots $(sort -n "$OBS" | tail -1) budget $CI_LOCAL_JOBS"
  echo "group probe: fail=$FAIL_COUNT infra=$INFRA_COUNT"
  exit 0
fi

if [ "$SUMMARY_PROBE" -eq 1 ]; then
  # One step that really passes and one that is really skipped, through the real
  # `run_step` / `skip_step` / `print_skip_summary` / `print_final_verdict` path.
  # The property under test is that the verdict of a run containing a skip is NOT
  # "All CI-parity checks passed." — the whole defence against a scoped gate
  # reporting green over a step it never looked at.
  run_step "summary probe passing step" true
  skip_step "summary probe skipped step" "the probe asked for one, to exercise this path"
  print_skip_summary
  print_final_verdict
  exit $?
fi

if [ "$LIST_STEPS" -eq 0 ]; then
  echo "CI-parity local validation — order mirrors .github/workflows/ci.yml"
fi

# 0. Step-inventory guard (#983) — FIRST, because it is the only check that can
#    tell whether the rest of this file is still the gate it claims to be. The
#    steps below used to be guarded by `if [ -f <script> ]` with no `else`, so a
#    deleted script removed its step and this file still exited 0. It cannot
#    detect that itself: a skipped step and a passing step are identical in its
#    output and in its exit code. This diffs `--list-steps` against the
#    checked-in scripts/ci-local-steps.txt, which is a second observer.
run_step "ci-local.sh step inventory" bash scripts/test-ci-local-inventory.sh

# 0b. The gate's own concurrency and failure-reporting contract (#1983). Second,
#     because everything after it is read through the reporting helpers this
#     asserts: an ANSI-blind marker grep made every coloured failure in every
#     suite here summarise as "(no recognised failure marker)", and a grouped
#     child killed under load was reported as a plain `exit 1`, i.e. as if a
#     check had asserted false. It also pins the single-instance lock, which is
#     the general answer to a property reported three times (#1607, #1697,
#     #1983). Runs serially and takes seconds: it drives the lock through
#     `--lock-probe`, never a second full gate.
run_step "ci-local.sh concurrency and failure-reporting contract" \
  bash scripts/test-ci-local-concurrency.sh

# 0c. The gate's own change-scoping contract (#1985). Third, for the same reason
#     0b is second: `--changed` can remove the two most expensive steps from a
#     run, and the only thing standing between that and a false green is the
#     rule that a skipped step is reported as a third state rather than as a
#     pass. Runs in seconds against git fixtures, via `--scope-probe`, never a
#     second full gate.
run_step "ci-local.sh change-scoping contract" \
  bash scripts/test-ci-local-changed-scope.sh

# 1. Go build + tests (internal/ + cmd/)
# -json so the skip accounting has events to read (#474): a package whose tests
# all SKIPPED still prints `ok`, so without it a guard that stopped guarding is
# indistinguishable from one that passed. Piped back through
# go-test-json-echo.py so the log stays readable.
#
# `--changed` (#1985) may skip the two TEST passes below — never `go build`,
# never `gofmt`: those are seconds, and a compile error is not confined to the
# package that changed. The decision is made once, here, and both suites report
# the same reason.
decide_go_scope
run_step "go build ./..." go build ./...
if [ "$GO_SCOPE_RUN" -eq 0 ]; then
  skip_step "go test ./... -count=1 (with skip accounting)" \
    "--changed, and $GO_SCOPE_REASON"
else
run_group "go test ./... -count=1 (with skip accounting)" \
  bash -c 'set -o pipefail; go test -json ./... -count=1 | tee go-test.json | python3 scripts/go-test-json-echo.py && python3 scripts/check-go-test-skips.py go-test.json'
fi
# Mirrors the race half of ci.yml's "Test (plain and race, concurrently)" step
# (#493, merged with the plain pass in #1218), which replaced the
# internal/orchestrator-scoped step from #428 — one race pass, not two. The
# scoped step existed because the race detector is the only thing that fails
# when a drainBackground() join is deleted from a test body, and that argument
# was never specific to one package. THAT DECISION IS UNCHANGED by anything
# below; only its stated cost moved.
#
# Measured whole-tree cost, with the date and the machine class next to the
# number this time (#1985):
#
#   #1218,  2026-09-04, Apple M-series, idle:  168s plain -> 178s race  (+6%)
#   #1991,  2026-09-22, idle 12-core Apple M:  194s plain -> 202s race  (+4%)
#
# So the +6% conclusion holds: the race detector is nearly free on top of the
# plain run, not the ~3x once feared. What the second row adds is the OTHER
# number, which the first row never stated — the two passes together are ~396s
# of a 10m31s gate, about 63% of it, and on a diff that cannot reach Go neither
# pass can observe the change. That is the case `--changed` answers.
#
# A 288s/297s figure was reported on 2026-09-22 and withdrawn by its author the
# same day: it was taken while load from a concurrent gate was still draining.
# It is recorded here only so the next reader does not rediscover it and
# conclude this comment is stale. Any number quoted here must carry the date and
# the machine state it was taken under, for exactly that reason.
#
# On a box running a second gate both passes are slower, which is the budget
# above doing its job rather than a regression.
if [ "$GO_SCOPE_RUN" -eq 0 ]; then
  skip_step "go test -race -count=1 ./..." \
    "--changed, and $GO_SCOPE_REASON"
else
run_group "go test -race -count=1 ./..." \
  go test -race -count=1 ./...
fi
run_step "gofmt -l ./internal ./cmd" \
  bash -c '! gofmt -l ./internal ./cmd | grep .'

# 1b. branch-merged-check.sh regression suite — the shell-side decision
#     procedure `git branch -D` reclaim decisions defer to (docs/GIT_WORKFLOW.md
#     § After Merge). Kept aligned with the Go sweep's own ancestry-acceptance
#     door (#593).
run_group "branch-merged-check.sh regression suite" \
  bash scripts/test-branch-merged-check.sh

# 1b2. post-merge-check.sh regression suite — the OTHER verification idiom
#      AGENTS.md mandates and that hand-writing gets wrong (#1038). Its two
#      motivating states — an empty check-run list and a still-running check —
#      cannot be produced on demand against live CI, so they are only ever
#      exercised here.
run_group "post-merge-check.sh regression suite" \
  bash scripts/test-post-merge-check.sh

# 1b2a. publish-vsix-set.sh regression suite — the registry publish loop. Its
#       motivating state is a partial release: the v0.4.2 Open VSX publish had
#       one target land, the next hit a 503, and the third was never attempted,
#       so the release advertised a version most platforms could not install and
#       a re-run could not repair it. Neither a transient 5xx nor an
#       already-published conflict can be produced on demand against a live
#       registry, so they are only ever exercised here.
run_group "publish-vsix-set.sh regression suite" \
  bash scripts/test-publish-vsix-set.sh

# 1b2b. sign-macos-binaries.sh regression suite. The state it must handle
#       correctly is the one that exists today: no Apple credentials, so it has
#       to be a clean no-op rather than a release-breaking failure. It must also
#       never report success for a signature that does not verify, since a
#       broken signature is worse than none because it looks deliberate. Neither
#       state can be produced against real codesign without a certificate.
run_group "sign-macos-binaries.sh regression suite" \
  bash scripts/test-sign-macos-binaries.sh

# 1b2c. malware-scan.sh regression suite — the artifact scan that now gates
#       every release and Marketplace publish. Its failure mode is silence:
#       `clamscan` exits 0 both for a clean artifact and for one it never read
#       (measured: `--max-filesize=1M` on the real .vsix reports "Data scanned:
#       0 B" and exits 0). The gate therefore asserts a floor on bytes actually
#       scanned, and this suite is what proves that floor can fail. The arms
#       are stubbed so they run without ClamAV installed; a real-clamscan arm
#       asserts the tool still emits the summary fields the script parses, so
#       the stubs cannot drift. No arm plants an EICAR sample -- this suite
#       runs only on developer machines, where that would trip real antivirus.
run_group "malware-scan.sh regression suite" \
  bash scripts/test-malware-scan.sh

# 1b3. scrub-evidence.sh regression suite (#1335) — the second of the two
#      layers that must each stop a credential reaching a public artifact. The
#      first is the output-channel sanitizer, which matches secret SHAPES; this
#      one matches the exact VALUES the harness was handed, so a credential in a
#      format nobody has a pattern for is still caught. It only ever runs inside
#      the clean-install container, which no ordinary change exercises — so
#      without this step the layer is unverified until the next incident.
run_group "scrub-evidence.sh regression suite" \
  bash scripts/test-scrub-evidence.sh

# 1b4. capture-cli-help.sh regression suite (#1617) — the capture script
#      installs third-party CLIs and runs them. Its safeguards (no credential
#      reaches a child, only --help runs, the mktemp prefix is removed, a call
#      past its timeout is killed with everything it started) are exercised
#      only here, against stubs: a real capture downloads every CLI, which no
#      ordinary change does. It writes nothing outside its own temp dirs.
run_group "capture-cli-help.sh regression suite" \
  bash scripts/test-capture-cli-help.sh

# 1b5. adapter-canary.sh regression suite (#1639) — the scheduled latest-CLI
#      canary's implementation. Exercised against fixtures and a stubbed gh,
#      never a live install of every manifest CLI (that is the workflow's own
#      daily/PR run): the schema-diff leg, the report job's exact-title
#      dedupe, and the stub-provider's own bounded start/stop.
run_group "adapter-canary.sh regression suite" \
  bash scripts/test-adapter-canary.sh

# 1c. CI change-class gate (#647) — drives scripts/ci-change-class.sh against
#     real git fixtures AND asserts .github/workflows/ci.yml still consumes its
#     outputs. Mirrors ci.yml's own ungated step in the Go job; the wiring half
#     is what makes the gate impossible to document without shipping.
run_group "CI change-class gate regression suite" \
  bash scripts/test-ci-change-class.sh

# 2. Generated files must be in sync
run_step "make generate-ipc-client" make generate-ipc-client
run_step "generated IPC client in sync" \
  git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts

# 2b. Canonical terminal-kind rule table (#306) — the generated SDK module and
#     the behaviour golden must be exactly what table.json renders.
run_step "terminal-kind table consumers in sync" make check-terminal-kind-table

# 2c. Platform operation registry (#750) — api/generated/go/platform/operations.gen.go
#     must be exactly what api/platform-operations.yaml renders. That file carries
#     each operation's credential requirement, which is what the conformance test
#     in internal/platform checks call sites against; a hand-edited copy is a
#     contract nobody reviewed.
run_step "platform operation registry in sync" make check-platform-operations

# No step here for the hand-rolled-platform-request gate (#750): it is enforced
# by TestPlatformRawHTTP_RealPackageIsClean, which step 1's `go test ./...`
# already runs — and which ci.yml runs UNGATED via `go test ./internal/preflight/`.
# `nightgauge preflight platform-raw-http` is the human/skill-facing entry point
# for the same check, not a second gate.

# 3. npm audit allow-list
run_step "npm audit allow-list" node scripts/npm-audit-check.js

# 4. SKILL.md metadata validation. Self-test first, same reasoning as 11 and 5b:
#    the validator reported `missing required field: metadata.source` once, on a
#    file that provably had it and validated clean five times over the identical
#    tree, because a frontmatter block that never closed handed the field checks
#    a truncated buffer (#856). A torn read now fails under its own UNREADABLE
#    message and exit code rather than wearing a missing field's costume — a
#    nondeterministic gate teaches operators to re-run until green, which is the
#    same as not having the gate.
run_step "SKILL.md metadata gate regression suite" \
  bash scripts/test-validate-skill-metadata.sh
run_step "SKILL.md metadata" bash scripts/validate-skill-metadata.sh

# 5. Publication boundary — allowlist, fail-closed. Catches private-class content
#    before it is pushed rather than after CI rejects it.
#
#    Scope is the tracked tree PLUS untracked, non-ignored files (#716). Those
#    are the newest content in a change and the guard used to skip them
#    silently, so this step handed back a pass it had not earned and CI failed
#    on the very files it never opened. A run that scanned untracked files says
#    so on its success line; one that exits non-zero names them inline.
run_step "publication boundary" python3 scripts/publication-boundary-check.py

# 5a. Credential scan — scripts/credential-scan.sh, gitleaks over full history
#     with .gitleaksignore (mirrors .github/workflows/credential-scan.yml).
#     The header listed it as never skipped; until #2075 no step ran it, and
#     PR #2073 passed this gate and failed CI's scan. A missing gitleaks is
#     INFRASTRUCTURE, never a pass.
run_step "credential scan (tree + full history)" bash scripts/credential-scan.sh

# 5b. Publication boundary self-test — proves the guard still fails closed
#     (mirrors .github/workflows/publication-boundary.yml's second step).
run_step "Publication boundary regression suite" bash scripts/test-publication-boundary.sh

# 5b-iii. Rename carry-over (#837) — hermetic, ~0.3s. Deliberately NOT a case
#     in the suite above: exercising a rename means `git mv`-ing a real tracked
#     file, and that suite is re-run inside sandboxes and SIGKILLed mid-run by
#     the hermeticity tests below. A kill between the mv and the restore breaks
#     the checkout. This builds its own throwaway repo instead.
run_step "Publication boundary rename carry-over" \
  python3 scripts/test-publication-boundary-rename.py

# 5b-i-b. Derived reference ceiling (#1078) — the ceiling is inferred from merge
#     history rather than recorded, so the inference is load-bearing. The case
#     that matters is that a pull-request TITLE cannot raise it: the mark comes
#     from text an author partly controls, and a raised ceiling weakens the rule
#     silently. Same throwaway-repo shape as the rename suite, for the same
#     crash-safety reason.
run_step "Publication boundary derived ceiling" \
  python3 scripts/test-publication-boundary-ceiling.py

# 5b-i-c. Branch-local ceiling lag and baseline erosion (#1129) — the derived
#     ceiling is per-branch and `git merge origin/main` does NOT advance it
#     (the merge is the second parent), while `tree_baseline` is one global
#     integer compared against a count that GROWS as the ceiling falls. The two
#     together blocked a branch with references it never wrote. Same
#     throwaway-repo shape: these cases need controlled merge topology.
run_step "Publication boundary ceiling lag and erosion" \
  python3 scripts/test-publication-boundary-erosion.py

# 5b-ii. Publication boundary suite hermeticity (#713, #722) — the suite plants
#     deliberately-forbidden fixtures, so two properties have to hold and
#     neither is self-evident from reading it: a SIGKILLed run leaves the
#     operator's checkout byte-identical, and the worktree registration such a
#     run leaks (trap cannot catch SIGKILL, and prune cannot remove an entry
#     whose directory survives) is reclaimed by the NEXT run. Both are asserted
#     by doing it for real, which costs one extra full suite run.
# 5b-i. Attribution helpers (#832) — the hermeticity assertions below scope
# themselves to what the SUITE writes rather than to global repo state. A
# scoped assertion that scoped away the real failure would be silently useless,
# so the scoping has its own tests. Costs ~1s: no tree scan.
run_step "Publication boundary attribution" \
  bash scripts/test-publication-boundary-attribution.sh

run_step "Publication boundary suite hermeticity" \
  bash scripts/test-publication-boundary-hermeticity.sh

# 5c. Band-vocabulary reintroduction gate (#582) — fails on hand-inlined band
#     closed sets / regex alternations in production source, outside the
#     allowed surfaces (spike #568 §5).
run_step "band-vocabulary reintroduction gate" python3 scripts/check-band-vocabulary.py

# 5d. Band-vocabulary gate self-test — proves the gate still fails closed.
run_group "Band-vocabulary gate regression suite" bash scripts/test-band-vocabulary-check.sh

# 5e. Stale-visibility-prose reintroduction gate (#697) — fails on a tracked
#     artifact unconditionally asserting this repository is private, the
#     exact shape all four #697 instances took.
run_step "visibility-prose reintroduction gate" python3 scripts/check-visibility-prose.py

# 5f. Nonexistent-workflow-reference gate (#545) — fails when a tracked file
# names a `.github/workflows/*.yml` path that does not exist. Self-test first:
# a gate nothing exercises degrades into an unconditional pass.
run_group "Workflow-reference gate regression suite" bash scripts/test-workflow-refs-check.sh
run_group "Marketplace channel regression suite" bash scripts/test-marketplace-channel.sh
run_group "Release channel verification regression suite" bash scripts/test-verify-release-channels.sh
run_step "nonexistent-workflow-reference gate" python3 scripts/check-workflow-refs.py

# 5f2. Proposal-artifact validator regression suite (#1304) — the schema gate
# between the read-only model job and the write job in release-watchdog.yml
# and continuous-improvement.yml. Every case is a shape the gate must reject.
run_group "Proposal-artifact validator regression suite" bash scripts/test-validate-proposal-artifact.sh

# 5g. CLA gate regression suite (#976) — spawns the real .github/scripts/cla-check.mjs
#     against a local HTTP stub and pins its bounded retry: a transient 5xx/429
#     or socket failure is retried, every other 4xx is the gate's own verdict and
#     is raised on the first shot. Mirrors .github/workflows/lint.yml's own step
#     (not ci.yml's — this block is the lint.yml mirror region). Costs ~12s: the
#     backoff is deliberately real, because a test-only zero-delay knob would
#     re-open the mutation the timing assertion exists to kill.
run_group "CLA gate regression suite" node --test .github/scripts/cla-check.test.mjs

# 4b. Cache-boundary measurement smoke test
run_step "Cache-boundary measurement smoke" bash scripts/test-measure-cache-boundary-loss.sh

# 4b. Test-tree typecheck (#499).
# tsconfig.json covers src/** only, vitest transforms through esbuild
# (transpile-only, so `import type` is erased unresolved), and eslint registers
# the TS parser for tests without a `project` — so nothing typechecked tests/**
# or the Playwright fixtures. A type-only import could name an export that no
# longer exists and the suite stayed green.
run_step "VSCode test-tree typecheck" npm run typecheck:tests -w nightgauge-vscode

# 5. ESLint
run_step "ESLint" npm run lint

# 6. Prettier formatting — the #1 cause of avoidable CI failures.
run_step "Prettier format:check" npm run format:check

# 7. Build all workspaces
run_step "npm run build (all workspaces)" npm run build

# 7b. Phase markers ↔ PHASE_REGISTRY drift check
# Runs after the SDK build so the script can import PHASE_REGISTRY from
# the workspace package. Catches the class of registry↔skill marker drift
# before it reaches the orchestrator.
run_step "Phase markers ↔ PHASE_REGISTRY" npx tsx scripts/validate-phase-markers.ts

# 8. Tests (single run — NEVER bare vitest which hangs in watch mode)
run_step "npm run test (all workspaces)" npm run test -- --run

# 9. Generated package contributions in sync
run_step "Generated VSCode contributions in sync" \
  npx -w nightgauge-vscode tsx scripts/generate-package-contributions.ts --check

# 9b. @types/vscode must not exceed engines.vscode. `vsce package` enforces this
# at packaging time (dev-install.sh / release) but no build/test step does — a
# Dependabot bump (#165) raised the types past the engine floor and only broke
# at install. Guard it here so the mismatch fails locally, not at install.
run_step "@types/vscode <= engines.vscode" \
  node packages/nightgauge-vscode/scripts/check-engine-types.mjs

# 10. Markdown link check — cross-document reference integrity (root *.md +
#     docs/**). Self-test first: `markdown-link-check` reports `Status: 0` for a
#     request that never completed, and the gate used to fail on that exactly as
#     on a 404 (#1004). Errored requests are now re-probed and sorted into
#     dead / unreachable-from-runner / alive-after-reprobe; the suite pins both
#     directions, so "stop failing on an errored fetch" cannot be satisfied by
#     no longer failing on a dead internal link.
run_group "Link-check gate regression suite" bash scripts/test-check-md-links.sh
run_group "Markdown link check" bash scripts/check-md-links.sh
run_group "Agent-guidance gate regression suite" \
  bash scripts/test-agent-guidance-check.sh
run_group "Agent-guidance architecture" \
  bash scripts/check-agent-guidance.sh --workspace-block required
# Smart Setup installs this check into downstream repositories from its own
# bundled copy, so the copy must be the canonical file byte-for-byte (issue 1675).
run_group "Smart Setup bundles the canonical agent-guidance check" \
  cmp scripts/check-agent-guidance.sh skills/smart-setup/scripts/check-agent-guidance.sh

# Changelog ↔ release contract (docs/GIT_WORKFLOW.md § Changelog): every
# released tag has a section in CHANGELOG.md and the extension's changelog, and
# no heading is a bare issue number. Self-test first so the gate is proven
# able to go red before it is trusted to be green.
run_group "Changelog gate regression suite" bash scripts/test-check-changelog.sh
run_group "Changelog names every released tag" bash scripts/check-changelog.sh

# 11. Drift-gate self-test — proves the mirror gate below still fails closed
#     rather than passing vacuously, the defect it was created to fix (#539),
#     and that it no longer goes red on a dirty tree it has no quarrel with
#     (#546). Paired with 11b as 5b is paired with 5.
run_group "Mirror drift gate regression suite" \
  bash scripts/test-mirror-drift-gate.sh

# 11a. Agent-skill install targets — Grok/Codex/Claude home copies and
#      --*-only flags, against a throwaway HOME. Self-test includes a
#      vacuous-fail arm (install_grok commented out). Safe in the group only
#      because it runs the installer in a sandbox seeded from `git archive
#      HEAD`: the installer regenerates its repository's plugin-skills mirror,
#      and run against this checkout it made 11b read a half-rebuilt mirror.
#      Arms (g) and (g2) fail if the suite changes `git status --porcelain` or
#      rewrites the checkout's mirror, even with identical bytes.
run_group "Agent-skill install target regression suite" \
  bash scripts/test-install-agent-skills-targets.sh

# 11a2. Issue-body heading contract (#711) — the required-heading table exists
#       in three files (issue-audit SKILL.md, docs/ISSUE_AUDIT.md, and
#       issue-create's authoring rules) and issue-create runs issue-audit as its
#       own terminal gate. When the copies drift, every issue the pipeline
#       authors fails its own audit; that shipped as a WARNING nobody read until
#       #711. Self-test first, same reasoning as 11 and 5b.
run_group "Issue-body contract gate regression suite" \
  bash scripts/test-issue-body-contract.sh
run_step "Issue-body heading contract" \
  python3 scripts/check-issue-body-contract.py

# 11a2. Skill `echo "$VAR" | jq` gate (#1215) — zsh's builtin echo expands
#       backslash escapes, so a JSON `\n` reaches jq as a real newline, the
#       parse aborts, and the caller reads an empty string. On 2026-08-30 that
#       made the issue-audit terminal gate report all five required headings
#       MISSING on an issue that had every one of them. Placed before the mirror
#       drift gate: this is a canonical-skills edit, and 11b will fail anyway if
#       the fix did not reach the mirror. Self-test first, same reasoning as 11.
run_group "Skill echo-into-jq gate regression suite" \
  bash scripts/test-skill-echo-json.sh
run_step "Skill echo-into-jq gate" \
  python3 scripts/check-skill-echo-json.py

# 11b. Plugin skills mirror drift — claude-plugins/nightgauge/skills/ is
#      generated output committed on purpose (the marketplace manifest ships it
#      as the plugin source), so a canonical skills/ edit that never reached it
#      publishes a stale plugin. Mirrors lint.yml's step of the same name.
#
#      Read-only, like every other check in this file: `--check-mirror`
#      regenerates into a temp destination and compares the two trees — paths,
#      contents, symlink targets and the executable bit git tracks — so it
#      neither writes the checkout nor cares what is staged. That matters here
#      specifically — this script is run BECAUSE you have uncommitted work, and
#      the previous form (regenerate in place, fail on a dirty index) therefore
#      misfired in its primary use case (#546). When it does report drift, the
#      fix is `bash scripts/install-agent-skills.sh --generate-only`.
run_step "Plugin skills mirror in sync" \
  bash scripts/install-agent-skills.sh --check-mirror

# 11c. Mirror link integrity — the question 11b structurally cannot ask. The
#      drift gate compares the mirror to the generator's own output, so when the
#      generator copied `../../docs/X.md` verbatim into a directory two levels
#      deeper, both sides carried the same ~90 dead links and 11b was green by
#      construction (#831). This gate resolves each link against the file that
#      contains it, which is a fact about the tree rather than about the copy.
#      Self-test first, same reasoning as 11 and 5b.
run_step "Mirror link gate regression suite" \
  bash scripts/test-mirror-link-check.sh
run_step "Mirror link integrity" python3 scripts/check-mirror-links.py

if [ "$LIST_STEPS" -eq 1 ]; then
  exit 0
fi

# Slowest steps + total (#1217). Printed on pass AND fail: the run you most
# want to profile is often the one that failed at minute twelve.
print_timing_summary() {
  local total=$((SECONDS - GATE_STARTED))
  echo ""
  echo "Wall clock: $((total / 60))m$((total % 60))s total. Slowest steps:"
  local i
  for i in "${!STEP_LABELS[@]}"; do
    printf '%6s  %s\n' "${STEP_SECONDS[$i]}s" "${STEP_LABELS[$i]}"
  done | sort -rn | head -8 | sed 's/^/  /'
}

# Collect every concurrent step before summarising. Placed HERE, at the very
# end, so a grouped step overlaps the entire serial remainder rather than just
# its immediate neighbours — the Go suites (284s combined) run underneath the
# npm, lint and doc steps instead of in front of them.
#
# Nothing may read a grouped step's result before this point, and nothing does:
# the group is read-only by construction, so no serial step downstream depends
# on one having finished.
run_group_wait

echo ""
echo "-------------------------------------------------------------------------"
print_timing_summary

print_skip_summary
print_final_verdict
exit $?
