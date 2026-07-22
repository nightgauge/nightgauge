# Ralph Wiggum Loop Pattern

This document describes the Ralph Wiggum Loop integration in Nightgauge for
self-healing pipeline stages.

## Overview

The Ralph Wiggum Loop (or "Ralph Loop") is an agentic pattern where an AI agent:

1. **Executes** a task (e.g., run build, run tests)
2. **Evaluates** the result (checks exit code)
3. **Self-diagnoses** failures (parses error output)
4. **Attempts correction** (AI generates fix)
5. **Repeats** until success OR safety limits reached

The name references Ralph Wiggum from The Simpsons ("I'm helping!") because the
agent keeps trying to help even when initial attempts fail.

```
┌─────────────────────────────────────────────────────────────────┐
│                    RALPH WIGGUM LOOP PATTERN                    │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐         │
│  │   EXECUTE   │───▶│  EVALUATE   │───▶│  DIAGNOSE   │         │
│  │   (action)  │    │  (verify)   │    │  (analyze)  │         │
│  └─────────────┘    └─────────────┘    └─────────────┘         │
│         ▲                                      │                │
│         │                                      ▼                │
│         │           ┌─────────────┐    ┌─────────────┐         │
│         └───────────│   CORRECT   │◀───│   DECIDE    │         │
│                     │   (fix)     │    │  (retry?)   │         │
│                     └─────────────┘    └─────────────┘         │
│                                              │                  │
│                                              ▼                  │
│                                        [SUCCESS or              │
│                                         SAFETY LIMIT]           │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Integration Point

The Ralph Loop is integrated into the **`/nightgauge-feature-validate`**
skill for:

1. **Build verification** (Phase 1.5) — Auto-fix TypeScript/compilation errors
2. **Integration tests** (Phase 2) — Auto-fix failing tests

This is the lowest-risk integration point because:

- Build/test failures have clear, parseable output
- Fixes are typically small (syntax, types, imports)
- Human already validated the design (feature-planning approved)
- Exit codes are unambiguous (0 = success, non-zero = fail)

### Commit Behavior (Issue #1608)

Since commit+push moved from feature-dev to feature-validate, **RALPH loop fixes
are now included in the same commit as the original implementation**. When
feature-validate runs:

1. All implementation changes from feature-dev are on disk (uncommitted)
2. RALPH loop activates if build/tests fail, applying fixes on disk
3. After all validation passes, feature-validate commits everything — both the
   original implementation and any RALPH loop corrections — in a single commit
4. The commit is pushed to the remote branch

This is a key benefit: previously, feature-dev committed and pushed before
validation, so RALPH loop fixes either required additional commits or were lost
if the pipeline was interrupted between stages. Now, only validated code (with
all fixes applied) reaches the remote branch.

### Pre-existing Failure Exclusion

Before the Ralph Loop processes test failures, Phase 1.7 (Baseline Comparison)
identifies failures that already exist on `main`. These pre-existing failures
are excluded from the Ralph Loop because:

- They are not caused by the feature branch
- Attempting to fix them wastes tokens and time (observed: 13+ min, $30+ on
  Opus)
- The baseline comparison uses `git stash` to temporarily test against main's
  code, then restores feature changes

When all test failures are pre-existing, validation passes with a note. Only new
failures (tests that pass on main but fail on the feature branch) are sent to
the Ralph Loop for auto-fix attempts.

## Architecture

### Deterministic vs Probabilistic Separation

Following Nightgauge's architectural principle, the Ralph Loop strictly
separates:

| Component            | Type          | Implementation               |
| -------------------- | ------------- | ---------------------------- |
| Loop controller      | Deterministic | `ralphLoopController.ts`     |
| Error classification | Deterministic | `errorClassifier.ts` (regex) |
| Fix attempt          | Probabilistic | AI generates correction      |
| Success evaluation   | Deterministic | Exit code check              |

**Key constraint**: Loop control logic is deterministic (TypeScript code), not
AI-driven. Only the "fix attempt" step is probabilistic.

### Components

| Component                | Purpose                                 |
| ------------------------ | --------------------------------------- |
| `ralphLoopController.ts` | Iteration limits, token budget, timeout |
| `errorClassifier.ts`     | Parse and classify build/test errors    |
| `RalphLoopConfig`        | Configuration interface                 |
| `ClassifiedError`        | Structured error representation         |

### Error Classification

Errors are classified into severity levels:

| Severity        | Handling                                 |
| --------------- | ---------------------------------------- |
| `fixable`       | Ralph Loop attempts auto-fix             |
| `architectural` | Escalate to human (design change needed) |
| `configuration` | Escalate to human (environment issue)    |
| `unknown`       | Escalate to human (cannot determine)     |

### Abort Patterns

Certain error patterns immediately abort the loop and escalate to human:

- `Module not found` / `Cannot find module` — Missing dependency
- `ENOENT` / `EACCES` / `EPERM` — File system issues
- `Permission denied` — Security issue
- `Out of memory` / `ENOMEM` — Resource issue
- `Segmentation fault` — System issue
- `npm ERR! code ERESOLVE` — Dependency resolution conflict

## Configuration

### .nightgauge/config.yaml

```yaml
ralph_loop:
  # Master enable/disable
  enabled: true

  # Phase-specific toggles
  build: true
  tests: true
  lint: false # Future

  # Safety limits
  limits:
    max_iterations: 3
    token_budget_per_iteration: 2000
    total_token_budget: 10000
    iteration_timeout_ms: 60000
    total_timeout_ms: 300000

  # Patterns that abort the loop (requires human)
  abort_patterns:
    - "Custom error pattern"
