<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

## Batch Mode

**Batch mode is additive.** Every stage has a single-issue path that is the
default and is never modified by this section. A batch context file is an
_optional overlay_: when the file this stage looks for is absent — the normal
case — continue the single-issue path unchanged and do not mention batch mode
again. Never invent, guess at, or synthesize a batch context file that is not
on disk.

### The per-stage contract

| Stage              | Batch input          | Batch output        |
| ------------------ | -------------------- | ------------------- |
| `feature-validate` | `dev-batch-{E}.json` | `validate-{E}.json` |

All paths are relative to `.nightgauge/pipeline/`.

- **Every gate still runs.** Batch mode changes how many issues a run covers,
  never which validations execute. Build, tests, and stage gates run once over
  the combined change set — they are not skipped or sampled.

Full contract, detection block and invariants: Read `skills/_shared/BATCH_MODE.md` when `dev-batch-{E}.json` exists.

# Feature Validation (compact)

> Compact render profile (ADR 023 §Q5, #1663). Keeps every phase marker, every
> gate, the Exit Contract and `validate-{N}.json` contract, the honesty and
> no-flaky-dismissal rules and the verify-ui blocking rules; the shared
> preflight, freshness, long-running-process and self-assessment procedures
> are on-demand `Read` directives instead of inlined text. Content here MUST
> stay a strict subset of the base SKILL.md's own wording — this file trims,
> it never invents new instructions.

Trusts the dev context handoff (no build/unit-test/security re-runs that already passed), runs integration/E2E tests with Ralph Loop self-healing (up to 3 auto-fix attempts, [docs/RALPH_LOOP.md](../../../docs/RALPH_LOOP.md)), excludes pre-existing failures via baseline comparison, and writes validation context for `/nightgauge-pr-create`.

**Invoke**: `/nightgauge-feature-validate` (Claude Code plugin), `$nightgauge-feature-validate` (Codex), or via Agent Skills (Copilot/Cursor). **Requires**: `.nightgauge/pipeline/dev-{N}.json` from `/nightgauge-feature-dev`, on the feature branch from `/nightgauge-issue-pickup` (schema: [docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md)). **Config**: `.nightgauge/config.yaml` ([docs/CONFIGURATION.md](../../../docs/CONFIGURATION.md)); per-key defaults and env overrides in `_includes/configuration.md` (read when needed).

## Arguments

`--skip-manual` (skip manual testing prompts), `--e2e-only`, `--checklist-only`, `--auto-pass` (auto-pass all checklist items); no flag runs all checks.

## Exit Contract — Read This First

**This stage is NOT complete until `.nightgauge/pipeline/validate-{N}.json` exists on disk.** On any error, budget exhaustion, or bail-out, STILL execute Phase 6 and write it (`validation_status: "failed"` plus the matching `errorCategory` — enum: `build-failed`, `tests-failed`, `integration-failed`, `dead-code-blocked`, `mobile-apk-build-failed`, `mobile-mcp-tests-failed`, `verify-ui-gate-failed`). Exiting without it triggers a repo-blind orchestrator fallback that may misreport tests. The very last act before signaling completion MUST be:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
test -s ".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json" || \
  { echo "ERROR: validate-${ISSUE_NUMBER}.json missing — Phase 6 was skipped" >&2; exit 1; }
```

## Spike Issues (`type:spike`)

For `type:spike` issues, run `nightgauge spike materialize "$ISSUE_NUMBER" --dry-run`. Non-zero exit = the `docs/spikes/<N>-*.md` artifact is missing or its recommendations block fails schema validation — a **blocking** validation failure ([docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md)).

## Orchestration

Validation is an ordered pipeline — build (1.5) → tests (2) → CI-parity (2.5) — closed by an adversarial **gate** judge whose verdict is the evidence the Go `FeatureValidateGate.Verify()` loop consumes: a failed verdict fails validation. `gate_metrics[]` flows into the validate context (Phase 6); FeatureValidateGate treats zero records as a no-op failure. Full orchestration and supporting-file index: Read `skills/nightgauge-feature-validate/SKILL.md` when needed.

## Gotchas

- **Exit Contract**: write `validate-{N}.json` before exiting — even on failure. The orchestrator fallback is repo-blind.
- **Build hard gate**: when the build runs it MUST pass — no flag (`--auto-pass` included) bypasses it; unit tests alone miss build breaks.
- **Failed validation** → do NOT commit or push; leave the tree for triage.
- **Never skip the Phase 5 commit because `git log` shows a similar commit** — that commit is a _previous issue's_ (feature-dev never commits, #1608); skipping on a branch with zero commits ahead of base loses the entire implementation when the worktree is pruned. Only valid skip evidence: `git rev-list --count origin/<base>..HEAD` > 0.
- **Honesty rule — record every gate result as observed.** Never turn a catch into a pass by weakening the check: no lint-disable comments, skipped or deleted tests, loosened assertions or edited gate config. Fixing the code so it is genuinely correct is allowed (Ralph Loop); laundering the finding is not, and `validate-{N}.json` reports what actually ran.
- **Never dismiss a failing test as flaky without root-causing it.** Re-running until green is not a fix; a failure you cannot explain is recorded as a failure.
- **Env vars do NOT persist across Bash invocations** — re-derive `VALIDATION_STATUS` (and other gate inputs) inside the same cell that uses them; a stale/empty value spuriously skips the commit phase.
- See also the cross-cutting gotchas: Read `skills/_shared/GOTCHAS.md`.

## Workflow

**Phase markers**: at the start of each phase, emit `<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-validate" -->` as an HTML comment on its own line, BEFORE any other output.

**SKIP_TO_PHASE**: if set, skip all phases up to and including the named phase (case-insensitive, kebab-case) and resume from the next one.

### Phase -1: Validate Environment

**Read `skills/_shared/PREFLIGHT.md` now and follow it before continuing.**

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=23 stage="feature-validate" -->\n'
```

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

### Phase 0: Read Dev Context

```bash
printf '<!-- phase:start name="read-dev-context" index=1 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Extract the issue number from the branch; load `.nightgauge/pipeline/dev-{N}.json`. Missing file → ask git ground truth via `gate verify feature-dev` (#134); proceed against the working tree if git finds changes, otherwise exit 1.

### Phase 0.5: Batch Dev Context Detection

```bash
printf '<!-- phase:start name="batch-detection" index=2 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** If `dev-batch-{E}.json` exists, route to consolidated validation — build and test once for all changes.

### Phase 0.6: AC Completion Check (type:docs)

```bash
printf '<!-- phase:start name="ac-completion-check" index=3 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** `type:docs` issues only (no-op otherwise): set `AC_CHECK_REQUIRED=true` when the label is present, else `AC_CHECK_REQUIRED=false` and `AC_CHECK_SKIP=true`. Steps 0.6.2 (run ac-check), 0.6.2b (substantiate unchecked criteria against the diff and mark what the change demonstrably satisfies, #1233) and 0.6.3 (gate on result) live in the include — run all three; criteria that cannot be substantiated remain unchecked → exit 1 naming them.

### Phase 1: Detect Testing Environment (Deterministic)

```bash
printf '<!-- phase:start name="detect-testing-environment" index=4 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/test-setup.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Identify available frameworks and project type; no tests configured → proceed with manual checklist only.

### Phase 1.8: PTC Detection and Execution (CONDITIONAL)

```bash
printf '<!-- phase:start name="ptc-detection" index=5 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/test-setup.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** If Programmatic Tool Calling is available, run validation through a single PTC session instead of individual Bash calls.

### Phase 1.4: Base Branch Freshness Check

```bash
printf '<!-- phase:start name="freshness-check" index=6 total=23 stage="feature-validate" -->\n'
```

**Read `skills/_shared/FRESHNESS_CHECK.md` now and follow it before continuing this phase.**

Best-effort: if `FRESHNESS_CHECK_FAILED=true`, log a warning and continue — pr-merge resolves conflicts later.

### Phase 1.5: Run Build Verification (CONDITIONAL)

```bash
printf '<!-- phase:start name="build-verification" index=7 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/build-and-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** **Hard gate** (see Gotchas): build failure fails validation with `errorCategory: "build-failed"` and a captured stderr tail.

### Phase 1.6: Dead Code Detection (CONFIGURABLE GATE)

```bash
printf '<!-- phase:start name="dead-code-detection" index=8 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/build-and-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** When `validation.dead_code=gate` (default), current-issue error-severity dead-code findings block validation.

### Phase 1.7: Baseline Comparison for Test Failures

```bash
printf '<!-- phase:start name="baseline-comparison" index=9 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/build-and-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Identify pre-existing failures (already failing on main) so the Ralph Loop skips them; runs ONLY when tests fail, skipped when dev context shows all passed.

Before waiting on any long external process, Read `skills/_shared/LONG_RUNNING_PROCESSES.md` and follow it.

### Phase 2: Run Tests (Redundancy-Aware)

```bash
printf '<!-- phase:start name="run-tests" index=10 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/build-and-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run integration and E2E tests (dev does NOT); do not re-run unit tests the dev context confirms passed. Failures → record in context; Ralph Loop auto-fix if enabled. On a fix issue that touched a test file, Step 2.2.5 also proves the new assertion can go red — revert the fix from a COPY and confirm the test FAILS; green there means the test is decoration. Step 2.5 then closes the phase with `nightgauge gate check-test-execution`: a suite the configured test command structurally cannot reach has never been executed by anything, so a passing run says nothing about it (#1261). Silent in any repo that excludes nothing.

### Phase 2.4: Mobile MCP E2E Tests (Agent-Driven)

```bash
printf '<!-- phase:start name="mobile-mcp-tests" index=11 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/build-and-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Build the debug APK, boot the `Pixel_9_Pro` emulator, run every `test/mobile_mcp/specs/*.md` spec via mobile-mcp tools with screenshot/result-JSON evidence, stop the emulator. Config: `validation.mobile_mcp_tests` (default `"strict"` — spec failures block PR creation); zero-overhead skip when no runnable specs or no `flutter`/`adb`/`emulator` toolchain.

### Phase 2.45: Web UI Verification Gate (verify-ui)

```bash
printf '<!-- phase:start name="verify-ui-gate" index=12 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/verify-ui-gate.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** When the diff touches frontend code in a UI-bearing repo, start the dev server, chain into `nightgauge-verify-ui` to drive the flow in a real browser, and gate on the result; emits the verify-ui `gate record-metric` per the include. Config: `validation.verify_ui_tests` (default `"strict"`). Trigger detection is deterministic (`nightgauge ci classify-ui-surface`), never LLM-judged. UI-relevant diff with no registered flow → record an explicit skip reason, never a silent pass.

### Phase 2.5: CI Parity Check (Deterministic)

```bash
printf '<!-- phase:start name="ci-parity-check" index=13 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run the repo's actual CI commands locally (format, lint, typecheck, full build). **HARD GATE**: they must pass (after up to 3 auto-fix attempts) or `VALIDATION_STATUS=failed` — PRs arrive green. The adversarial-review `gate record-metric` is emitted per the include.

### Phase 2.6: Knowledge Coverage Check

```bash
printf '<!-- phase:start name="knowledge-coverage-check" index=14 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Cross-check the implementation against the issue's `PRD.md` ACs and `decisions.md`; emit a coverage map and telemetry. Non-blocking by default (`knowledge.validate.strict: true` to gate); skip when `knowledge_path` is unset or the files are missing.

### Phase 2.7: Pre-Push Merge Validation Gate

```bash
printf '<!-- phase:start name="pre-push-merge-validation" index=15 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Validate against the latest target branch before commit/push (conflicts, merged-state regressions, security). **Gate**: on failure set `VALIDATION_STATUS=failed` and skip commit/push.

### Phase 3: Generate Manual Checklist (Conditional)

```bash
printf '<!-- phase:start name="generate-checklist" index=16 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Component-specific checklist from project type and changed files; auto-passed at >1000 passing tests and 0 failures; security items omitted (feature-dev ran them).

### Phase 4: Feedback Signal Evaluation

```bash
printf '<!-- phase:start name="feedback-signal-evaluation" index=17 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Emit backward signals ONLY for upstream structural problems (planning errors, ambiguous requirements, model limits) — normal test failures dev simply fixes do NOT warrant a signal.

### Phase 4.9: Compute Validation Status (Pre-Commit)

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Compute `VALIDATION_STATUS` before the commit decision so Phase 5 can gate on it.

### Phase 5: Commit and Push Validated Code

```bash
printf '<!-- phase:start name="commit-and-push" index=18 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Commit all changes (feature-dev implementation + Ralph fixes) and push — the commit lives here, not in feature-dev (#1608). **Gate**: if `VALIDATION_STATUS` is `"failed"`, do NOT commit or push. Commit-skip evidence rule: see Gotchas.

### Phase 6: Write Validate Context

```bash
printf '<!-- phase:start name="write-validate-context" index=19 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/context-and-board.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Write `.nightgauge/pipeline/validate-{N}.json` for `/nightgauge-pr-create` (schema: [docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md)) — **every run, even on failure** (Exit Contract above).

### Phase 7: Sync Project Board Status

```bash
printf '<!-- phase:start name="sync-project-status" index=20 total=23 stage="feature-validate" -->\n'
```

> **Read `_includes/context-and-board.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Sync board Status to "In progress" via Go binary `project move-status` (idempotent, best-effort — never fail validation over it).

### Phase 8: Output Summary and Signal Complete

```bash
printf '<!-- phase:start name="output-summary" index=21 total=23 stage="feature-validate" -->\n'
```

Display summary (branch, issue, status, commit SHA, build/test results, checklist). Next step: `passed`/`partial`/`skipped` → `/nightgauge-pr-create`; `failed` → fix issues first.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Resolve the nightgauge binary (standard cascade, as prior phases), then
# best-effort: "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress".

# Enforce the Exit Contract — fail loudly if Phase 6 was skipped (the
# orchestrator's repo-blind fallback may misreport test status; always write
# the file ourselves).
CONTEXT_FILE=".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json"
if [ ! -s "$CONTEXT_FILE" ]; then
  echo "ERROR: ${CONTEXT_FILE} missing — Phase 6 (Write Validate Context) did not run." >&2
  echo "Re-run Phase 6 before exiting; do NOT rely on the orchestrator fallback." >&2
  exit 1
fi
```

### Phase 9: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=22 total=23 stage="feature-validate" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before continuing this phase.**
