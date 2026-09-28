# Reference: Dead Code Detection (Phase 1.6)

Procedural detail for **Phase 1.6 (Dead Code Detection)**. Read this when that
phase fires.

## Phase 1.6: Dead Code Detection

**PURPOSE**: Detect potentially dead code patterns that may indicate incomplete
implementations. Behavior is controlled by `validation.dead_code` config
(default: `"gate"`).

### Step 1.6.1: Scope to Changed Files

Only analyze files from `dev-{N}.json` (`FILES_CREATED` and `FILES_MODIFIED`).
Findings in changed files are `severity: "error"` (actionable). Findings in
unchanged files are `severity: "warning"` (logged, non-blocking).

### Step 1.6.2: Detection Rules

Run the following checks against scoped files:

- **VSCode command registration** (`vscode-extension` only): Extract commands
  from `contributes.commands` in package.json, search `src/` for matching
  `registerCommand('command.id')` calls. Flag unregistered commands.
- **Unused export detection** (TypeScript/JavaScript): Find
  `export function/class/const` in `src/`, check if imported elsewhere. Skip
  entry points (`activate`, `deactivate`, `main`, `default`, `index`) and
  `index.ts` re-exports.
- **Command argument safety** (`vscode-extension` only): Check `view/title` and
  `view/item/context` menu command handlers for argument type validation
  (`Array.isArray`, `typeof`, `instanceof`, null checks). Flag handlers with no
  validation.
- **Terminal output detection** (TypeScript/JavaScript): For each new or
  modified `export function/class` that returns a value, check if callers assign
  and use the return value. Flag functions whose return values are called but
  the result is only logged, stored to a file, or discarded. Report as type:
  `"terminal_output"` with actionable message:
  `"[ServiceName].[method]() return value is stored but never consumed — expected consumer: [suggest based on codebase patterns]"`.
- **Orphaned producer detection** (TypeScript/JavaScript): For each new
  `EventEmitter.emit()`, `fire()`, or `_onDid*` event firing in changed files,
  check that at least one non-test subscriber exists in the codebase. For
  services that write to files (e.g., `writeFileSync`, `fs.promises.writeFile`
  to `.nightgauge/` paths), check that at least one reader exists. Report
  as type: `"orphaned_producer"`.

### Step 1.6.3: Gating Decision

After collecting and scoping findings, make the gating decision:

1. Read config `validation.dead_code` (default: `"gate"`)
   - `"gate"` — Fail validation if current-issue dead code found (severity:
     error items exist)
   - `"warn"` — Log warnings only, do not block (backwards-compatible behavior)
   - `"off"` — Skip dead code detection entirely (Phase 1.6 is skipped)
2. If mode is `"gate"` and any `severity: "error"` findings exist:
   - Set `DEAD_CODE_BLOCKED=true`
   - Display actionable error listing each finding with file, line, and
     suggested fix
3. If mode is `"warn"`: record findings but proceed

```bash
DEAD_CODE_MODE=$(yq -r '.validation.dead_code // "gate"' .nightgauge/config.yaml 2>/dev/null || echo "gate")
DEAD_CODE_BLOCKED=false

if [ "$DEAD_CODE_MODE" = "off" ]; then
  echo "⏭ Dead code detection disabled (validation.dead_code=off)"
elif [ "$DEAD_CODE_MODE" = "gate" ]; then
  ERROR_COUNT=$(printf '%s\n' "$DEAD_CODE_JSON" | jq '[.[] | select(.severity == "error")] | length')
  if [ "$ERROR_COUNT" -gt 0 ]; then
    DEAD_CODE_BLOCKED=true
    echo "✗ Dead code gating FAILED: $ERROR_COUNT finding(s) in current-issue files"
    printf '%s\n' "$DEAD_CODE_JSON" | jq -r '.[] | select(.severity == "error") | "  - \(.type): \(.name) at \(.location)"'
  fi
elif [ "$DEAD_CODE_MODE" = "warn" ]; then
  echo "⚠ Dead code findings recorded as warnings (validation.dead_code=warn)"
fi
```

### Step 1.6.4: Integration Check Gating

For `terminal_output` and `orphaned_producer` findings, apply separate gating
controlled by `validation.integration_check` (default: `"warn"`):

```bash
INTEGRATION_CHECK_MODE=$(yq -r '.validation.integration_check // "warn"' .nightgauge/config.yaml 2>/dev/null || echo "warn")

if [ "$INTEGRATION_CHECK_MODE" = "off" ]; then
  echo "⏭ Integration check disabled (validation.integration_check=off)"
elif [ "$INTEGRATION_CHECK_MODE" = "gate" ]; then
  INTEGRATION_ERRORS=$(printf '%s\n' "$DEAD_CODE_JSON" | jq '[.[] | select(.type == "terminal_output" or .type == "orphaned_producer") | select(.severity == "error")] | length')
  if [ "$INTEGRATION_ERRORS" -gt 0 ]; then
    DEAD_CODE_BLOCKED=true
    echo "✗ Integration check FAILED: $INTEGRATION_ERRORS orphaned integration(s)"
    printf '%s\n' "$DEAD_CODE_JSON" | jq -r '.[] | select(.type == "terminal_output" or .type == "orphaned_producer") | select(.severity == "error") | "  - \(.type): \(.name) at \(.location)"'
  fi
elif [ "$INTEGRATION_CHECK_MODE" = "warn" ]; then
  echo "⚠ Integration findings recorded as warnings (validation.integration_check=warn)"
fi
```