```

### Environment Overrides

```bash
# Disable Ralph Loop entirely
export NIGHTGAUGE_RALPH_LOOP_ENABLED=false

# Reduce iterations for CI (faster feedback)
export NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS=1

# Increase token budget for complex fixes
export NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET=5000

# Increase total token budget
export NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKEN_BUDGET=20000

# Adjust timeouts
export NIGHTGAUGE_RALPH_LOOP_ITERATION_TIMEOUT=120000
export NIGHTGAUGE_RALPH_LOOP_TOTAL_TIMEOUT=600000
```

## Safety Guardrails

### Deterministic Limits (Mandatory)

1. **Iteration limit**: Maximum 3 attempts per error type (default)
2. **Token budget**: Maximum 2,000 tokens per iteration, 10,000 total
3. **Timeout**: Maximum 60 seconds per iteration, 5 minutes total
4. **Abort patterns**: Errors that require human intervention

### Circuit Breaker Integration

Ralph Loop integrates with the existing pipeline circuit breaker:

1. Ralph Loop iterations are separate from stage retries
2. If Ralph Loop fails after max iterations, increment stage `retry_count`
3. If `retry_count >= MAX_STAGE_RETRIES (3)`, block and require user
   intervention

### Security Considerations

1. **Output sanitization** remains enabled — blocks dangerous commands
2. **Error truncation** — Large outputs are truncated to 500 characters
3. **Input sanitization** — Should be enabled for self-prompting scenarios
4. **No arbitrary code execution** — Only runs configured build/test commands

## Token Usage Analysis

### Estimated Tokens Per Iteration

| Component         | Tokens (estimate) | Notes                         |
| ----------------- | ----------------- | ----------------------------- |
| Error context     | ~500              | Build/test output (truncated) |
| File context      | ~1000             | Relevant source code          |
| Fix prompt        | ~200              | Instructions for AI           |
| Fix generation    | ~500              | Code changes                  |
| **Per iteration** | ~2200             | Conservative estimate         |

### Cost Scenarios

| Scenario            | Iterations | Tokens | Cost (Claude) |
| ------------------- | ---------- | ------ | ------------- |
| Fix on first try    | 1          | 2,200  | ~$0.01        |
| Fix on second try   | 2          | 4,400  | ~$0.02        |
| Hit iteration limit | 3          | 6,600  | ~$0.03        |
| Complex multi-error | 3 × 2      | 13,200 | ~$0.06        |

### ROI Analysis

| Approach            | Time    | Token Cost | Human Cost |
| ------------------- | ------- | ---------- | ---------- |
| Manual fix + retry  | 5-15min | ~$0.02     | Developer  |
| Ralph Loop (3 iter) | 1-3min  | ~$0.06     | Minimal    |

**Conclusion**: Worth the extra $0.04 to save 5-15 minutes of developer time.

## Usage

### Automatic (Pipeline Mode)

Ralph Loop activates automatically in `/nightgauge-feature-validate` when:

1. Build command fails (Phase 1.5)
2. Tests fail (Phase 2)
3. `ralph_loop.enabled` is `true` in configuration

### Manual Invocation

Ralph Loop can also be triggered manually:

```bash
# In feature-validate, build fails -> Ralph Loop activates
/nightgauge-feature-validate

