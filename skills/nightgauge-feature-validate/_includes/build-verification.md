# Reference: Build Verification (Phase 1.5)

Procedural detail for **Phase 1.5 (Run Build Verification)**. Read this when
that phase fires.

## Phase 1.5: Run Build Verification

**PURPOSE**: Ensure the code compiles/builds successfully before any other
validation. This is a **hard gate** — if the build fails, validation fails with
`errorCategory: "build-failed"` and a captured tail of stderr.

> **CRITICAL**: When the build IS run, it MUST pass regardless of
> `--skip-manual`, `--auto-pass`, or any other flags. A failing build should
> NEVER pass validation.

### Step 1.5.0: Check Dev Context and Skip If Passed

```bash
BUILD_CMD="" BUILD_RAN=false BUILD_PASSED=false BUILD_SKIPPED_REASON=""

if [ "$DEV_BUILD_RAN" = "true" ] && [ "$DEV_BUILD_STATUS" = "passed" ]; then
  BUILD_PASSED=true
  BUILD_SKIPPED_REASON="build verified by feature-dev"
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "build_verification", "reason": "build verified by feature-dev (dev context build_verification.status=passed)"}]')
fi
```

### Step 1.5.1: Detect, Run, and Gate (if not skipped)

If not skipped, detect build command from project manifests: `package.json`
(build/compile/tsc scripts), `tsconfig.json` (npx tsc --noEmit),
`pyproject.toml`, `go.mod`, `Cargo.toml`, or `pubspec.yaml`.

**Flutter/Dart projects** (`pubspec.yaml` detected): Run `dart fix --apply`
first (auto-fixes deprecated APIs and type mismatches), then `dart analyze`
as the build gate. This matches what CI runs and catches type errors that
`flutter test` alone misses.

```bash
if [ -f pubspec.yaml ]; then
  echo "Flutter/Dart project detected — running dart fix + analyze"
  dart fix --apply 2>&1 || echo "dart fix non-zero (non-fatal)"
  BUILD_CMD="dart analyze"
fi
```

Then run and gate:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BUILD_GATE_START_MS=$(date +%s%3N)
SELF_HEALED=false
SDK_REBUILD_ATTEMPTED=false
BUILD_DURATION_MS=0
BUILD_EXIT_CODE=0

if [ -z "$BUILD_SKIPPED_REASON" ] && [ -n "$BUILD_CMD" ]; then
  BUILD_OUTPUT=$($BUILD_CMD 2>&1); BUILD_EXIT_CODE=$?; BUILD_RAN=true
  BUILD_GATE_END_MS=$(date +%s%3N)
  BUILD_DURATION_MS=$((BUILD_GATE_END_MS - BUILD_GATE_START_MS))
  [ $BUILD_EXIT_CODE -eq 0 ] && BUILD_PASSED=true || BUILD_PASSED=false
fi

# Build failure is a HARD GATE — cannot be bypassed by --auto-pass or --skip-manual.
# Exception: stale SDK dist is auto-recoverable (run SDK build, retry once).
if [ "$BUILD_RAN" = "true" ] && [ "$BUILD_PASSED" = "false" ]; then
  if printf '%s\n' "$BUILD_OUTPUT" | grep -q "RECOVERABLE: stale_sdk_dist\|SDK dist/index.js not found\|SDK dist is stale"; then
    if [ "$SDK_REBUILD_ATTEMPTED" = "false" ]; then
      SDK_REBUILD_ATTEMPTED=true
      echo "=== Auto-healing: stale SDK dist detected — rebuilding SDK ==="
      SDK_BUILD_OUTPUT=$(npm run -w @nightgauge/sdk build 2>&1)
      SDK_BUILD_EXIT=$?
      if [ $SDK_BUILD_EXIT -ne 0 ]; then
        echo "ERROR: SDK rebuild failed — root cause below. Aborting."
        echo "$SDK_BUILD_OUTPUT"
        echo "BUILD FAILED - VALIDATION CANNOT CONTINUE"; exit 1
      fi
      echo "=== SDK rebuilt successfully — retrying extension build ==="
      BUILD_OUTPUT=$($BUILD_CMD 2>&1); BUILD_EXIT_CODE=$?
      if [ $BUILD_EXIT_CODE -eq 0 ]; then
        BUILD_PASSED=true
        SELF_HEALED=true
        echo "=== Auto-heal successful: SDK rebuild + extension rebuild passed ==="
      else
        echo "BUILD FAILED (after SDK auto-heal) - VALIDATION CANNOT CONTINUE"
        echo "$BUILD_OUTPUT"
        exit 1
      fi
    fi
  fi

  if [ "$BUILD_PASSED" = "false" ]; then
    echo "BUILD FAILED - VALIDATION CANNOT CONTINUE"
    ERROR_CATEGORY="build-failed"
    STDERR_TAIL=$(echo "$BUILD_OUTPUT" | tail -100)
    echo "$BUILD_OUTPUT"
    exit 1
  fi
fi

# Minimum duration check — detect suspiciously fast builds that may indicate
# the deterministic build didn't actually run (LLM rubber stamp, issue 3041).
MINIMUM_DURATION_FLAGGED=false
MINIMUM_DURATION_ACTUAL_MS=${BUILD_DURATION_MS:-0}
MINIMUM_DURATION_P10_MS=0
MINIMUM_DURATION_WARNING=""

if [ "$BUILD_RAN" = "true" ] && [ "$BUILD_PASSED" = "true" ]; then
  # Read p10 baseline from config, fall back to language defaults
  P10_FROM_CONFIG=$(yq -r '.performance.build_time_p10_ms // 0' .nightgauge/config.yaml 2>/dev/null || echo "0")

  if [ "$P10_FROM_CONFIG" -gt 0 ]; then
    MINIMUM_DURATION_P10_MS="$P10_FROM_CONFIG"
  elif [ -f go.mod ]; then
    MINIMUM_DURATION_P10_MS=10000   # Go: 10s default
  elif [ -f package.json ]; then
    MINIMUM_DURATION_P10_MS=15000   # Node.js monorepo: 15s default
  elif [ -f pubspec.yaml ]; then
    MINIMUM_DURATION_P10_MS=20000   # Flutter: 20s default
  fi

  if [ "$MINIMUM_DURATION_P10_MS" -gt 0 ] && \
     [ "$MINIMUM_DURATION_ACTUAL_MS" -lt "$MINIMUM_DURATION_P10_MS" ]; then
    MINIMUM_DURATION_FLAGGED=true
    MINIMUM_DURATION_WARNING="Build completed in ${MINIMUM_DURATION_ACTUAL_MS}ms, but p10 baseline is ${MINIMUM_DURATION_P10_MS}ms — verify the build command actually ran."
    echo "⚠ Minimum duration check FLAGGED: build completed in ${MINIMUM_DURATION_ACTUAL_MS}ms, but p10 baseline is ${MINIMUM_DURATION_P10_MS}ms"
    echo "  This may indicate the deterministic build did not actually run."
    echo "  Ensure your build command is correct in .nightgauge/config.yaml"
  fi
fi

# Record self-heal event if auto-heal occurred (best-effort — never block on this)
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
if [ "$SELF_HEALED" = "true" ] && [ -n "$BINARY" ]; then
  "$BINARY" outcome record-self-heal \
    --issue "$ISSUE_NUMBER" \
    --category "stale_sdk_dist" \
    --stage "feature-validate" 2>/dev/null || true
fi

# Gate metrics recorded inline in validate context (write-validate-context)
```
