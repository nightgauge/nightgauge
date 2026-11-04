---
name: verify-ui
description: Drive a running UI through a critical user flow with the Playwright
  CLI, asserting state at each step and capturing screenshots/traces, to verify a
  change actually works in the browser (not just that tests pass). Use after a
  UI-affecting change in a UI-bearing repo (dashboard, flutter web, acme-site,
  acme-web), when asked to visually verify a fix, or from feature-validate when
  the diff touches frontend code.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "2.0.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Edit Bash Glob Grep
---

# Verify UI

> Product-verification skill — drive the real app, assert at each step, prove it
> works. Verification skills have the highest measured impact on output quality;
> invest in making each flow's assertions precise.

<!-- phase-registry: standalone-skill -->

## Description

Build-and-test checks confirm code compiles and units pass; they do **not**
confirm the feature works in a browser. This skill drives the running app through
a defined **flow**, asserting concrete state at each step (URL, visible text,
element state, network response) and capturing a screenshot/trace per step under
`.nightgauge/verify/`. The output is a per-step pass/fail report plus
artifacts you can eyeball.

The browser is driven with the **Playwright CLI**
([`@playwright/cli`](https://www.npmjs.com/package/@playwright/cli)) through
Bash. The CLI keeps one persistent headless browser session alive across
invocations (`open` starts it, `close` ends it), so each step is a plain shell
command — no MCP server involved.

## Invocation

| Tool        | Command                                  |
| ----------- | ---------------------------------------- |
| Claude Code | `/nightgauge-verify-ui <flow> [--url …]` |
| Codex       | `$nightgauge-verify-ui <flow>`           |

## When to use

- After implementing/fixing UI in a UI-bearing repo and before opening the PR.
- When the user asks to "verify", "confirm it works", or "screenshot" a change.
- From `feature-validate` when the diff touches frontend code — gate on
  behavioral/visual assertions, not just exit codes.

## Prerequisites

- **The app is running and reachable** (dev server or a built preview). Resolve
  the base URL from `--url`, else the flow's default, else the repo's dev script.
- **The Playwright CLI is available.** `npx -y @playwright/cli --version` must
  succeed, and a browser must be installed
  (`npx -y @playwright/cli install-browser` on a fresh machine). If the CLI or
  browser is unavailable, fail with that instruction — do not silently "pass".

Command surface used by this skill (see `npx -y @playwright/cli --help`):
`open` / `goto` / `click` / `fill` / `press` / `snapshot` / `find` /
`screenshot` / `requests` / `request` / `console` / `eval` / `close`.

## Flows

A **flow** is a named, ordered list of steps, each with an **action** and an
**assertion**. Flows live in `flows/<name>.md` in this skill. Bundled:

- **`flows/dashboard-auth.md`** — reference flow: sign in to the dashboard and
  land on an authenticated route. Read it as the template for new flows.

To add a flow, copy the reference, set the default URL, and write **one explicit
assertion per step** (a step with no assertion is not verification).

## Workflow

### Phase 0: Environment Preflight

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


Additionally confirm the Playwright CLI is callable
(`npx -y @playwright/cli --version`) and the target base URL responds (a quick
`curl -fsS "$BASE_URL" >/dev/null`). Abort with a clear message if either is
missing.

### Phase 1: Resolve the flow

Read `flows/<name>.md`. Resolve the base URL (`--url` > flow default > repo dev
script). Create the run artifact dir:

```bash
RUN_DIR=".nightgauge/verify/${FLOW}-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$RUN_DIR"
```

Then open the browser session and reset the console baseline:

```bash
npx -y @playwright/cli open "$BASE_URL"
npx -y @playwright/cli console error --clear
```

`open` runs headless by default and the session persists across subsequent
Bash invocations until `close`.

### Phase 2: Drive the flow, asserting each step (state + console)

For each step, in order:

1. **Act** with the matching CLI command (`goto` / `click` / `fill` / `press`).
   Target elements by the `[ref=…]` from the latest `snapshot` output.
2. **Assert** the step's expected state from `snapshot` output (or a
   `requests` / `request <n>` check for API-backed steps — `requests` numbers
   each request; `request <n>` shows its status and bodies). Treat a
   missing/wrong element or an unexpected status as a **hard failure** — record
   it and stop the flow (don't push past a broken step into noise).
3. **Assert zero new console errors.** Run
   `npx -y @playwright/cli console error` after the step: because the list was
   cleared after the previous step (empty output = clean), anything it returns
   is new to this step and is a hard failure, exactly like a wrong element.
   Then reset the baseline for the next step with
   `npx -y @playwright/cli console error --clear` (the `--clear` call itself
   prints no messages — always inspect first, then clear). A step that changes
   nothing in the console is not a failure; a step that adds a JS exception or
   a failed resource load is, even if the visual assertion in (2) passed.
4. **Capture**
   `npx -y @playwright/cli screenshot --filename "$RUN_DIR/NN-<step>.png"`.

### Phase 2.5: Core Web Vitals (primary flow only, budget optional)

After the flow's first navigation (the primary page load), measure Core Web
Vitals with `npx -y @playwright/cli eval '<snippet>'`. **Do not** call
`performance.getEntriesByType('largest-contentful-paint' | 'layout-shift')`
directly — verified empirically (#4193) that this logs a `Deprecated API for
given entry type` console **warning** on every call, which would itself
pollute the console-error checks in Phase 2. Use a buffered
`PerformanceObserver` instead, which returns the same data with zero console
noise:

```js
async () => {
  const lcp = await new Promise((resolve) => {
    let settled = false;
    const obs = new PerformanceObserver((list) => {
      const entries = list.getEntries();
      if (entries.length) {
        settled = true;
        resolve(entries[entries.length - 1].startTime);
      }
    });
    obs.observe({ type: "largest-contentful-paint", buffered: true });
    setTimeout(() => {
      if (!settled) resolve(null);
    }, 500);
  });
  const cls = await new Promise((resolve) => {
    let total = 0;
    const obs = new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (!e.hadRecentInput) total += e.value;
      }
    });
    obs.observe({ type: "layout-shift", buffered: true });
    setTimeout(() => resolve(Number(total.toFixed(4))), 500);
  });
  const nav = performance.getEntriesByType("navigation")[0] || {};
  return { lcp_ms: lcp, cls, ttfb_ms: nav.responseStart ?? null };
};
```

Read an optional budget from `.nightgauge/config.yaml`
(`validation.verify_ui.web_vitals_budget`, e.g. `{lcp_ms: 2500, cls: 0.1}`). No
budget configured → report the numbers in `report.json` and do not fail.
Budget configured and exceeded → hard failure for the flow (same severity as a
failed step assertion), recorded in `report.json` so the reason is visible.

### Phase 3: Report + handoff

Close the browser session (`npx -y @playwright/cli close` — run this on
failure paths too), then write `$RUN_DIR/report.json`:

```json
{
  "flow": "dashboard-auth",
  "base_url": "http://localhost:5173",
  "status": "passed",
  "steps": [
    {
      "n": 1,
      "name": "load-login",
      "status": "passed",
      "screenshot": "01-load-login.png",
      "new_console_errors": []
    }
  ],
  "web_vitals": {
    "lcp_ms": 152,
    "cls": 0,
    "ttfb_ms": 1.8,
    "budget": null,
    "budget_exceeded": false
  },
  "artifacts_dir": ".nightgauge/verify/dashboard-auth-…"
}
```

`status` is `"passed"` only when every step passed, no step introduced a new
console error, and (when a budget is configured) no vital exceeded it.

When invoked from `feature-validate`, reference `report.json` in the validation
context file and **fail validation if `status` is not `passed`**.

### Self-Assessment Epilogue

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file,
`assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json` in the clone's pipeline state
directory, through the binary (JSON on stdin; never a direct file write):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge layout write pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json" <<'JSON'
{ ... the record below ... }
JSON
```

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ASSESSMENT_FILE="$(nightgauge layout path pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json")"
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```


## Gotchas

- **A step without an assertion is not verification.** Screenshots alone prove
  nothing — every step must assert concrete state and fail loudly on mismatch.
- **Never report "passed" when the CLI/app was unavailable.** Inability to drive
  the app is a failure, not a pass (mirrors the staging-200 footgun: a green HTTP
  status doesn't mean the flow worked).
- **Stop at the first hard failure.** Driving past a broken step produces
  misleading downstream screenshots.
- **Keep selectors resilient.** Prefer the `[ref=…]` targets from `snapshot`
  over brittle CSS/nth-child chains.
- **`console error` accumulates until cleared — and `--clear` prints nothing.**
  Verified empirically: the list persists across CLI invocations until
  `console error --clear` resets it, and the `--clear` call returns no
  messages. So the per-step pattern is always _inspect_
  (`console error`) then _reset_ (`console error --clear`); collapsing the two
  into one `--clear` call silently discards the very errors you were checking
  for.
- **Always `close` the session, including on failure.** The browser is a
  persistent daemon that outlives the Bash call that opened it; a flow that
  aborts without `close` leaks a headless browser
  (`npx -y @playwright/cli close-all` reclaims strays).
- **The CLI writes a `.playwright-cli/` scratch dir in CWD** (console logs,
  snapshot files). It is working state, not an artifact — never commit it;
  everything reportable belongs in `$RUN_DIR`.
- **`getEntriesByType('largest-contentful-paint' | 'layout-shift')` is noisy.**
  Verified empirically: calling it directly logs a `Deprecated API for given