# Output shows Ralph Loop activity:
# ┌─────────────────────────────────────────────────────────────────┐
# │  RALPH LOOP ACTIVATED                                           │
# └─────────────────────────────────────────────────────────────────┘
# Iteration 1/3: Attempting to fix build error
# Error: TS2345: Type 'string' is not assignable to type 'number'
# Fix: Updated type annotation in src/utils/calc.ts
# Re-running build...
# ✓ Build passed after 1 iteration
```

### Disabling for Specific Runs

```bash
# Disable via environment for one run
NIGHTGAUGE_RALPH_LOOP_ENABLED=false /nightgauge-feature-validate

# Or use --skip-ralph-loop flag (if implemented)
/nightgauge-feature-validate --skip-ralph-loop
```

## Real Error Examples

These examples show the Ralph Loop handling common error categories.

### Example 1: TypeScript Type Error (Build Fix)

```
Iteration 1/3:
  Error:  TS2345: Argument of type 'string' is not assignable to parameter
          of type 'number'. (src/services/CostService.ts:42)
  Class:  build-error (fixable)
  Fix:    Changed `parseFloat(cost)` return type annotation
  Result: Build passed ✓ (1 iteration, ~800 tokens)
```

### Example 2: Test Failure — Missing Mock (Test Fix)

```
Iteration 1/3:
  Error:  TypeError: Cannot read properties of undefined
          (reading 'createOutputChannel')
          at new Dashboard (Dashboard.ts:15)
  Class:  test-failure (fixable)
  Fix:    Added `createOutputChannel` mock to vscode mock object
  Result: Tests passed ✓ (1 iteration, ~1,200 tokens)
```

### Example 3: Cascading Build Errors (Multi-Iteration)

```
Iteration 1/3:
  Error:  TS2307: Cannot find module './types.js' (3 files)
  Class:  build-error (fixable)
  Fix:    Added missing export to types/index.ts

Iteration 2/3:
  Error:  TS2554: Expected 2 arguments, but got 1.
          (tests/services/health.test.ts:45)
  Class:  test-failure (fixable)
  Fix:    Updated test call to include new required parameter
  Result: Build + tests passed ✓ (2 iterations, ~2,500 tokens)
```

### Example 4: Abort Pattern (Escalation)

```
Iteration 1/3:
  Error:  Error: EACCES: permission denied, open '/etc/config'
  Class:  abort-pattern (non-fixable)
  Action: Ralph Loop aborted — matched abort regex 'permission denied'
  Result: Escalated to user with remediation guidance
          (0 fix attempts, ~200 tokens)
