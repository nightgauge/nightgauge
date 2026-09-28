# Reference: Mobile MCP E2E Tests (Phase 2.4)

Procedural detail for **Phase 2.4 (Mobile MCP E2E Tests)**. Read this when
that phase fires.

### Step 2.4: Run Mobile MCP E2E Tests (Agent-Driven)

**PURPOSE**: Execute mobile-mcp specs against the debug APK on the Android
emulator. This phase drives the real app on a live device via the mobile-mcp
MCP server, exercising UI flows and **data-correctness** assertions (the
workflow that caught the v2.3.0 Sun & Moon UTC bug in acme-tracker).

This is distinct from Step 2.3 (deterministic Playwright/Cypress E2E). Mobile
MCP execution is **agent-driven**: the skill executor reads each spec and calls
mobile-mcp MCP tools — there is no compiled binary.

**Contract**: The repo-under-test owns the stable contract at
`test/mobile_mcp/PIPELINE.md` (discovery glob, per-spec result schema, evidence
dir, pass/fail semantics). This phase implements the runner half: build APK,
boot emulator, install, drive specs, collect evidence, gate.

**Activation**: Only when `test/mobile_mcp/specs/` exists and contains at least
one non-template `.md` spec file. Backend-only repos (no such directory) skip
this phase with zero overhead.

**Config gate** (`validation.mobile_mcp_tests`, default `"strict"`):

- `strict`: failures (or APK-build/emulator failures) block PR creation.
- `best_effort`: failures and environment problems are logged but do not block.
- `skip`: phase is skipped entirely.

**Tool prerequisites**: `flutter`, `adb`, and `emulator` must be on `PATH`. When
any is missing the phase records a `skipped_reason` and — in `strict` mode —
does **not** block (a missing toolchain is an environment gap, not a test
failure). A failing/erroring _spec_ is what blocks in `strict` mode.

#### Step 2.4.0: Detect Mobile MCP Specs and Toolchain

```bash
MOBILE_MCP_RAN=false
MOBILE_MCP_PASSED=false
MOBILE_MCP_SPECS_RUN=0
MOBILE_MCP_SPECS_PASSED=0
MOBILE_MCP_SPECS_FAILED=0
MOBILE_MCP_RESULTS_JSON="[]"
MOBILE_MCP_EVIDENCE_DIR=""
MOBILE_MCP_SKIPPED_REASON=""
MOBILE_MCP_ACTIVE=false

MOBILE_MCP_MODE=$(yq -r '.validation.mobile_mcp_tests // "strict"' .nightgauge/config.yaml 2>/dev/null || echo "strict")
[ -n "${NIGHTGAUGE_VALIDATION_MOBILE_MCP_TESTS:-}" ] && MOBILE_MCP_MODE="$NIGHTGAUGE_VALIDATION_MOBILE_MCP_TESTS"

if [ "$MOBILE_MCP_MODE" = "skip" ]; then
  MOBILE_MCP_SKIPPED_REASON="config: validation.mobile_mcp_tests=skip"
  echo "⏭ Mobile MCP tests skipped (config)"
else
  SPEC_DIR="test/mobile_mcp/specs"
  SPEC_COUNT=$(find "$SPEC_DIR" -name "*.md" ! -name "_template.md" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$SPEC_COUNT" -eq 0 ]; then
    MOBILE_MCP_SKIPPED_REASON="no specs found in $SPEC_DIR"
    echo "⏭ Mobile MCP tests skipped — no specs found"
  else
    # Toolchain presence — a missing tool is an environment gap, never a test failure.
    MISSING_TOOLS=""
    for tool in flutter adb emulator; do
      command -v "$tool" >/dev/null 2>&1 || MISSING_TOOLS="$MISSING_TOOLS $tool"
    done
    if [ -n "$MISSING_TOOLS" ]; then
      MOBILE_MCP_SKIPPED_REASON="missing toolchain:$MISSING_TOOLS"
      echo "⏭ Mobile MCP tests skipped — not on PATH:$MISSING_TOOLS"
    else
      echo "Mobile MCP: found $SPEC_COUNT spec(s) in $SPEC_DIR"
      MOBILE_MCP_ACTIVE=true
    fi
  fi
fi
```

