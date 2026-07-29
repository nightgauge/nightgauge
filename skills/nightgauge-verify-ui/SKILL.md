---
name: nightgauge-verify-ui
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

<!-- include: ../_shared/PREFLIGHT.md -->

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

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

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

<!-- include: ../_shared/GOTCHAS.md -->

## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