```

---

## Monitoring

### Dashboard Metrics

The VSCode extension tracks:

- Ralph Loop activations per stage
- Iterations per activation
- Success rate (fixes vs escalations)
- Token consumption per loop
- Time saved estimate

### Logging

Ralph Loop events are logged to `.nightgauge/logs/ralph-loop.log`:

```json
{
  "timestamp": "2026-02-07T18:00:00Z",
  "loop_id": "ralph-build-1707328800000",
  "phase": "build",
  "iteration": 1,
  "error_type": "type",
  "error_code": "TS2345",
  "file": "src/utils/calc.ts",
  "tokens_consumed": 1850,
  "duration_ms": 12500,
  "success": true
}
```

## Troubleshooting

### Loop Not Activating

1. Check `ralph_loop.enabled: true` in `.nightgauge/config.yaml`
2. Check phase-specific toggle (`build`, `tests`, `lint`)
3. Verify error is classified as `fixable` (not `configuration` or
   `architectural`)

### Loop Hitting Limits Too Quickly

1. Increase `max_iterations` for complex projects
2. Increase `token_budget_per_iteration` for larger fixes
3. Increase `iteration_timeout_ms` for slow builds

### Loop Escalating Fixable Errors

1. Check abort patterns — may be matching too broadly
2. Verify error classifier is parsing output correctly
3. Check error severity classification

### Loop Making Wrong Fixes

1. Ralph Loop only fixes — correctness depends on AI
2. If fixes are consistently wrong, consider:
   - Reducing max iterations (fail faster to human)
   - Improving error context (more file content)
   - Disabling Ralph Loop for this phase

## Cross-Repository Self-Healing

The RALPH loop is not limited to the repository where the orchestrator is
installed. When the pipeline runs against a **target repository** (e.g.,
`acme-platform`), the same self-healing pattern applies — the
orchestrator monitors CI failures on the target repo's PR and pushes fix commits
until CI passes or safety limits are reached.

### How It Works

The orchestrator (running from the nightgauge workspace) drives the entire
loop via GitHub API. The target repo needs only standard CI (e.g., GitHub
Actions) — **no `.nightgauge/config.yaml` or RALPH configuration is
required in the target repo**.

```
┌──────────────────────────────────────────────────────────────────────────┐
│               CROSS-REPO SELF-HEALING FLOW                               │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Orchestrator (nightgauge)           Target Repo (e.g., platform)   │
│  ┌─────────────────────────┐             ┌──────────────────────────┐   │
│  │ 1. Run pipeline stages  │────────────▶│ Feature branch + PR      │   │
│  │    (issue-pickup →      │             │ created in target repo   │   │
│  │     pr-create)          │             └──────────────────────────┘   │
│  └─────────────────────────┘                        │                    │
│                                                      ▼                    │
│                                           ┌──────────────────────────┐   │
│                                           │ 2. Target repo CI runs   │   │
│                                           │    (GitHub Actions)      │   │
│                                           └──────────────────────────┘   │
│                                                      │                    │
│                                                ┌─────┴──────┐            │
│                                                │  CI fails   │            │
│                                                └─────┬──────┘            │
│                                                      ▼                    │
│  ┌─────────────────────────┐             ┌──────────────────────────┐   │
│  │ 3. Orchestrator reads   │◀────────────│ CI failure logs          │   │
│  │    failure output via   │             │ (via GitHub API)         │   │
│  │    GitHub API           │             └──────────────────────────┘   │
│  └─────────────────────────┘                                             │
│              │                                                            │
│              ▼                                                            │
│  ┌─────────────────────────┐                                             │
│  │ 4. AI diagnoses error   │                                             │
│  │    and generates fix    │                                             │
│  └─────────────────────────┘                                             │
│              │                                                            │
│              ▼                                                            │
│  ┌─────────────────────────┐             ┌──────────────────────────┐   │
│  │ 5. Commit + push fix    │────────────▶│ New commit on feature    │   │
│  │    to feature branch    │             │ branch → CI re-triggers  │   │
│  └─────────────────────────┘             └──────────────────────────┘   │
│              │                                       │                    │
│              │                                 ┌─────┴──────┐            │
│              │                                 │  CI passes  │            │
│              │                                 └─────┬──────┘            │
│              ▼                                       ▼                    │
│  ┌─────────────────────────┐             ┌──────────────────────────┐   │
│  │ 6. pr-merge proceeds    │────────────▶│ PR merges cleanly       │   │
│  └─────────────────────────┘             └──────────────────────────┘   │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

### Key Differences from Local RALPH Loop

| Aspect             | Local (feature-validate)             | Cross-Repo (post-PR)                           |
| ------------------ | ------------------------------------ | ---------------------------------------------- |
| Where it runs      | Orchestrator runs build/test locally | Orchestrator monitors remote CI via GitHub API |
| When it activates  | During feature-validate stage        | After pr-create, during pr-merge CI wait       |
| Error source       | Local stdout/stderr                  | GitHub Actions logs via API                    |
| Fix delivery       | Local file edits + re-run            | git commit + push to feature branch            |
| Target repo config | `.nightgauge/config.yaml` required   | No config needed — standard CI suffices        |
| Safety limits      | `ralph_loop.limits` in config        | Stage retry limits in orchestrator             |

### Real Example: Issue #53 (acme-platform)

