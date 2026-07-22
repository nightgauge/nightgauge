# Issue Pickup — Phases 2.5, 2.7, 2.8, 2.9 (signal start + size/baseline/dependency gates)

This reference file contains the procedural detail for four adjacent phases of
the issue-pickup stage:

- **Phase 2.5** — Signal Stage Start (`signal-stage-start`, index 2)
- **Phase 2.7** — Size Gate Preflight (`size-gate-preflight`, index 3)
- **Phase 2.8** — Baseline-CI Dependency Gate (`baseline-ci-gate`, index 4)
- **Phase 2.9** — Native blockedBy Dependency Gate (`blocked-dependency-gate`, index 5)

## Contents

- [Phase 2.5: Signal Stage Start](#phase-25-signal-stage-start)
- [Phase 2.7: Size Gate Preflight](#phase-27-size-gate-preflight)
- [Phase 2.8: Baseline-CI Dependency Gate](#phase-28-baseline-ci-dependency-gate)
- [Phase 2.9: Native blockedBy Dependency Gate](#phase-29-native-blockedby-dependency-gate)

---

## Phase 2.5: Signal Stage Start

**PURPOSE**: Update state.json to indicate this stage has started.

**IMPORTANT**: This phase runs AFTER issue selection because we need the
`$ISSUE_NUMBER` variable.

```bash
# Go binary: project move-status
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

---

## Phase 2.7: Size Gate Preflight

**PURPOSE**: Reject or soft-route issues that exceed pipeline size thresholds
before committing resources to branch creation and analysis. Runs after issue
selection so `$ISSUE_NUMBER` is always available.

### Step 2.7.1: Read Configuration

```bash
CONFIG_PATH=".nightgauge/config.yaml"
GATE_ENABLED="true"

if [ -f "$CONFIG_PATH" ]; then
  # Check if size gate is explicitly disabled
  GATE_ENABLED_RAW=$(grep -A2 'size_gate:' "$CONFIG_PATH" | grep 'enabled:' | awk '{print $2}' | head -1)
  if [ "$GATE_ENABLED_RAW" = "false" ]; then
    GATE_ENABLED="false"
  fi
fi

if [ "$GATE_ENABLED" != "true" ]; then
  echo "Size gate: disabled in config — skipping"
  # Continue to Phase 3
fi
```

### Step 2.7.2: Evaluate Issue Size via Go Binary

When `GATE_ENABLED=true` and `ISSUE_NUMBER` is set:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

if [ -z "$BINARY" ]; then
  echo "Size gate: nightgauge binary not found — skipping gate check"
  # Continue to Phase 3 (graceful degradation)
else
  GATE_OUTPUT=$("$BINARY" size-gate check \
    --issue "$ISSUE_NUMBER" \
    --config "${CONFIG_PATH:-.nightgauge/config.yaml}" 2>&1)
  GATE_EXIT=$?

  if [ $GATE_EXIT -ne 0 ]; then
    # Gate rejected the issue
    echo "Size gate: REJECTED"
    echo "$GATE_OUTPUT"

    # Record outcome for learning loop visibility
    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "failed" \
      --reason "issue-too-large: $GATE_OUTPUT" 2>/dev/null || true

    echo ""
    echo "To unblock: decompose this issue into smaller sub-issues and link them"
    echo "using the GitHub sub-issue API, then retry /nightgauge-issue-pickup"

    exit 1
  fi

  echo "Size gate: PASSED"
fi
```

### Step 2.7.3: Soft-Route Option

When `pipeline.size_gate.routes.reject_action = "soft-route"` in config, the Go
binary (`size-gate check`) exits 0 and the pipeline continues. The model
downgrade to haiku is communicated via `NIGHTGAUGE_PIPELINE_FORCE_MODEL`
if configured by the user in their environment.

**Default behavior (`reject_action: fail`)**: Exit 1 stops the pipeline.

---

## Phase 2.8: Baseline-CI Dependency Gate

**PURPOSE**: Defer dispatch of issues whose acceptance criteria require
promoting a CI check on `main` when `main`'s recent runs of that check are
failing. Runs after Phase 2.7 (size-gate) so size rejections short-circuit
before the network call.

See [docs/FAILURE_TAXONOMY.md](../../../docs/FAILURE_TAXONOMY.md) for the
`[baseline-ci-deferred]` infrastructure pattern and Issue #3004 for the full
design rationale.

### Step 2.8.1: Read Configuration

```bash
GATE_ENABLED="true"
if [ -f "$CONFIG_PATH" ]; then
  GATE_ENABLED_RAW=$(grep -A2 'baseline_ci_gate:' "$CONFIG_PATH" | grep 'enabled:' | awk '{print $2}' | head -1)
  if [ "$GATE_ENABLED_RAW" = "false" ]; then
    GATE_ENABLED="false"
  fi
fi

if [ "$GATE_ENABLED" != "true" ]; then
  echo "Baseline-CI gate: disabled in config — skipping"
fi
```

### Step 2.8.2: Evaluate Issue via Go Binary

When `GATE_ENABLED=true` and `ISSUE_NUMBER` is set:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

if [ -z "$BINARY" ]; then
  echo "Baseline-CI gate: nightgauge binary not found — skipping"
else
  GATE_OUTPUT=$("$BINARY" baseline-gate check \
    --issue "$ISSUE_NUMBER" \
    --config "${CONFIG_PATH:-.nightgauge/config.yaml}" \
    --json 2>&1)
  GATE_EXIT=$?

  if [ $GATE_EXIT -eq 1 ]; then
    # Defer: parse JSON for evidence and post deferral comment.
    WORKFLOW=$(echo "$GATE_OUTPUT" | jq -r '.workflow // "unknown"' 2>/dev/null)
    JOB=$(echo "$GATE_OUTPUT" | jq -r '.job // empty' 2>/dev/null)
    FAILED=$(echo "$GATE_OUTPUT" | jq -r '.failed_runs // 0' 2>/dev/null)
    SAMPLED=$(echo "$GATE_OUTPUT" | jq -r '.sampled_runs // 0' 2>/dev/null)
    REASON=$(echo "$GATE_OUTPUT" | jq -r '.reason // ""' 2>/dev/null)

    JOB_LINE=""
    [ -n "$JOB" ] && JOB_LINE=" job=\`$JOB\`"

    COMMENT_BODY="## Baseline-CI Dependency Gate — Deferred

This issue's acceptance criteria require promoting a CI check on \`main\`,
but \`main\`'s recent runs are currently red:

- **Workflow**: \`$WORKFLOW\`$JOB_LINE
- **Failed**: $FAILED of last $SAMPLED runs
- **Reason**: $REASON

The pipeline has paused this item ([\`baseline-ci-deferred\`](../../docs/FAILURE_TAXONOMY.md#infrastructure))
and will automatically resume dispatch when the baseline goes green (daily
\`baseline-defer-sweep\` cron). No operator action required."

    nightgauge forge issue comment --subject-id "$ISSUE_NUMBER" -b "$COMMENT_BODY" 2>/dev/null || \
      echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "deferred" \
      --reason "[baseline-ci-deferred] $WORKFLOW $JOB failed $FAILED/$SAMPLED" 2>/dev/null || true

    echo "Baseline gate: DEFERRED"
    echo "signal=deferred"
    exit 0
  fi

  if [ $GATE_EXIT -eq 0 ]; then
    echo "Baseline gate: PASSED"
  else
    # Exit 2 = config/IO error — log and continue (best-effort, never over-defer).
    echo "warning: baseline-gate check failed with exit $GATE_EXIT, continuing"
  fi
fi
```

### Step 2.8.3: Short-Circuit Behavior

When the gate decides to defer:

- The Go binary inserts/updates the queue item as `paused` with
  `pausedReason.kind = "baseline_ci_red"` (queue schema 2.2; see
  `packages/nightgauge-vscode/src/types/queue.ts`).
- `signal=deferred` is printed to stdout so the orchestrator short-circuits
  remaining stages, mirroring the AC reconciliation `signal=verify-and-close`
  pattern from feature-planning.
- The daily `.github/workflows/baseline-defer-sweep.yml` cron re-evaluates
  every paused-baseline-CI item and resumes those whose last
  `green_threshold` (default 2) consecutive runs on `main` are all `success`.

When the gate cannot extract a workflow path from the AC text (decision
`unparseable`), exit code is 0 and dispatch proceeds — best-effort design per
ADR-003 (do not over-defer).

---

## Phase 2.9: Native blockedBy Dependency Gate

**PURPOSE**: Defer pickup of issues that have an OPEN native `blockedBy`
dependency (the blocker's PR is not merged). This is the deterministic sibling
of the baseline-CI gate (Phase 2.8): a controlled hold, **not** a pipeline
failure. Runs after Phase 2.8 so both preflight holds resolve before issue
analysis.

See [docs/FAILURE_TAXONOMY.md](../../../docs/FAILURE_TAXONOMY.md) for the
`[blocked-dependency]` infrastructure pattern and Issue #231 for the design
rationale.

### Step 2.9.1: Read Configuration

```bash
DEP_ENABLED="true"
DEP_MODE="warn"
if [ -f "$CONFIG_PATH" ]; then
  DEP_ENABLED=$(yq -r '.enforcement.dependencies.enabled // "true"' "$CONFIG_PATH" 2>/dev/null || echo "true")
  DEP_MODE=$(yq -r '.enforcement.dependencies.mode // "warn"' "$CONFIG_PATH" 2>/dev/null || echo "warn")
fi

# The deterministic deferral gate only runs in `block` mode. `warn`/`ignore`
# stay interactive and are handled by the DEPENDENCY_CHECKING include in Phase 3.
if [ "$DEP_ENABLED" != "true" ] || [ "$DEP_MODE" != "block" ]; then
  echo "Dependency gate: skipped (mode=$DEP_MODE)"
fi
```

### Step 2.9.2: Evaluate Issue via Go Binary

When `DEP_ENABLED=true`, `DEP_MODE=block`, and `ISSUE_NUMBER` is set:

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

if [ -z "$BINARY" ]; then
  echo "Dependency gate: nightgauge binary not found — skipping"
else
  GATE_OUTPUT=$("$BINARY" deps-gate check --issue "$ISSUE_NUMBER" --json 2>&1)
  GATE_EXIT=$?

  if [ $GATE_EXIT -eq 1 ]; then
    # Defer: parse the open blockers and post a deferral comment.
    BLOCKERS=$(echo "$GATE_OUTPUT" | jq -r '.open_dependencies[]? | "- #\(.number) \(.title)"' 2>/dev/null)
    REASON=$(echo "$GATE_OUTPUT" | jq -r '.reason // "blocked by open dependency"' 2>/dev/null)

    COMMENT_BODY="## Dependency Gate — Deferred

This issue has an open \`blockedBy\` dependency (the blocker's PR is not merged),
so pickup is deferred ([\`blocked-dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure)):

$BLOCKERS

The pipeline has paused this item ([\`blocked_dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure))
and will automatically resume dispatch when the blockers close
(\`deps-gate promote\` sweep, or the autonomous cascade). No operator action required."

    nightgauge forge issue comment --subject-id "$ISSUE_NUMBER" -b "$COMMENT_BODY" 2>/dev/null || \
      echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "deferred" \
      --reason "[blocked-dependency] $REASON" 2>/dev/null || true

    echo "Dependency gate: DEFERRED"
    echo "signal=deferred"
    exit 0
  fi

  if [ $GATE_EXIT -eq 0 ]; then
    echo "Dependency gate: PASSED"
  else
    # Exit 2 = config/IO error — log and continue (best-effort, never over-defer).
    echo "warning: deps-gate check failed with exit $GATE_EXIT, continuing"
  fi
fi
```

### Step 2.9.3: Short-Circuit Behavior

When the gate decides to defer:

- The Go binary inserts/updates the queue item as `paused` with
  `pausedReason.kind = "blocked_dependency"` and `blockingIssues` (queue schema
  2.3; see `packages/nightgauge-vscode/src/types/queue.ts`).
- `signal=deferred` is printed to stdout so the orchestrator short-circuits
  remaining stages, exactly mirroring Phase 2.8.
- `deps-gate promote` (run on a sweep, and the autonomous cascade on blocker
  completion) re-evaluates every paused `blocked_dependency` item and resumes
  those whose blockers have all closed.

`warn` and `ignore` dependency modes are unchanged — they stay interactive and
are handled by the `DEPENDENCY_CHECKING` include in Phase 3.
