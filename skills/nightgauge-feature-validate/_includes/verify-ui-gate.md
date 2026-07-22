# Reference: Web UI Verification Gate (Phase 2.45)

Procedural detail for **Phase 2.45 (Web UI Verification Gate)**. Read this
when that phase fires.

## Contents

- [Step 2.45.0: Detect UI-Bearing Surface and Registered Flow](#step-2450-detect-ui-bearing-surface-and-registered-flow)
- [Step 2.45.1: Start Dev Server](#step-2451-start-dev-server-if-not-already-reachable)
- [Step 2.45.2: Run the Flow via Agent](#step-2452-run-the-flow-via-agent-chained-skill-invocation)
- [Step 2.45.3: Stop Dev Server](#step-2453-stop-dev-server-if-started-by-this-phase)
- [Step 2.45.4: Gate on Results and Record Gate-Metric](#step-2454-gate-on-results-and-record-gate-metric)

**Contract**: browser-driven counterpart to Phase 2.4's mobile-mcp gate — same
shape (detect → start → drive → gate → teardown), different runtime (a real
browser via the Playwright MCP, chained into `nightgauge-verify-ui`
rather than driven inline). See [docs/GATE_RELAXATION.md](../../../docs/GATE_RELAXATION.md)
for the shared change classifier this phase's trigger reuses, and
[skills/nightgauge-verify-ui/SKILL.md](../../nightgauge-verify-ui/SKILL.md)
for the flow-driving mechanics (console-error and Core Web Vitals assertions
live there, not here).

**Config gate** (`validation.verify_ui_tests`, default `"strict"`):

- `strict`: a failed flow (or a `web_vitals` budget breach) blocks PR creation.
- `best_effort`: failures are logged but do not block.
- `skip`: phase is skipped entirely.

**Activation**: only when the diff touches UI-bearing frontend surface AND a
verify-ui flow is registered for the current repo. Neither condition met →
zero-overhead skip (preserves fast-track economics for docs-only/config-only
and non-UI diffs). First condition met but no flow registered → an **explicit
skip reason** is recorded (never a silent pass) so the coverage gap stays
visible to reviewers.

### Step 2.45.0: Detect UI-Bearing Surface and Registered Flow

```bash
VERIFY_UI_RAN=false
VERIFY_UI_PASSED=false
VERIFY_UI_ACTIVE=false
VERIFY_UI_SKIPPED_REASON=""
VERIFY_UI_FLOW=""
VERIFY_UI_REPO=""
VERIFY_UI_BASE_URL=""
VERIFY_UI_REPORT_JSON="{}"
VERIFY_UI_ARTIFACTS_DIR=""

VERIFY_UI_MODE=$(yq -r '.validation.verify_ui_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo "strict")
[ -n "${NIGHTGAUGE_VALIDATION_VERIFY_UI_TESTS:-}" ] && VERIFY_UI_MODE="$NIGHTGAUGE_VALIDATION_VERIFY_UI_TESTS"

BINARY="${NIGHTGAUGE_BIN:-}"; [ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
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

if [ "$VERIFY_UI_MODE" = "skip" ]; then
  VERIFY_UI_SKIPPED_REASON="config: validation.verify_ui_tests=skip"
  echo "⏭ Web UI verification skipped (config)"
elif [ -z "$BINARY" ]; then
  VERIFY_UI_SKIPPED_REASON="nightgauge binary not found — cannot classify UI surface"
  echo "⏭ Web UI verification skipped — $VERIFY_UI_SKIPPED_REASON"
else
  VERIFY_UI_REPO=$(basename "$(pwd)")
  UI_SURFACE_JSON=$("$BINARY" ci classify-ui-surface --base "origin/${BASE_BRANCH:-main}" --head HEAD --repo "$VERIFY_UI_REPO" --json 2>/dev/null || echo '{"touches_ui_surface":false,"reason":"classifier unavailable"}')
  TOUCHES_UI=$(echo "$UI_SURFACE_JSON" | jq -r '.touches_ui_surface // false')

  if [ "$TOUCHES_UI" != "true" ]; then
    VERIFY_UI_SKIPPED_REASON="$(echo "$UI_SURFACE_JSON" | jq -r '.reason // "diff does not touch UI-bearing surface"')"
    echo "⏭ Web UI verification skipped — $VERIFY_UI_SKIPPED_REASON"
  else
    # Repo -> flow registry. Onboarding a new UI-bearing repo needs BOTH a new
    # skills/nightgauge-verify-ui/flows/<name>.md AND an entry here (see
    # that skill's "Flows" section and its reference flow's "Notes").
    case "$VERIFY_UI_REPO" in
      acme-dashboard)
        VERIFY_UI_FLOW="dashboard-auth"
        VERIFY_UI_BASE_URL="http://localhost:5173"
        VERIFY_UI_DEV_CMD="npm run dev"
        ;;
      *)
        VERIFY_UI_FLOW=""
        ;;
    esac

    if [ -z "$VERIFY_UI_FLOW" ]; then
      VERIFY_UI_SKIPPED_REASON="diff touches UI-bearing surface in '$VERIFY_UI_REPO' but no verify-ui flow is registered for it — coverage gap, not a pass (add flows/<name>.md and register it in verify-ui-gate.md)"
      echo "⚠ Web UI verification: $VERIFY_UI_SKIPPED_REASON"
    else
      VERIFY_UI_ACTIVE=true
      echo "Web UI verification: repo=$VERIFY_UI_REPO flow=$VERIFY_UI_FLOW"
    fi
  fi
fi
```

### Step 2.45.1: Start Dev Server (if not already reachable)

```bash
DEV_SERVER_PID=""
DEV_SERVER_STARTED_BY_SKILL=false

if [ "$VERIFY_UI_ACTIVE" = "true" ]; then
  if curl -fsS "$VERIFY_UI_BASE_URL" >/dev/null 2>&1; then
    echo "Dev server already reachable at $VERIFY_UI_BASE_URL"
  else
    echo "=== Starting dev server: $VERIFY_UI_DEV_CMD ==="
    nohup $VERIFY_UI_DEV_CMD > /tmp/verify-ui-dev-server-${ISSUE_NUMBER}.log 2>&1 &
    DEV_SERVER_PID=$!
    DEV_SERVER_STARTED_BY_SKILL=true

    BOOT_WAIT=0
    until curl -fsS "$VERIFY_UI_BASE_URL" >/dev/null 2>&1; do
      sleep 2
      BOOT_WAIT=$((BOOT_WAIT + 2))
      if [ $BOOT_WAIT -ge 60 ]; then
        echo "ERROR: dev server did not become reachable within 60s"
        VERIFY_UI_SKIPPED_REASON="dev server failed to start within 60s ($VERIFY_UI_DEV_CMD)"
        VERIFY_UI_ACTIVE=false
        break
      fi
    done
    [ "$VERIFY_UI_ACTIVE" = "true" ] && echo "Dev server ready at $VERIFY_UI_BASE_URL (boot in ${BOOT_WAIT}s)"
  fi
fi
```

> **Note on the dev-server-start gate.** Mirrors the mobile-mcp APK-build note
> (Step 2.4.1): a failed start does NOT `exit 1` here — control must still
> reach Step 2.45.3 (teardown) so a server this phase started is never
> orphaned.

### Step 2.45.2: Run the Flow via Agent (chained `Skill()` invocation)

When `VERIFY_UI_ACTIVE=true`, the skill executor itself (not a script) invokes
the verify-ui skill as a chained skill call:

```
Skill(skill="nightgauge:verify-ui", args="$VERIFY_UI_FLOW --url $VERIFY_UI_BASE_URL")
```

This is a documented skill-to-skill chain, not a spontaneous model action —
`nightgauge-verify-ui` carries `metadata.chainable: true` specifically so
this call is not blocked by `disable-model-invocation` (#4194). After the
call returns, read the report it wrote:

```bash
if [ "$VERIFY_UI_ACTIVE" = "true" ]; then
  VERIFY_UI_ARTIFACTS_DIR=$(find .nightgauge/verify -maxdepth 1 -type d -name "${VERIFY_UI_FLOW}-*" 2>/dev/null | sort | tail -1)
  REPORT_PATH="$VERIFY_UI_ARTIFACTS_DIR/report.json"

  if [ -n "$VERIFY_UI_ARTIFACTS_DIR" ] && [ -f "$REPORT_PATH" ] && jq -e . "$REPORT_PATH" >/dev/null 2>&1; then
    VERIFY_UI_REPORT_JSON=$(jq -c . "$REPORT_PATH")
  else
    VERIFY_UI_REPORT_JSON=$(jq -nc --arg f "$VERIFY_UI_FLOW" '{flow: $f, status: "error", error: "no valid report.json written by verify-ui"}')
  fi
fi
```

### Step 2.45.3: Stop Dev Server (if started by this phase)

```bash
if [ "$DEV_SERVER_STARTED_BY_SKILL" = "true" ] && [ -n "$DEV_SERVER_PID" ]; then
  echo "=== Stopping dev server (started by this phase) ==="
  kill "$DEV_SERVER_PID" 2>/dev/null || true
fi
```

> Teardown runs whenever this phase started the dev server, including failure
> paths (boot timeout, flow error). Never leave an orphaned dev server behind.

### Step 2.45.4: Gate on Results and Record Gate-Metric

```bash
if [ "$VERIFY_UI_ACTIVE" = "true" ]; then
  VERIFY_UI_RAN=true
  VERIFY_UI_STATUS=$(echo "$VERIFY_UI_REPORT_JSON" | jq -r '.status // "error"')
  [ "$VERIFY_UI_STATUS" = "passed" ] && VERIFY_UI_PASSED=true || VERIFY_UI_PASSED=false

  if [ "$VERIFY_UI_PASSED" = "true" ]; then
    "$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate verify-ui --result pass
  else
    FIRST_FAIL=$(echo "$VERIFY_UI_REPORT_JSON" | jq -r '([.steps[]? | select(.status != "passed")] | .[0].name) // .error // "unknown"')
    "$BINARY" gate record-metric --issue "$ISSUE_NUMBER" --gate verify-ui \
      --result catch --error-summary "verify-ui flow '$VERIFY_UI_FLOW' failed: $FIRST_FAIL"
    if [ "$VERIFY_UI_MODE" = "strict" ]; then
      VALIDATION_STATUS="failed"
      ERROR_CATEGORY="verify-ui-gate-failed"
    fi
  fi
fi

echo "Web UI verification: ran=$VERIFY_UI_RAN passed=$VERIFY_UI_PASSED repo='$VERIFY_UI_REPO' flow='$VERIFY_UI_FLOW' skip_reason='${VERIFY_UI_SKIPPED_REASON}'"
```

Only a `pass`/`catch` gate-metric record is written to `gate-metrics.jsonl`
when the gate actually ran — matching `AppendGateMetric`'s validated result
values (`"pass"` or `"catch"` only, see `internal/state/gate_metrics_writer.go`).
When the gate is skipped (either condition in Step 2.45.0), no record is
written; `FeatureValidateGate` never inspects a gate name whose stage never
emitted a record, so an absent record is not silently treated as a pass — the
skip reason's visibility comes from the `verify_ui` block in
`validate-{N}.json` (Phase 6, `context-and-board.md`), not from gate-metrics.

The `VERIFY_UI_*` variables flow into the `validate-{N}.json` writer
(`context-and-board.md`, Phase 6) as the `verify_ui` block.