The pipeline processed issue #53 against `acme-platform`, a separate
repository with its own CI. The RALPH loop self-healed through two iterations:

```
Commit 1: Initial implementation
  → CI failed: TypeScript type errors + workspace build order issues

Commit 2: Auto-fix for TypeScript type errors
  → CI failed: workspace build order still wrong

Commit 3: Auto-fix for workspace build order (added build dependency)
  → CI passed ✓

PR merged cleanly with all three commits.
```

No manual intervention was required. The orchestrator diagnosed each CI failure
from the GitHub Actions logs and pushed targeted fixes to the feature branch.

## Known Auto-Recoverable Failure Classes

These failure classes have prescribed, deterministic fixes built into the pipeline.
When detected, the pipeline executes the fix and retries **once** before escalating.

| Category         | Detection Pattern                             | Prescribed Fix                     | Recovery Bounded By                            |
| ---------------- | --------------------------------------------- | ---------------------------------- | ---------------------------------------------- |
| `stale-sdk-dist` | `RECOVERABLE: stale_sdk_dist` in build output | `npm run -w @nightgauge/sdk build` | 1 retry per stage; SDK build failure escalates |

### `stale-sdk-dist` — Stale or Missing SDK Dist

**Symptom**: `feature-validate` or `feature-dev` extension build fails with:

```
ERROR: SDK dist/index.js not found — run `npm run -w @nightgauge/sdk build` first
RECOVERABLE: stale_sdk_dist
```

**Root cause**: `nightgauge-sdk/dist/index.js` is missing or older than a
source file in `nightgauge-sdk/src/`. This typically happens after `git pull`
or branch switch when the SDK source changed but `dist/` wasn't rebuilt.

**Auto-recovery sequence** (stages: `feature-validate`, `feature-dev`):

1. Build output matches `RECOVERABLE: stale_sdk_dist`
2. Stage runs `npm run -w @nightgauge/sdk build`
3. If SDK build succeeds → retry extension build once
4. If extension build passes → `SELF_HEALED=true`, continue normally
5. If SDK build fails → stage fails with SDK build error (not freshness-check error)
6. If extension build still fails after heal → stage fails normally
7. Outcome recorded as self-heal event with category `stale_sdk_dist`

**Taxonomy**: `CatSdkDistStale` (`stale-sdk-dist`) — `Retryable: true, MaxRetries: 1`.
When stage-level healing succeeds, the orchestrator never sees the failure. When
stage-level healing fails, `CatSdkDistStale` flows through the retry engine
automatically.

**Outcome recording**: Captured in `.nightgauge/complexity-model.yaml` under
`prediction_accuracy.self_heal_events` via `nightgauge outcome record-self-heal`.
If frequency is high, consider making SDK build a mandatory first step in all pipeline
stages. See issue #2917.

## Future Enhancements

### Phase 1: Current Implementation (feature-validate)

- Build error auto-fix
- Test failure auto-fix
- Deterministic loop control

### Phase 1.5: Current Implementation (cross-repo CI healing)

- Post-PR CI failure auto-fix via GitHub API
- Works across any target repo with standard CI
- No target repo configuration required

### Phase 2: Lint Auto-Fix (Future)

- ESLint/Prettier error auto-fix
- Enable via `ralph_loop.lint: true`
- Lower risk — formatting is mechanical

### Phase 3: Feature-Dev Integration (Future)

- Test-driven correction during implementation
- Higher risk — requires stricter limits
- Human checkpoint after each correction

## Related Documentation

- [ARCHITECTURE.md](./ARCHITECTURE.md) — Deterministic vs Probabilistic pattern
- [MULTI_REPO_WORKSPACE.md](./MULTI_REPO_WORKSPACE.md) — Multi-repo routing and
  cross-repo pipeline execution
- [FEEDBACK_LOOPS.md](./FEEDBACK_LOOPS.md) — Intra-run feedback signals and
  backtracking
- [SECURITY.md](./SECURITY.md) — Output sanitization
- [CONFIGURATION.md](./CONFIGURATION.md) — Configuration reference
- [skills/nightgauge-feature-validate/SKILL.md](../skills/nightgauge-feature-validate/SKILL.md)
  — Validation skill

## Author

nightgauge