entry type` console warning each time, which would corrupt the console-error
  checks for later steps. Use `PerformanceObserver({buffered: true})` (Phase
  2.5) instead — same data, zero console noise.
- **A missing Core Web Vitals budget is not a failure.** Report the numbers;
  only fail when a budget is configured in
  `.nightgauge/config.yaml` and exceeded.

### Cross-cutting gotchas (every Nightgauge skill)

> These apply to **all** skills. Skill-specific gotchas live above this line in
> each skill's `## Gotchas` section. Keep both terse: symptom → why → do-instead.
> When a new failure mode is root-caused (retro / failure-taxonomy), add it here
> or to the owning skill — Gotchas are the highest-signal part of a skill.

- **Bare `nightgauge` is not always on `PATH`.** Run the Phase 0 PREFLIGHT
  binary-discovery cascade first and prepend `dirname($BINARY)` to `PATH`.
  Skipping it surfaces as `command not found` only on some machines.
- **Never emit a foreground `sleep` wait loop.** The harness denies foreground
  `sleep` — an unbounded shell idiom that polls a completion marker in a
  `while`/`until` loop with no iteration cap — the tool call is rejected
  outright. To wait on a long-running background job, start it with the
  backgrounded-command form the harness provides (e.g. a Bash call with
  `run_in_background: true`) and then block on its completion notification /
  the Monitor tool instead of polling on a timer. Never invent a foreground
  poll loop to bridge the wait (#289).
- **Never call the forge CLI directly.** Use `nightgauge forge …`
  (`issue`/`pr`/`project`/`label`/`repo`/`auth`/`graphql`), never a bare
  `gh`/`glab`. The `no-direct-gh` lint fails CI on regressions; the abstraction
  is what lets `IB_FORGE=gitlab` work unchanged.
- **Headless skills must not block on input.** If `AskUserQuestion` is not in
  `allowed-tools`, the skill runs headless — make autonomous decisions or fail
  with a clear error. A blocked prompt hangs the whole pipeline run.
- **Write your handoff/context file before exiting — even on failure.** Pipeline
  stages that exit without their `{stage}-{N}.json` in the clone's pipeline
  state directory (`nightgauge layout path pipeline`) force the orchestrator
  onto a repo-blind deterministic fallback that may misreport state (issue 3114). Do not rely on the fallback.
- **An assertion that cannot go red is decoration.** A test that passes both
  before and after a fix constrains nothing — a regression test written for an
  SSH-push bug asserted "a push succeeds with no auth configured" and passed
  identically on the broken code, because a `file://` remote needs no
  credentials either way. Prove it: `cp <file> /tmp/<file>.bak`, restore the
  pre-fix behavior **from the copy**, confirm the new test FAILS, restore, and
  confirm it passes. Never `git checkout -- <file>` to back a fix out — on an
  uncommitted branch that destroys the fix. See
  [Vacuous Assertion](https://github.com/nightgauge/nightgauge/blob/main/docs/FAILURE_TAXONOMY.md#vacuous-assertion-the-test-that-cannot-go-red).
- **A green suite is a statement about the cases that RAN.** After adding
  assertions, check the reported PASS count rose by exactly the number you
  added; a suite can skip a case silently and still print "all tests passed".
- **Verify the issue's premise before implementing it.** An issue's stated cause
  can be flatly wrong — a request to delete a sweep that was never built, a
  claim that a failure category already classified an error when it fell through
  to `CatUnknown`, a "build it or delete it" fork where building was
  architecturally impossible. Check the claim against the tree first; if the
  premise is false, do the smaller correct thing and say so in the handoff.
- **Never report success when a step failed.** Swallowing a failed build/test
  lets a broken change flow downstream where it is caught later at higher cost
  (#2779). Surface the failure with its output.
- **Never push to `main`.** Use `feat/`/`fix/`/`docs/` branches; merges are
  manual squash only (no `--auto`, no `--admin`).
- **Production safety.** Never run `docker compose down -v` against a production
  stack — `-v` destroys `postgres_data`. Fix credential mismatches with
  `ALTER USER`, not a volume wipe.


## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