> **Worktree isolation (concurrent slots — #24 AC).** Emulator console/adb ports
> are a shared host resource: two slots booting `Pixel_9_Pro` at once collide.
> The MVP strategy is **sequential serialization** via an advisory lock at
> `~/.nightgauge/mobile-mcp.lock`. Steps 2.4.1–2.4.5 below run inside a
> `flock`-guarded block so only one slot drives the emulator at a time; other
> slots wait (up to 10 min) for the lock. When `flock` is unavailable (macOS
> without `util-linux`), fall back to running unguarded and log a warning — the
> typical single-slot local run is unaffected. (Future Option B: per-slot AVDs
> with distinct ports for true parallelism — see plan #24.)

```bash
MOBILE_MCP_LOCK_FILE="$HOME/.nightgauge/mobile-mcp.lock"
mkdir -p "$(dirname "$MOBILE_MCP_LOCK_FILE")"
MOBILE_MCP_USE_LOCK=false
command -v flock >/dev/null 2>&1 && MOBILE_MCP_USE_LOCK=true
[ "$MOBILE_MCP_USE_LOCK" = "false" ] && echo "⚠ flock unavailable — mobile-mcp runs unguarded (single-slot only)"
```

The remaining steps (2.4.1–2.4.6) execute only when `MOBILE_MCP_ACTIVE=true`.
Acquire the lock once around the device-using region:

```bash
acquire_mobile_lock() {
  [ "$MOBILE_MCP_USE_LOCK" = "true" ] || return 0
  exec 9>"$MOBILE_MCP_LOCK_FILE"
  flock -w 600 9 || { echo "ERROR: could not acquire mobile-mcp lock in 600s"; return 1; }
}
release_mobile_lock() {
  [ "$MOBILE_MCP_USE_LOCK" = "true" ] || return 0
  flock -u 9 2>/dev/null || true
  exec 9>&- 2>/dev/null || true
}
```

#### Step 2.4.1: Build Debug APK

When `MOBILE_MCP_ACTIVE=true`:

```bash
echo "=== Building debug APK for mobile-mcp tests ==="
APK_BUILD_OUTPUT=$(flutter build apk --debug 2>&1)
APK_BUILD_EXIT=$?
APK_PATH=""

if [ $APK_BUILD_EXIT -ne 0 ]; then
  echo "ERROR: Debug APK build failed — mobile-mcp tests cannot run"
  echo "$APK_BUILD_OUTPUT" | tail -30
  MOBILE_MCP_SKIPPED_REASON="apk build failed (exit $APK_BUILD_EXIT)"
  MOBILE_MCP_ACTIVE=false
  if [ "$MOBILE_MCP_MODE" = "strict" ]; then
    VALIDATION_STATUS="failed"
    ERROR_CATEGORY="mobile-apk-build-failed"
  fi
else
  APK_PATH=$(find build/app/outputs/flutter-apk/ -name "*.apk" ! -name "*release*" 2>/dev/null | head -1)
  echo "APK: $APK_PATH"
  [ -z "$APK_PATH" ] && { MOBILE_MCP_SKIPPED_REASON="apk not found after build"; MOBILE_MCP_ACTIVE=false; }
fi
```

> **Note on the APK-build gate.** A failed debug build in `strict` mode sets
> `VALIDATION_STATUS=failed` here but does **not** `exit 1` — control must reach
> Step 2.4.5 (emulator teardown) and Phase 6 (context write) so the failure is
> recorded in `validate-{N}.json`. The early-exit pattern used by the Phase 1.5
> build gate is wrong here because it would orphan a booted emulator.

#### Step 2.4.2: Boot Emulator (under lock)

```bash
EMULATOR_STARTED_BY_SKILL=false
EMULATOR_PID=""

if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  acquire_mobile_lock || { MOBILE_MCP_ACTIVE=false; MOBILE_MCP_SKIPPED_REASON="lock acquisition failed"; }
fi

if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  RUNNING_DEVICES=$(adb devices 2>/dev/null | grep -v "^List" | grep -c "device$" || echo 0)
  if [ "$RUNNING_DEVICES" -eq 0 ]; then
    echo "=== Booting Android emulator: Pixel_9_Pro ==="
    emulator -avd "Pixel_9_Pro" -no-window -no-audio -no-boot-anim &
    EMULATOR_PID=$!
    EMULATOR_STARTED_BY_SKILL=true
    # Declare the child so the runaway monitor can see the boot wait as work
    # rather than a stall (#1488). See _shared/LONG_RUNNING_PROCESSES.md.
    echo "NIGHTGAUGE_PROGRESS: {\"pid\": $EMULATOR_PID, \"label\": \"android emulator boot\"}"

    BOOT_WAIT=0
    until adb shell getprop sys.boot_completed 2>/dev/null | grep -q "^1$"; do
      sleep 5
      BOOT_WAIT=$((BOOT_WAIT + 5))
      if [ $BOOT_WAIT -ge 120 ]; then
        echo "ERROR: Emulator failed to boot in 120s"
        MOBILE_MCP_SKIPPED_REASON="emulator boot timeout"
        MOBILE_MCP_ACTIVE=false
        break
      fi
    done
    [ "$MOBILE_MCP_ACTIVE" = "true" ] && echo "Emulator ready (boot_completed in ${BOOT_WAIT}s)"
  else
    echo "Emulator already running ($RUNNING_DEVICES device(s))"
  fi
fi
```

#### Step 2.4.3: Install APK

```bash
if [ "$MOBILE_MCP_ACTIVE" = "true" ] && [ -n "$APK_PATH" ]; then
  adb install -r "$APK_PATH" 2>&1 || {
    echo "ERROR: APK install failed"
    MOBILE_MCP_SKIPPED_REASON="adb install failed"
    MOBILE_MCP_ACTIVE=false
  }
fi
```

#### Step 2.4.4: Run Specs via Agent

When `MOBILE_MCP_ACTIVE=true`, for each spec in `test/mobile_mcp/specs/*.md`
(excluding `_template.md`), the **skill executor itself** reads the spec and
drives the app via the mobile-mcp MCP server. For each spec the agent MUST:

1. Call `mobile_init` to attach to the booted device.
2. Run the spec's `setup` (handles fresh-install permission dialogs), then each
   numbered step in order, using the helpers named in `test/mobile_mcp/helpers.md`.
3. Evaluate each assertion against `mobile_dump_ui` output (and screenshots for
   visual checks), recording it as `{"id": ..., "status": "pass"|"fail",
"actual": ...}` — **the assertion shape from `test/mobile_mcp/README.md`,
   not an invented one.**
4. Capture screenshots at each checkpoint with `mobile_screenshot`, saving them
   under `test/mobile_mcp/evidence/<spec>/<timestamp>/`.
5. Write the per-spec result block to
   `test/mobile_mcp/evidence/<spec>/<timestamp>/result.json` matching the
   contract result format (keys: `spec`, `platform`, `device`, `status`,
   `assertions`, `screenshots`, `notes`). `status` is `pass` (all assertions
   held), `fail` (an assertion was false), or `error` (spec could not complete).

The bash below prepares evidence directories, then **aggregates** the
`result.json` files the agent writes. It uses a process-substitution loop (not a
pipe) so the counters survive — a `find ... | while read` pipe runs the body in
a subshell and silently discards the incremented counts.

```bash
if [ "$MOBILE_MCP_ACTIVE" = "true" ]; then
  TIMESTAMP=$(date -u +%Y%m%dT%H%M%SZ)
  EVIDENCE_BASE="test/mobile_mcp/evidence"
  mkdir -p "$EVIDENCE_BASE"
  MOBILE_MCP_EVIDENCE_DIR="$EVIDENCE_BASE"
  SPEC_RESULTS_FILE=$(mktemp)
  echo "[]" > "$SPEC_RESULTS_FILE"

  # Pre-create per-spec evidence dirs so the agent has a target to write into.
  while IFS= read -r SPEC_FILE; do
    SPEC_NAME=$(basename "$SPEC_FILE" .md)
    mkdir -p "$EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP"
    echo "=== Spec queued: $SPEC_NAME → $EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP ==="
  done < <(find "test/mobile_mcp/specs" -name "*.md" ! -name "_template.md" | sort)

  # >>> AGENT EXECUTION HAPPENS HERE <<<
  # The skill executor now drives each queued spec via mobile-mcp tools and
  # writes result.json + screenshots into each spec's $TIMESTAMP dir before the
  # aggregation loop below reads them.

  # Aggregate results (process substitution keeps counter mutations).
  while IFS= read -r SPEC_FILE; do
    SPEC_NAME=$(basename "$SPEC_FILE" .md)
    RESULT_JSON_PATH="$EVIDENCE_BASE/$SPEC_NAME/$TIMESTAMP/result.json"

    if [ -f "$RESULT_JSON_PATH" ] && jq -e . "$RESULT_JSON_PATH" >/dev/null 2>&1; then
      SPEC_RESULT=$(jq -c . "$RESULT_JSON_PATH")
      SPEC_STATUS=$(printf '%s\n' "$SPEC_RESULT" | jq -r '.status // "error"')
    else
      SPEC_STATUS="error"
      SPEC_RESULT=$(jq -nc --arg s "$SPEC_NAME" \
        '{spec: $s, status: "error", error: "no valid result.json written by agent"}')
    fi

    MOBILE_MCP_RESULTS_JSON=$(printf '%s\n' "$MOBILE_MCP_RESULTS_JSON" | jq -c ". + [$SPEC_RESULT]")

    if [ "$SPEC_STATUS" = "pass" ]; then
      MOBILE_MCP_SPECS_PASSED=$((MOBILE_MCP_SPECS_PASSED + 1))
    else
      MOBILE_MCP_SPECS_FAILED=$((MOBILE_MCP_SPECS_FAILED + 1))
    fi
    MOBILE_MCP_SPECS_RUN=$((MOBILE_MCP_SPECS_RUN + 1))
    echo "  $SPEC_NAME → $SPEC_STATUS"
  done < <(find "test/mobile_mcp/specs" -name "*.md" ! -name "_template.md" | sort)

  rm -f "$SPEC_RESULTS_FILE"
  MOBILE_MCP_RAN=true
  [ "$MOBILE_MCP_SPECS_FAILED" -eq 0 ] && MOBILE_MCP_PASSED=true || MOBILE_MCP_PASSED=false
fi
```

#### Step 2.4.5: Stop Emulator and Release Lock

```bash
if [ "$EMULATOR_STARTED_BY_SKILL" = "true" ]; then
  echo "=== Stopping emulator (started by this skill) ==="
  adb emu kill 2>/dev/null || { [ -n "$EMULATOR_PID" ] && kill "$EMULATOR_PID" 2>/dev/null; } || true
fi
release_mobile_lock
```

> Teardown runs whenever the skill booted the emulator, including failure paths
> (boot timeout, install failure, spec error). Never leave an orphaned emulator
> or held lock behind — a stuck lock blocks every subsequent slot for 10 min.

#### Step 2.4.6: Gate on Results

```bash
if [ "$MOBILE_MCP_MODE" = "strict" ] && [ "$MOBILE_MCP_RAN" = "true" ] && [ "$MOBILE_MCP_PASSED" = "false" ]; then
  echo "ERROR: Mobile MCP tests failed ($MOBILE_MCP_SPECS_FAILED/$MOBILE_MCP_SPECS_RUN specs failed)"
  echo "Evidence: $MOBILE_MCP_EVIDENCE_DIR"
  VALIDATION_STATUS="failed"
  ERROR_CATEGORY="mobile-mcp-tests-failed"
  # Screenshots are attached to the PR in pr-create from the mobile_mcp block — not here.
fi

echo "Mobile MCP: ran=$MOBILE_MCP_RAN passed=$MOBILE_MCP_PASSED specs=$MOBILE_MCP_SPECS_RUN failed=$MOBILE_MCP_SPECS_FAILED skip_reason='${MOBILE_MCP_SKIPPED_REASON}'"
```

The `MOBILE_MCP_*` variables flow into the `validate-{N}.json` writer
(`context-and-board.md`, Phase 6) as the `mobile_mcp` block, which `pr-create`
reads to attach screenshot evidence to the PR body.
