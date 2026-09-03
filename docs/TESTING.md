# Testing Guide

This document explains how to test plugins and configurations in this
repository.

## Testing Philosophy

### Core Principles

1. **Behavior over implementation** - Test what the code does, not how it does
   it
2. **Quality over quantity** - A few well-designed tests beat many trivial ones
3. **Error paths matter** - Test failure scenarios with specific error types
4. **Integration tests for contracts** - Verify API boundaries and data flows

### Test Value Hierarchy

| Value Level | Description                           | Example                                 |
| ----------- | ------------------------------------- | --------------------------------------- |
| **High**    | Tests behavior at API boundaries      | Service method returns expected result  |
| **High**    | Tests error paths with specific types | Invalid input throws ValidationError    |
| **Medium**  | Tests state transitions               | Pipeline stage updates status correctly |
| **Medium**  | Tests integration between components  | Config flows from YAML to service       |
| **Low**     | Tests implementation details          | Mock was called with exact args         |
| **Zero**    | Tests framework behavior or constants | Constant equals its hardcoded value     |

### What to Test

Since this repository contains both configuration/documentation files AND
executable TypeScript code, testing has two focus areas:

**For configuration/documentation files:**

1. **Syntax Validation** - Files are valid Markdown/JSON/YAML
2. **Functional Testing** - Plugins work correctly with AI assistants
3. **Cross-Tool Compatibility** - Configurations work across different AI tools

**For TypeScript code (VSCode extension, SDK):**

1. **Behavior at API boundaries** - Public methods produce expected outputs
2. **Error handling** - Invalid inputs produce appropriate errors
3. **State management** - State transitions happen correctly
4. **Integration** - Components work together as expected

## Automated Validation

### UI Feature Verification (MANDATORY for UI-affecting changes)

Every feature or fix that changes a user-facing surface (dashboard, VSCode
extension views, flutter/web UI) **must** be verified end-to-end in the running
UI with the `verify-ui` skill (Playwright-driven flow check) before it is
declared done — unit and component tests are not sufficient. A page can render
perfectly with no data behind it: the Action Center E1 launch shipped a
dashboard inbox whose platform sync was never wired, and every repo-level
suite stayed green while the deployed page sat empty (#330).

The verification must exercise the real user flow against a running instance
(deployed or locally served against real APIs): navigate, assert visible
state, perform the primary action, and assert the observable consequence —
including cross-surface effects when the feature spans surfaces (e.g. resolve
on the dashboard → card disappears in the extension). Capture screenshots or
traces as evidence in the PR or issue. Operator rule, 2026-07-20.

#### The VSCode extension is verified by the operator, not by Playwright

`verify-ui` drives a browser. The extension's views render inside a VSCode
window, so the Playwright flow above does not reach them, and neither does
`ci-local.sh` — **no automated check in this repo exercises extension UI.**

The loop that does is human-in-the-loop, and it works: the Claude usage meter
rendering incorrectly was caught this way, and no suite would have caught it.

1. Land the change and **rebuild the VSIX** (`dev-install.sh`).
2. **Say so explicitly, and name what to look at** — which view, the expected
   state, and what would count as wrong. "Rebuilt, please check" is not
   actionable; the operator should not have to reverse-engineer the intent of
   the change to know where to point their eyes.
3. The operator reloads the window and exercises the surface.
4. **Ask whether it behaved as expected, and wait for the answer.** A green
   suite is not a substitute. An unanswered ask is an open question, not a pass
   — do not mark the work verified or move on until it is answered.

Step 4 is the one that gets skipped. The failure mode is not that nobody looks;
it is finishing a UI change, reporting it as done, and leaving the operator to
guess that a check was wanted.

**Never carry forward a list of "unverified" surfaces.** Verification state goes
stale the moment the operator looks at something, and a stale list copied across
handoffs reads as an outstanding backlog long after the work is done. Ask about
the change in front of you; do not reconstruct an inventory.

### VSCode Extension Test Tiers

The extension package (`packages/nightgauge-vscode/`) has four test tiers
across three runners. A file's extension says which runner owns it — there is
no fourth convention and no file matched by more than one runner or by none
(enforced by `scripts/check-test-runner-coverage.sh`, below). The data-arrival
tier shares vitest's runner and `*.test.ts` convention; it is a separate tier
because of what it asserts, not how it runs.

| Tier                                                           | Directory                                  | Naming convention                                           | Runner                                                    | CI job                                                    |
| -------------------------------------------------------------- | ------------------------------------------ | ----------------------------------------------------------- | --------------------------------------------------------- | --------------------------------------------------------- |
| Unit / integration (Node, mocked VSCode API)                   | `tests/**` (excluding `tests/playwright/`) | `*.test.ts`                                                 | `vitest` (`vitest.config.ts`)                             | `vscode` (`.github/workflows/ci.yml`)                     |
| Data arrival (real transport stubbed at its boundary)          | `tests/arrival/**`                         | `*.test.ts`                                                 | `vitest` (`vitest.config.ts`)                             | `vscode` (`.github/workflows/ci.yml`)                     |
| Browser-driven webview (real Chromium)                         | `tests/playwright/**`                      | `*.playwright.ts`                                           | `@playwright/test` (`playwright.config.ts`)               | `playwright` (`.github/workflows/ci.yml`)                 |
| Extension host smoke (real headless VSCode)                    | `tests/vscode-host/**`                     | `*.host.ts` (entry point), `*.suite.ts` (cases)             | `@vscode/test-electron` via `tests/vscode-host/launch.ts` | `vscode-host` (`.github/workflows/ci.yml`)                |
| Clean-install release gate (packaged VSIX, real agent + forge) | `tests/clean-install/**` (repo root)       | `driver/driver.js` (in-host driver), `fixture/`, `issue.md` | `scripts/clean-install-e2e.sh` (Docker + Xvfb)            | `clean-install-e2e.yml` (dispatch + weekly; never on PRs) |

Run each tier locally:

```bash
# Unit / integration
npx -w nightgauge-vscode vitest run

# Data arrival only
npx -w nightgauge-vscode vitest run tests/arrival

# Browser-driven webview (generates the real dashboard HTML fixture first —
# a bare `playwright test` skips that and ~30% of the suite fails on a
# missing file)
npm run -w nightgauge-vscode test:e2e

# Extension host smoke (downloads VSCode on first run and opens a real
# window; `npm run build` must have run, because the host loads
# dist/extension.cjs rather than src/). On Linux, prefix with
# `xvfb-run --auto-servernum`.
npm run -w nightgauge-vscode test:host
```

Both tiers previously existed without full coverage: three files under a
now-deleted `tests/e2e-playwright/dashboard/` matched neither runner (wrong
directory for vitest's exclude, wrong extension for Playwright's testMatch),
and the 61-test Playwright suite that _did_ collect ran in no CI workflow at
all — a regression in any of those tests reached `main` unnoticed. Both were
fixed in #744: every browser-driven file was consolidated into
`tests/playwright/**` under the single `*.playwright.ts` convention, and the
`playwright` CI job above runs the full suite on every PR and push to `main`,
with the Chromium binary cached by Playwright's own version.

#### The host smoke tier (#745)

The third tier answers a question the other two structurally cannot: **does
the extension come up at all?** Both vitest and Playwright mock or bypass the
VSCode API — vitest replaces the `vscode` module wholesale, Playwright renders
a webview's HTML string in a browser with no extension host behind it. So
until #745 no test in this repository ever called `activate()`, and
activation failures, missing command registrations, tree views that throw on
construction, and panels that never open were all invisible. A view can be a
perfectly correct rendering function and still never open.

The tier launches one real, headless VSCode via `@vscode/test-electron`,
opens a throwaway empty folder as the workspace, and inside that window:

- activates the extension explicitly and fails on any unhandled rejection or
  ERROR-level output-channel line during startup, including the work
  `activate()` defers to `setTimeout` up to five seconds in;
- reconciles `contributes.commands` against `vscode.commands.getCommands()`
  **in both directions** — counts are read from `package.json` at runtime, never
  hardcoded;
- opens all twelve webview panels, asserting each creates, renders a non-empty
  body, and disposes cleanly;
- resolves `getChildren()` on all seven tree data providers, first against the
  empty workspace and then after copying
  `tests/fixtures/vscode-host/populated/` into it.

Three implementation notes that are load-bearing rather than incidental:

1. **No Mocha.** `@vscode/test-cli` would bring it, and with it a high-severity
   `serialize-javascript` advisory that `scripts/npm-audit-check.js` fails the
   `security` job on. `@vscode/test-electron` alone audits clean, so the tier
   ships a ~130-line runner (`tests/vscode-host/harness.ts`) instead.
2. **The tier observes the real extension, not a copy.** A module loaded via
   `--extensionTestsPath` from inside the `--extensionDevelopmentPath` tree
   shares the extension's `vscode` API object, so patching
   `vscode.window.createWebviewPanel` there is visible to `src/**`. That is
   what lets the panels and providers under test be the ones the extension
   actually built. `installObservers()` throws if a patch does not take —
   silently observing nothing would read as green.
3. **It cannot skip.** There is no "no display, skipping" branch; CI wraps the
   command in `xvfb-run --auto-servernum`. The launcher also fails when the
   in-host module wrote no transcript, so a VSCode that dies before running
   the tests cannot exit 0.

Findings the tier recorded on its first run — real defects, deliberately not
fixed in the PR that found them — live in
`packages/nightgauge-vscode/tests/vscode-host/known-issues.ts` as shrinking
baselines: a new occurrence fails the run, and each listed entry is expected
to be deleted when its issue is fixed.

#### The data-arrival tier (#746)

The host smoke tier answers "does every surface open". This one answers **"does
it open with the right data"** — and it exists because that question had no
answer at all.

Every dashboard test in this repository is the same shape: build a fixture,
render it, assert on the HTML.

```ts
const html = getHealthTabHtml(makeResult({ overall_score: 72 }));
expect(html).toContain("72");
```

That proves the renderer works _if_ data arrives. Whether it arrives was
untested — and that is exactly what was broken, on four tabs at once, for
months, behind ~1,600 passing test files (epic #741). No renderer test can
detect it: the fixture is created after the boundary the bug lives at.

An arrival test inverts that. It stubs the view's **real transport** — the
layer the data actually crosses — runs the real service, the real refresh
method and the real renderer, and asserts a value from the transport response
appears in the rendered document.

Two tests per view at minimum:

1. **Arrival** — a recorded transport response produces a populated view.
   Assertions read the rendered HTML, never the fixture object.
2. **Failure fidelity** — each failure kind produces the state that matches
   that cause, and specifically _not_ a populated-looking empty one. For
   platform-backed views that is all five `PlatformFailureKind`s, injected as
   the Go layer's own error text so `classifyPlatformError` runs too.

##### Stub the transport, not the service

This is the whole discipline, and it is easy to get subtly wrong.
`tests/views/dashboard/Dashboard.platformRefresh.test.ts` replaces
`PlatformAnalyticsHealthService.fetchAndCache` with a `vi.fn()` returning a
`PlatformResult`. That is a fine test of the panel's branching, and it is blind
to everything between the wire and that value. The arrival tier stubs one layer
lower — `IpcClient.getInstance()` — so the service is real code under test.

| View group                                    | Boundary that gets stubbed                      |
| --------------------------------------------- | ----------------------------------------------- |
| Health, Runs, Trends, Cost, Compliance, quota | `IpcClient` (`platformGet*` / `platformAudit*`) |
| Audit                                         | `globalThis.fetch` (HTTPS + session JWT)        |
| Dependencies, Epics                           | `IpcClient` (`prList` / `issueList`)            |
| Discovery                                     | nothing — real files in a real temp workspace   |
| Overview, Pipeline, Analytics, History        | nothing — real JSONL in a real temp workspace   |

`tests/arrival/dashboardHarness.ts` builds a Dashboard whose peripheral
services are mocked and whose **renderer is not**, and returns the HTML the
panel set. A stub there must be at least as complete as the thing it replaces:
a `getAggregates()` returning `{}` or a `getConfiguration().get()` that ignores
its default value produces failures that belong to the harness, not the
product.

##### Fixtures are recorded, not written

A hand-written fixture that disagrees with the API is how a renderer test
passes while the product is broken, so fixtures live in one place —
`tests/fixtures/arrival/` — with `manifest.json` binding each to the struct
that serialises it, and `PROVENANCE.md` documenting how to re-record every one.
`tests/arrival/fixtureContract.test.ts` reads those Go structs' `json` tags and
fails when a fixture has a key the boundary does not emit or omits one it
always does.

##### A view added without an arrival test fails CI

`scripts/check-arrival-coverage.sh` (pretest, so also `scripts/ci-local.sh`)
enumerates views from the product — dashboard tabs from `VALID_TABS`, webview
panels from the `createWebviewPanel(` call sites, tree views from
`contributes.views` — and requires each to appear in `tests/arrival/views.json`
with either an arrival test or an explicit `pending` reason. Dashboard tabs may
not be pending. `scripts/test-check-arrival-coverage.sh` plants one fault per
rule so the guard is never merely assumed to work.

##### When to add to this tier

Add an arrival test when a view acquires a new data source, when a service
changes which transport method it calls, or when a bug report is "the panel is
empty / shows zeros / says the wrong thing about why". Do **not** add renderer
assertions here — those belong in `tests/views/`; this tier's assertions exist
to prove the data got there.

Slices still open at the time of writing, tracked in `views.json`: the eleven
non-dashboard webview panels and the seven tree views.

### Stage Parity Validation

Validate core issue-to-PR stage parity between the Codex/Gemini adapters, Claude
command docs, and shared skill contracts:

```bash
npx -w @nightgauge/sdk vitest run tests/cli/stageParity.test.ts
```

This check fails with stage-specific diagnostics when command routing or stage
contract references drift.

### JSON Validation

Validate JSON files using `jq`:

```bash
# Validate a single file
jq . claude-plugins/nightgauge/.claude-plugin/plugin.json > /dev/null && echo "Valid JSON"

# Validate all JSON files
find . -name "*.json" -exec jq . {} \; > /dev/null 2>&1 && echo "All JSON valid"
```

### Markdown Linting

Use `markdownlint` to check Markdown files:

```bash
# Install
npm install -g markdownlint-cli

# Lint all Markdown files
markdownlint "**/*.md" --ignore node_modules
```

### YAML Validation

Use `yamllint` for YAML files:

```bash
# Install
pip install yamllint

# Validate YAML files
yamllint configs/ standards/
```

## Manual Plugin Testing

### Testing with Claude Code

1. **Install the plugin locally:**

   ```bash
   # Add to settings.json
   {
     "plugins": [
       "/path/to/nightgauge/claude-plugins/nightgauge"
     ]
   }
   ```

2. **Test commands in a sample repository:**

   ```bash
   cd /path/to/test-repo

   # Test smart-setup
   /nightgauge:smart-setup --audit-only

   # Test update-docs
   /nightgauge:update-docs --report-only
   ```

3. **Verify expected outputs:**
   - Audit reports are accurate
   - Generated files are correct
   - No existing files are destroyed (NON-DESTRUCTIVE policy)

### Testing with GitHub Copilot

1. Copy configuration to a test repository:

   ```bash
   cp standards/AGENTS_TEMPLATE.md /test-repo/AGENTS.md
   ```

2. Open the repository in VS Code with Copilot enabled

3. Verify Copilot acknowledges the AGENTS.md in chat

4. Ask Copilot to perform tasks and verify it follows the guidelines

### Testing with Cursor

1. Copy Cursor rules to a test repository:

   ```bash
   mkdir -p /test-repo/.cursor/rules
   cp configs/cursor/*.mdc /test-repo/.cursor/rules/
   ```

2. Open in Cursor IDE

3. Verify the rules are being applied in suggestions

## Test Scenarios

### Smart Setup Command

| Scenario                            | Expected Result                                              |
| ----------------------------------- | ------------------------------------------------------------ |
| Empty repository                    | Creates AGENTS.md, CLAUDE.md, docs/, copilot-instructions.md |
| Repository with existing AGENTS.md  | Reads existing, identifies gaps, asks permission to add      |
| Repository with complete docs       | Reports "No changes needed"                                  |
| "Just audit what's missing" request | Reports status without making changes                        |
| "Skip questions" request            | Uses `[TEAM TO DOCUMENT]` markers                            |

### Update Docs Command

_Part of the `docs` plugin_

| Scenario                            | Expected Result                       |
| ----------------------------------- | ------------------------------------- |
| Docs in sync                        | Reports "Documentation is up to date" |
| Stale references                    | Identifies deprecated terms/patterns  |
| Missing cross-references            | Reports broken internal links         |
| "Auto-fix simple issues" request    | Fixes simple issues automatically     |
| "Just show me what's stale" request | Generates report without changes      |

### Non-Destructive Policy

| Scenario              | Expected Result                          |
| --------------------- | ---------------------------------------- |
| Existing AGENTS.md    | Never overwritten without permission     |
| Existing CLAUDE.md    | Never overwritten without permission     |
| Existing docs/\*.md   | Only additions offered, not replacements |
| User declines changes | Original files remain untouched          |

## CI/CD Validation

Plugin-manifest validation is **not** wired into CI. These checks —

- JSON syntax in plugin manifests
- Required fields in plugin.json
- Markdown structure in command files

— are scripted in
[.github/validation/plugin-validation.md](../.github/validation/plugin-validation.md)
and must be run by hand. `.github/workflows/lint.yml` covers Prettier, ESLint,
SKILL.md metadata and the plugin-mirror drift gate.

### Nonexistent-workflow-reference gate

`scripts/check-workflow-refs.py` fails when a tracked file names a
`.github/workflows/<name>.yml` path that does not exist in this repository. It
exists because three workflows — `skills-smoke.yml`,
`claude-plugin-validation.yml` and `synthetic-regression.yml` — were asserted
across docs, an ADR, Go comments and two shell libraries while never having
existed on any ref, so readers skipped the manual step those files described.

Two escape hatches, both in `scripts/workflow-refs-allowlist.txt`:

| Bucket    | Meaning                                                                 |
| --------- | ----------------------------------------------------------------------- |
| `example` | A workflow the reader is told to create in their own repo, or a fixture |
| `stale`   | A false claim about this repo whose correction is a wider sweep         |

The `stale` bucket is debt and may only shrink. An allowlist entry that matches
nothing — path gone, reference deleted, or the workflow since created — fails
the gate, so a fixed claim cannot leave a dead exemption behind.

`scripts/test-workflow-refs-check.sh` is the gate's own regression suite; both
run in `.github/workflows/lint.yml` and `scripts/ci-local.sh`, self-test first.

### Viewing CI Results

Check the Actions tab in GitHub for validation results after each push.

## Reporting Issues

If testing reveals issues:

1. Document the scenario that caused the issue
2. Note expected vs. actual behavior
3. Include relevant file contents
4. Create a GitHub issue with the details

## Test Checklist

Before submitting changes:

- [ ] All JSON files pass `jq` validation
- [ ] All Markdown files pass linting
- [ ] Plugin commands tested in Claude Code
- [ ] Non-destructive policy verified
- [ ] Cross-tool compatibility checked (if applicable)
- [ ] CI/CD checks pass

---

## VSCode Extension Testing

The `packages/nightgauge-vscode` package contains TypeScript code that is
tested using Vitest. This section covers testing patterns for the extension.

### Running Tests

> **WARNING: Watch Mode Footgun**
>
> Running bare `vitest` (or `npm run test` if it calls `vitest` without `run`)
> enters **interactive watch mode** and hangs indefinitely — blocking CI, AI
> agents, and any automated pipeline.
>
> | Command                               | Behavior                          |
> | ------------------------------------- | --------------------------------- |
> | `vitest run`                          | ✅ Runs once and exits            |
> | `npx -w nightgauge-vscode vitest run` | ✅ Runs once and exits (monorepo) |
> | `npx -w @nightgauge/sdk vitest run`   | ✅ Runs once and exits (SDK)      |
> | `vitest`                              | ❌ Enters watch mode — **hangs**  |
> | `npm run test` (calls bare `vitest`)  | ❌ Enters watch mode — **hangs**  |
>
> Always use `vitest run` for any non-interactive context.

```bash
cd packages/nightgauge-vscode

# Run tests once (CI-safe)
npx vitest run

# Run tests once via workspace alias
npm run test:run

# Run tests in watch mode (interactive development only)
npm run test

# Run tests with coverage
npm run test:coverage
```

### Test Structure

Tests follow the Arrange/Act/Assert pattern and use Vitest:

```typescript
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

describe("MyModule", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should do something", () => {
    // Arrange
    const input = "test";

    // Act
    const result = myFunction(input);

    // Assert
    expect(result).toBe("expected");
  });
});
```

> **Vitest 4 trap — `restoreAllMocks()` clears implementations set in a
> `vi.mock` factory.** The pattern above is safe only when every mock
> implementation is installed per-test. If a shared stub is set inside the
> `vi.mock` factory, the first test that calls `mockImplementation` leaves
> **every later test in the file** with a mock returning `undefined`. The
> failure surfaces far from its cause — as "spawn was never called" in an
> unrelated `describe` — and one override once took out 132 tests.
>
> Reinstall shared stubs in a top-level `beforeEach`, not only in the factory.

### Where extension tests must live

`packages/nightgauge-vscode/vitest.config.ts` sets
`include: ["tests/**/*.test.ts"]`. **A test placed anywhere else is silently
never run** — it is not skipped, not reported, and not counted; it simply does
not exist as far as CI is concerned.

| Package             | Tests live in | Also collected from `src/**/__tests__/`? |
| ------------------- | ------------- | ---------------------------------------- |
| `nightgauge-vscode` | `tests/**`    | **No**                                   |
| `nightgauge-sdk`    | `tests/**`    | **Yes**                                  |

The two configs differ, which is exactly how the mistake gets made (Issue
#732: nine `.test.ts` files sat under
`packages/nightgauge-vscode/src/**/__tests__/`, never collected, until they
were moved into `tests/**` and made to pass). Before adding a test to the
extension, confirm it appears in the run count; a green suite is not evidence
that your file was in it.

`packages/nightgauge-vscode/scripts/check-test-collection.sh` makes the class
unrepeatable: it fails if any `*.test.ts` file exists under
`packages/nightgauge-vscode/` outside `tests/**`, and runs automatically as
`pretest` before `npm run test -w nightgauge-vscode` (and therefore as part of
`bash scripts/ci-local.sh`). Its own coverage lives in
`scripts/test-check-test-collection.sh`, which plants a fixture file outside
`tests/` and asserts the guard catches it before asserting the clean-tree case
passes — per the #539/#549 lesson that a gate nothing exercises degrades into
an unconditional pass. This guard only enforces the location convention; it
does not verify that a file already inside `tests/` is actually matched by a
runner's include/exclude globs.

That broader check is `scripts/check-test-runner-coverage.sh` (Issue #744,
also wired into `pretest`): it fails if a `*.test.ts` file sits under
`tests/playwright/` (vitest excludes that whole directory, and Playwright's
`testMatch` never matches `.test.ts`) or a `*.playwright.ts` file sits
outside `tests/` (Playwright's `testDir` never reaches it, and vitest's
extension never matches it). The two scripts compose rather than overlap:
`check-test-collection.sh` owns the location rule for `*.test.ts` files
outside `tests/` entirely; `check-test-runner-coverage.sh` owns runner
reachability for files that already obey it, plus the `*.playwright.ts`
convention `check-test-collection.sh` never covered. Its own coverage lives
in `scripts/test-check-test-runner-coverage.sh`, following the same
plant-detect-clean pattern.

This is the check that would have caught #744's four real orphans: three
files under a now-deleted `tests/e2e-playwright/dashboard/` (excluded
directory, wrong extension) and `tests/playwright/smoke.test.ts` (right
directory, wrong extension for that directory's runner) — see
[VSCode Extension Test Tiers](#vscode-extension-test-tiers) above for where
they live now.

The same guard was extended in #745 to cover the host smoke tier, where
reachability is not a glob question at all. That tier is a single esbuild bundle rooted at
`tests/vscode-host/index.host.ts`, so what runs is what the entry point
imports. The guard therefore fails on a `*.host.ts` outside
`tests/vscode-host/` (bundled by nothing, matched by no other runner), on a
`*.suite.ts` that `index.host.ts` never imports (compiles into nothing and
reports green having run zero of its cases), and on a `*.test.ts` under
`tests/vscode-host/` (vitest _would_ collect it and then die on
`require("vscode")`, which only resolves inside an extension host). All three
shapes are exercised by `scripts/test-check-test-runner-coverage.sh`.

### A test that passes alone and fails in the suite is order-dependent, not flaky

"Flaky" invites a re-run. Order dependence does not go away on a re-run — it
goes away on YOUR machine and comes back on CI, which is the worst possible
place to learn about it.

Two mechanisms produce it, and both were hit in one session:

**Two `vi.mock` calls for the same module.** Only one wins, and which one is
registration-order dependent. A file that already mocks `IpcClient` on a single
line will not match a patch looking for the multi-line shape, so a scripted
edit adds a SECOND mock instead of extending the first — and the surviving one
decides whether the rest of the file's spies are wired at all. Symptom: an
assertion on a spy that was never called, in a file that passes in isolation.

```bash
# After any scripted edit that touches mocks, assert the count is exactly 1.
for f in tests/**/*.test.ts; do
  n=$(grep -c 'vi.mock("../../src/services/IpcClient"' "$f")
  [ "$n" -gt 1 ] && echo "DUPLICATE MOCK: $f ($n)"
done
```

**A value captured before `vi.mock`'s factory can see it.** `vi.mock` is
hoisted; a plain `const` above it is not. Use `vi.hoisted` for anything a mock
factory closes over, and match whatever the file already does — a file that
declares one spy with `vi.hoisted` and its neighbour with a bare `const` will
behave differently under load.

**Verify both ways before believing either**: run the file alone AND run the
full suite. A green single-file run is not evidence about suite behaviour, and
a green suite run on an unloaded machine is not evidence about CI.

### A fake has no API limits, so some bugs only a live probe can find

A test double answers every question you thought to ask it. The provider's real
API also enforces limits you did **not** think to ask about, and those are the
ones that ship.

`github.ListMergedPRHeads` requested `first: 250` merged PRs, a number chosen by
eye against a shell script's `--limit 500`. **GitHub caps a GraphQL connection
page at 100 and rejects the query above it.** Every unit test passed — the fake
`MergedPRHeadLister` had no page limit — and the feature shipped doing nothing
at all.

Two lessons, and the second is the sharper one.

**Probe the real API once before believing a query.** A throwaway
`gh api graphql` call against the live endpoint validates the query shape, the
argument types, and the limits in seconds. Do it for any new query, and for any
change to a `first:`/`per_page:` value. It also catches scalar-type mistakes the
Go type system cannot: `shurcooL/graphql` derives a variable's declared GraphQL
type from its Go type **name**, so passing a `graphql.String` where GitHub wants
`GitObjectID!` compiles, reads correctly, and fails only against the server.

**A number that must satisfy an external constraint gets its own test.** Not a
behavioural test — an assertion about the constant:

```go
func TestMergedPRIndexSize_FitsOneGitHubPage(t *testing.T) {
    if mergedPRIndexSize > maxGraphQLPageSize { … }
}
```

Naming `maxGraphQLPageSize` turns a magic number into a stated constraint, and
the test fails the moment someone raises the other one.

### No contract test may depend on live GitHub quota

A "verb is registered" contract test (`internal/ipc/ipc_contract_test.go`
`TestContract_*`) asserts one thing: the IPC method exists and does not
return `-32601` method-not-found. It is not a test of GitHub connectivity,
and it must never be able to fail — or hang — because of the operator's real
API quota.

Two ways that dependency creeps in, and both are load-bearing on this repo's
own daemon:

1. **A handler that calls GitHub for real.** `board.list` dispatches to a
   client with the proactive rate-limit gate attached. On a machine whose
   quota is genuinely exhausted — which is exactly the state this product's
   own daemon produces after heavy use — a client wired with
   `WithRateLimitWait()` sleeps for up to `maxFullExhaustionWait` (75
   minutes, see `internal/github/client.go`) waiting for a reset that a
   "registered" assertion never needed. `internal/ipc/server_integration_test.go`'s
   `TestMain` sets `NIGHTGAUGE_GITHUB_RATELIMIT_NO_WAIT=1` for every `serve`
   subprocess the package spawns, which flips
   `gh.Client.WithRateLimitWait()` to a no-op so the gate always fails fast
   (`gh.ErrRateLimitGated` → JSON-RPC `-32603`) instead of blocking.
   `assertMethodRegistered` already accepts `-32603` as "the method ran", so
   a gated call still proves the verb is registered.
2. **A machine-global state file read from inside the test process.** The
   rate-limit gate persists to `gh.DefaultSharedTrackerPath()`, which
   resolves `$HOME/.nightgauge/rate-limit.json` — the same file the
   developer's or CI runner's own `nightgauge serve` writes. A test harness
   that inherits `os.Environ()` unmodified (every harness in
   `internal/ipc` does, via `cmd.Env = append(os.Environ(), …)`) hands that
   real, mutable, machine-wide file to the spawned binary. `TestMain` also
   isolates `HOME` to a package-lifetime temp directory for this reason, so
   the daemon's own read of its rate-limit state is under test control, not
   the operator's.

`TestContract_Board_RegisteredSurvivesExhaustedQuota` is the fixture-backed
regression guard: it writes `remaining: 0` into the _isolated_
`$HOME/.nightgauge/rate-limit.json` (via `gh.SharedRateLimitTracker.Set`, not
hand-rolled JSON) and asserts the `board.list/registered` request still
completes in under 2 seconds. Before both isolations above, the equivalent
assertion timed out at the harness's 10s `nextLine` ceiling — proving the
fixture actually exercises the failure mode rather than trivially passing.

When adding a new IPC handler that calls out to a forge, do not assume the
existing isolation covers it for free — check whether the handler's client
carries `WithRateLimitWait()` and, if so, whether it is reachable from the
package's shared `TestMain` isolation before treating a new "registered"
subtest as gate-proof.

### Fail-open is a behaviour; failing silently is a bug

A supplementary check that degrades rather than failing the operation is usually
right — a broken lookup should not take down a sweep. But "degrade" and "say
nothing" are different decisions, and conflating them is how the `first: 250`
bug survived its first run.

The door swallowed its index error by design, so the symptom was not an error
anywhere: every lookup answered _not found_, which is **indistinguishable from a
repo that genuinely has no matches**. Nothing was red. The only evidence was in
the API ledger — one request, and no follow-up call that a successful lookup
would have made.

If a code path answers "no" both when the answer is no and when it could not
look, log the second case. Once per operation, at WARN, naming what degraded and
what the caller loses by it. The same rule the `doctor` checks follow for
_unverifiable_ — "I could not look" must never render as "there is nothing
wrong" — applies inside the code, not only in what it prints.

### Mock Factories

Mock factories are located in `tests/mocks/` and provide consistent test data:

| Mock File           | Purpose                                   |
| ------------------- | ----------------------------------------- |
| `github-api.ts`     | GitHub GraphQL responses, issue mocks     |
| `field-mappings.ts` | Project field option IDs and lookup mocks |

### Project Field Mapping Tests

The project field mapping logic has comprehensive test coverage. This tests the
label-to-field mappings for Priority and Size fields. Status is managed directly
via project board fields (not labels).

#### Test Files

| File                                                 | Coverage                         |
| ---------------------------------------------------- | -------------------------------- |
| `tests/utils/projectFieldMapping.test.ts`            | Pure mapping function unit tests |
| `tests/services/ProjectBoardService.mapping.test.ts` | Integration tests                |

#### Testing Mapping Logic

The mapping functions mirror shell script behavior for Priority and Size:

```typescript
import {
  mapPriorityLabel,
  mapSizeLabel,
  extractPriorityLabel,
  extractSizeLabel,
} from "../../src/utils/projectFieldMapping";

// Forward mapping: label → field value
expect(mapPriorityLabel("priority:high")).toBe("P1");
expect(mapSizeLabel("size:M")).toBe("M");

// Label extraction from arrays
expect(extractPriorityLabel(["type:feature", "priority:high"])).toBe("priority:high");
expect(extractSizeLabel(["type:feature", "size:M"])).toBe("size:M");
```

#### Shell Script Parity Tests

Tests verify TypeScript matches shell script behavior exactly:

```typescript
describe("matches add-to-project.sh map_priority_label()", () => {
  const shellMappings = [
    ["priority:critical", "P0"],
    ["priority:high", "P1"],
    ["priority:medium", "P2"],
    ["priority:low", "P2"],
    ["unknown", ""],
  ];

  shellMappings.forEach(([input, expected]) => {
    it(`map_priority_label("${input}") returns "${expected}"`, () => {
      expect(mapPriorityLabel(input)).toBe(expected);
    });
  });
});
```

#### Using Mock Field Mappings

For tests that need option ID lookups:

```typescript
import {
  MOCK_FIELD_MAPPINGS,
  getMockStatusOptionId,
  getMockPriorityOptionId,
  getMockSizeOptionId,
} from "../mocks/field-mappings";

// Get option IDs
expect(getMockStatusOptionId("Ready")).toBe("opt_ready_id");
expect(getMockPriorityOptionId("P1")).toBe("opt_p1_id");
expect(getMockSizeOptionId("M")).toBe("opt_m_id");
```

#### Creating Mock Issues with Consistent Mappings

Use `createMockIssueWithMappedFields` to auto-map labels to field values:

```typescript
import { createMockIssueWithMappedFields } from "../mocks/github-api";

// Priority and size auto-mapped from labels
const issue = createMockIssueWithMappedFields({
  number: 42,
  labels: ["type:feature", "priority:high", "size:M"],
});

expect(issue.priority).toBe("P1"); // Auto-mapped from priority:high
expect(issue.size).toBe("M"); // Auto-mapped from size:M
```

### Mutation testing — the only proof an assertion is load-bearing

A passing test proves nothing about whether it would catch the bug. **Mutation-
test every new assertion**: stub the fix out, confirm the test fails, restore,
then `diff` against a saved copy to prove the tree is byte-identical.

Four rules, each learned from a survivor that looked like a weak assertion and
was not:

1. **Verify the mutation actually applied.** A substitution that did not land
   looks exactly like a weak test. Anchor on an exact string and assert the
   anchor was found. A mutation that does not APPLY is never a pass.
2. **When a mutation SURVIVES, suspect the fixture before the assertion.** Of
   15 mutations run against #334, four survived and every one was a fixture that
   did not reproduce production. The sharpest: deleting the "keep the default
   branch" guard survived because the fixture left the primary checkout _on_
   `main`, and git refuses `branch -D main` unaided — the very state the bug
   requires was missing. Reproduce the production **layout**, not just a
   directory: `filepath.Join(t.TempDir(), "skills")`, not a bare `t.TempDir()`,
   when the code under test keys off a path segment.
3. **Pick fixture values that are not substrings of the fallback.** An "ignores
   the envelope, always returns the default" mutation survived because the
   asserted tool list `Read,Write,Edit,Glob,Grep` is a prefix of the default
   `Read,Write,Edit,Glob,Grep,Bash,Task`. Assert equality, not containment.
4. **A survivor can also mean the test pins a DIFFERENT line than its name
   claims.** Not every survivor is a weak assertion or a bad fixture; sometimes
   the assertion is strong, the test genuinely fails when the code is broken —
   just never for the reason the test is named after. Deleting the HTTP-status
   check in `resolveIssueRef` (#849) left a suite fully green, including a test
   called `..._AbsentIssueIsAnError`. That test served GitHub's real 404 body,
   `{"message":"Not Found"}`, which carries no `node_id` — so a _later_
   guard rejected it and the test passed without the line it was written to
   protect. Two checks in sequence, and the fixture could only ever reach the
   second.

   The tell is a survivor whose named test looks like it should have caught it.
   The fix is to make the fixture **valid in every respect except the one under
   test** — here, a well-formed issue object served under a 404 status, so
   nothing but the status check can reject it. A fixture that trips several
   guards at once cannot tell you which one is load-bearing, and it silently
   becomes the _only_ coverage the moment someone deletes the guard it was
   actually exercising.

### A test that mutates shared state cannot be made crash-safe by being careful

A test that `git mv`s a tracked file, edits a checked-in config, or otherwise
mutates the repository it runs in has a window between the mutation and the
restore. Careful `defer`-based cleanup closes that window against ordinary
failure — and not at all against the process being killed inside it.

That is not a hypothetical risk in this tree: the hermeticity suites re-run
cases inside sandboxes and **deliberately SIGKILL them mid-run**. Three rename
cases were added to `scripts/test-publication-boundary.sh`; they passed, and
they took the suite around them from "completes cleanly" to "exited 2", because
exercising a rename means moving a real tracked file and a kill between the
`mv` and the restore leaves the checkout missing a source file.

**The repair is a throwaway fixture, not more care.** Those tests now live in
`scripts/test-publication-boundary-rename.py`, which builds its own scratch
repository — so a kill at any instant destroys nothing that matters. If a test
needs to mutate a repository, give it a repository of its own.

Corollary for diagnosis: when adding a test breaks a suite that does not test
the same thing, **revert only the test file and re-run**. That separates "my fix
is wrong" from "my test is hostile to its neighbours" in one step, instead of
guessing.

### Mutate a COPY — `git checkout --` is not an undo for a mutation

Mutation testing means deliberately breaking the subject to prove a check goes
red. The obvious way to undo that is `git checkout -- <file>` — and it is wrong
whenever the surrounding work is **uncommitted**, because it does not revert
your mutation, it reverts to `HEAD`. Your mutation and the change you were
verifying go together.

This is not hypothetical. An agent auditing a two-line deletion mutation-tested
it by re-inserting the deleted key, restored with `git checkout --`, and
silently wiped the still-uncommitted fix — then reported the acceptance criteria
met, because it had already measured them before restoring. The deletion had to
be redone from scratch, and it was caught only because `git status` showed the
files unmodified while the report claimed both were changed.

**The habit that works: mutate a copy, never the working tree.**

```bash
cp docs/FOCUS_MODE.md /tmp/mutant.md    # mutate the copy
# ...apply the mutation to /tmp/mutant.md...
grep -ci vscode docs/FOCUS_MODE.md      # 1  — the repaired subject
grep -ci vscode /tmp/mutant.md          # 2  — the check discriminates
```

Two rules fall out of it:

1. **A check is only proof if it distinguishes the subject from the mutant.**
   Compare the two measurements; do not read "the command printed nothing" as a
   pass. A case-sensitive grep for `vscode` matches neither `VSCode` nor
   `VSCode Extension`, so it can be green on a completely unrepaired file.
2. **If you must mutate in place, restore from a backup you took** (`cp` before,
   `cp` back after) — never from git, unless the work is already committed.

   **"Already committed" is a claim about the specific hunk, not about the
   session.** Committing first is the obvious defence, and it has a hole:
   `git checkout -- <file>` restores that file to `HEAD` _entirely_, so it also
   reverts anything you added to it **after** the commit. An agent working #1011
   committed its work, mutation-tested the extension half, restored from git —
   correctly, at that point — then added a second fix to `scheduler.go`,
   mutation-tested that, and restored the same way. The second restore silently
   deleted the fix while leaving the test that demanded it, and `git status`
   showed a clean tree, because the next `git add -A && git commit --amend`
   committed the reverted file. It surfaced only in the full-suite gate, one
   package out of ninety.

   Note the failure mode is _inverted_ from the uncommitted case above: the tree
   looks clean and the guard looks like a flake. `cp` before / `cp` back is
   immune to both, because it restores exactly what you saved and nothing else.

The same asymmetry applies to any agent or script that verifies by editing:
**a verification step that can write is a verification step that can destroy
what it verifies.** Give it a read-only remit and a scratch directory, and diff
the tree against its report rather than trusting the report.

### A test file's own fixtures can trip the rule the file tests

A test for a lint, gate, or boundary rule has to contain examples of the thing
the rule rejects. That makes the test file itself a violation, and the tree-wide
counter the gate ratchets against will count it.

The repair is the rule's own **"files that DEFINE or TEST this rule"** allowlist
category — the one already holding the checker and its existing test script. A
new test file is absent from it only because it did not exist yet.

**Do NOT raise the ratchet instead.** A baseline that only ever moves down is a
debt being burned off; spending four of its references on fixtures converts a
one-line allowlist entry into permanent lost headroom, and the next reader
cannot tell the difference between "the tree got worse" and "we added a test".

### A test's probe should be pinned to its subject for a documented reason

When a test drives a mechanism through some _other_ call site — a rate-limit
gate exercised via whatever API call is handy, a cache exercised via whatever
read is nearest — that call site is load-bearing and invisible. Migrate it and
the test keeps passing while silently testing something else.

`client_ratelimit_test.go` drove the **GraphQL** gate through
`Client.GetRepositoryID`. #849 moved that call to REST; nothing failed, and the
tests quietly became REST-gate tests — duplicating the `TestREST_*` cases below
them while leaving the GraphQL gate with no coverage at all. Green the whole
way.

**Pick a probe whose behaviour is pinned by something a future change must
confront.** These tests now use `RepoService.RepoMetadata`, which
`docs/GITHUB_GRAPHQL_SCHEMA.md` classifies requires-GraphQL for a measured
reason (REST reports a `default_branch` for a repository that has none). It
cannot be migrated out from under them without that reversal being argued
first. Say so at the helper, so the next person to reach for a convenient probe
reads why this one was chosen.

### Prefer an assertion the bug would actually fail

Error-only assertions pass against whole classes of real defects. #296's
teardown returned no error _while destroying volumes_; #298's handler returned
`"ok"` _while wiping the wrong tree_; #323's `cleanup` returned no error _while
tearing the stack down_. One error-only assertion passes all three.

**Assert the side effect** — add a test seam if that is the only way to reach it.

Two corollaries:

- **A new refusal path needs a test for "nothing to refuse."** #325 fired its
  guard on zero projects and turned an ordinary no-op into a non-zero exit.
  Every existing test supplied at least one item, so nothing caught it.
- **Run the built binary after touching a CLI's control flow.** #325 was found
  that way and by nothing else — the suite was green.
- **After a migration, a test that still passes is not evidence.** Two
  `skillRunner.test.ts` tests went green against a new stub while asserting
  things that no longer existed, including a fallback path that had never
  existed on disk (its own `fs` mock invented it). Read every test in a migrated
  area and ask what would have to break for it to fail; delete the ones whose
  answer is "nothing."

### Assert against CI's git config, not yours

`status.showUntrackedFiles=all` is set globally on some maintainer machines.
Porcelain's default collapses an untracked directory to a single entry, so a
fixture asserting on bare `git status --porcelain` passes locally and fails in
CI. **Pin `--untracked-files=all` in tests**, exactly as the production code
does (#223). To reproduce CI locally:

```bash
git config --global --unset status.showUntrackedFiles
```

### Coverage Goals

| Area              | Target | Rationale                                |
| ----------------- | ------ | ---------------------------------------- |
| Mapping functions | >90%   | Pure functions with clear contracts      |
| Critical services | ≥60%   | State management, pipeline orchestration |
| Services          | >80%   | Core business logic                      |
| Overall extension | >70%   | Balanced coverage with quality focus     |

---

## Testing Anti-Patterns (Issue #485 Audit)

Based on a comprehensive audit of 135 test files (~63K lines), these
anti-patterns were identified. **Avoid these patterns in new tests:**

### 1. Implementation Detail Testing

**Problem:** Tests verify internal state or private methods rather than
observable behavior.

```typescript
// ❌ BAD: Testing implementation detail
it("should call internal helper", () => {
  const result = service.process(input);
  expect((service as any)._internalHelper).toHaveBeenCalled();
});

// ✅ GOOD: Testing observable behavior
it("should return processed result", () => {
  const result = service.process(input);
  expect(result.status).toBe("processed");
});
```

### 2. Framework Behavior Testing

**Problem:** Tests verify that mocks were called correctly rather than that
behavior occurred.

```typescript
// ❌ BAD: Testing framework behavior
it("should register command", () => {
  registerCommand(logger);
  expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
    "nightgauge.myCommand",
    expect.any(Function)
  );
});

// ✅ GOOD: Testing command behavior
it("should execute command successfully", async () => {
  const disposable = registerCommand(logger);
  const handler = extractHandler(disposable);
  await handler();
  expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Command executed");
});
```

### 3. Mock Call Count Assertions

**Problem:** Tests focus on how many times a mock was called rather than the
result.

```typescript
// ❌ BAD: Brittle call count assertion
expect(mockService.fetch).toHaveBeenCalledTimes(3);

// ✅ GOOD: Focus on result
const items = await provider.getChildren();
expect(items).toHaveLength(3);
```

### 4. 1:1 Test-to-Function Mapping

**Problem:** Each function has exactly one test that verifies it exists and
returns a value.

```typescript
// ❌ BAD: Trivial 1:1 mapping
describe("getNextStage", () => {
  it("returns issue-pickup after pipeline-start", () => {
    expect(getNextStage("pipeline-start")).toBe("issue-pickup");
  });
  it("returns feature-planning after issue-pickup", () => {
    expect(getNextStage("issue-pickup")).toBe("feature-planning");
  });
  // ... 6 more identical tests
});

// ✅ GOOD: Test the behavioral contract
describe("Pipeline stage progression", () => {
  it("should complete full pipeline from start to finish", () => {
    let stage = "pipeline-start";
    const visited = [stage];
    while (stage) {
      stage = getNextStage(stage);
      if (stage) visited.push(stage);
    }
    expect(visited).toEqual([
      "pipeline-start",
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
      "pipeline-finish",
    ]);
  });
});
```

### 5. Trivial Constant Verification

**Problem:** Tests verify that constants equal their hardcoded values.

```typescript
// ❌ BAD: Testing a constant
it("should have correct prefix", () => {
  expect(ENV_VAR_PREFIX).toBe("NIGHTGAUGE_");
});

// ❌ BAD: Testing defaults match schema
it("has expected default values", () => {
  expect(DEFAULT_SETTINGS.enabled).toBe(true);
  expect(DEFAULT_SETTINGS.volume).toBe(0.5);
});

// ✅ GOOD: Test behavior when defaults are used
it("should use defaults when config is missing", () => {
  mockConfigBridge.getUI.mockReturnValue({});
  const settings = getSettings();
  expect(settings.enabled).toBe(true); // Falls back to default
});
```

### 6. Pass-Through Tests

**Problem:** Tests verify that a function returns what it was given.

```typescript
// ❌ BAD: Testing pass-through
it("should handle zero values", () => {
  const result = parseTokens({ input: 0, output: 0 });
  expect(result.input).toBe(0);
  expect(result.output).toBe(0);
});

// ✅ GOOD: Test meaningful transformation or edge case
it("should handle missing fields with defaults", () => {
  const result = parseTokens({});
  expect(result.input).toBe(0); // Default applied
});
```

### 7. Mirror Tests (reimplementing the subject inside the test file)

**Problem:** The test file carries its own copy of the logic it claims to
guard, so it asserts against the copy and never touches shipped code. It stays
green through any regression in the real function — including the deletion of
the fix it was written for.

```typescript
// ❌ BAD: local reimplementation asserted against
function resolveAgentRunnerRoot(root: string | null, wm: WorkspaceManager | null) {
  return root ?? wm?.getAllRepositories()[0]?.path ?? null; // a copy, not the ship
}
expect(resolveAgentRunnerRoot(null, wm)).toBe("/workspace/repo-a");

// ✅ GOOD: import the real symbol
import { resolveAgentRunnerRoot } from "../../src/bootstrap/services";
expect(resolveAgentRunnerRoot(null, wm)).toBe("/workspace/repo-a");
```

**Falsifiability test:** gut the shipped function (e.g. make it `return null`)
and run the file. If it stays green, the file is a mirror.

**When the subject is not importable**, exactly two alternatives are
sanctioned:

1. **Export it.** Lift the logic out of the inline closure into a named export
   and import it — this is what #404 did for `resolveAgentRunnerRoot` and
   `resolveTerminalFunnelTarget` in `bootstrap/services.ts`.
2. **Assert against the source text**, when the fix is a _deletion_ and there
   is no runnable logic left to exercise — the resurrection-pin idiom of
   `packages/nightgauge-vscode/tests/bootstrap/*Removed.test.ts`. A source pin
   is also the right tool for a call site that no behavioral case can reach.

Reimplementing is never the third option: "the bootstrap is impractical to
instantiate" explains the difficulty, it does not make a copy a guard.

---

## Good Testing Patterns

### 1. Behavior Testing at API Boundaries

```typescript
describe("ProjectBoardService", () => {
  it("should return issues sorted by priority", async () => {
    const issues = await service.getIssuesByStatus("Ready");
    const priorities = issues.map((i) => i.priority);
    expect(priorities).toEqual(["P0", "P1", "P1", "P2"]);
  });
});
```

### 2. Error Path Coverage with Specific Types

```typescript
describe("ContextManager", () => {
  it("should throw ContextNotFoundError for missing file", async () => {
    await expect(manager.read("nonexistent")).rejects.toThrow(ContextNotFoundError);
  });

  it("should throw ContextValidationError for invalid schema", async () => {
    await expect(manager.read("invalid")).rejects.toThrow(ContextValidationError);
  });
});
```

### 3. Integration Tests for Service Interactions

```typescript
describe("Config Integration", () => {
  it("should flow config from YAML through merge engine to service", async () => {
    // Arrange
    await writeConfigFile({ pr: { delete_branch: false } });

    // Act
    const service = new PRService();
    await service.initialize();

    // Assert
    expect(service.canAdminMerge()).toBe(true);
  });
});
```

### 4. Table-Driven Tests for Multiple Scenarios

```typescript
describe("mapPriorityLabel", () => {
  const cases = [
    ["priority:critical", "P0"],
    ["priority:high", "P1"],
    ["priority:medium", "P2"],
    ["priority:low", "P2"],
    ["unknown", ""],
  ] as const;

  cases.forEach(([input, expected]) => {
    it(`maps "${input}" to "${expected}"`, () => {
      expect(mapPriorityLabel(input)).toBe(expected);
    });
  });
});
```

### 5. Boundary Condition Testing

```typescript
describe("calculateComplexity", () => {
  it("should handle minimum valid input", () => {
    expect(calculateComplexity({ files: 1, lines: 1 })).toBeGreaterThan(0);
  });

  it("should handle maximum expected input", () => {
    expect(calculateComplexity({ files: 100, lines: 10000 })).toBeLessThan(Infinity);
  });

  it("should reject negative values", () => {
    expect(() => calculateComplexity({ files: -1 })).toThrow();
  });
});
```

---

### Common Testing Patterns

#### Mocking External Commands

```typescript
import { exec } from "child_process";
import { promisify } from "util";

vi.mock("child_process");
const execAsync = promisify(exec);

vi.mocked(execAsync).mockResolvedValue({
  stdout: JSON.stringify({ data: "response" }),
  stderr: "",
} as any);
```

#### Testing with GraphQL Responses

```typescript
import { createMockGraphQLResponseWithPagination } from "../mocks/github-api";

const mockResponse = createMockGraphQLResponseWithPagination(
  [
    createMockReadyIssue({ number: 1, priority: "P0" }),
    createMockReadyIssue({ number: 2, priority: "P1" }),
  ],
  {
    status: "Ready",
    hasNextPage: false,
  }
);

vi.mocked(execAsync).mockResolvedValue({
  stdout: JSON.stringify(mockResponse),
  stderr: "",
} as any);
```

---

## Integration Tests

Integration tests exercise the full HTTP transport layer with real HTTP servers
(no mocked fetch). Two modes are available:

### Mock Server Mode (always runnable, no Docker required)

Uses in-process HTTP servers (`httptest.NewServer` in Go, `http.createServer` in
TypeScript) to simulate platform API responses. These run as part of the normal
test suite.

```bash
make test-platform-integration
```

Or individually:

```bash
# Go — IPC binary ↔ mock platform server
go test ./internal/ipc/... -run TestIPCPlatform -v -count=1

# TypeScript — PlatformApiClient ↔ mock HTTP server
npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts
```

**What's covered (mock mode):**

| Area               | Go (`ipc_platform_integration_test.go`)     | TypeScript (`authFlowsIntegration.test.ts`) |
| ------------------ | ------------------------------------------- | ------------------------------------------- |
| Health check       | `TestIPCPlatform_HealthCheck_Online`        | `describe('Health check')`                  |
| License validation | `TestIPCPlatform_ValidateLicense_Online`    | `describe('License validation')`            |
| Skill resolution   | `TestIPCPlatform_ResolveSkill_Online`       | `describe('Skill resolution')`              |
| Offline fallback   | `TestIPCPlatform_ValidateLicense_Offline_*` | `describe('Offline fallback')`              |
| GitHub token exch. | —                                           | `describe('GitHub token exchange')`         |
| Device flow        | —                                           | `describe('Device flow')`                   |
| Token refresh      | —                                           | `describe('Token refresh')`                 |
| Connection state   | `TestIPCPlatform_Status_Online`             | `describe('Connection state lifecycle')`    |

### Docker Compose Mode (requires acme-platform)

Runs the same test suites against a real platform instance started via Docker
Compose. Tests guarded by `PLATFORM_TEST_URL` are enabled in this mode.

```bash
make test-e2e-docker
```

Or manually:

```bash
export PLATFORM_TEST_URL=http://localhost:3000
go test ./internal/ipc/... -run TestIPCPlatform -v -count=1
npx -w nightgauge-vscode vitest run tests/integration/authFlowsIntegration.test.ts
```

The `scripts/test-e2e-platform.sh` script automates the full lifecycle: start
Docker Compose services, wait for health, run tests, tear down.

---

## GitLab CE Integration Harness (#3366)

The GitLab CE harness exercises the live-API path of the forge surface (Wave
5-2 of the forge-abstraction epic #3349). It boots a Dockerized GitLab CE
container, seeds deterministic fixtures, and runs the integration test suite
under `tests/integration/` with the `integration` build tag.

### Quick start (local)

```bash
make integration-gitlab
```

That target boots `gitlab/gitlab-ce:17.6.0-ce.0`, waits for `/-/health`,
generates a root PAT via the GitLab OAuth password grant, runs the seeder
binary (`go run ./tests/integration/cmd/seed`), executes the integration test
suite, and tears the container down with `docker compose down -v`.

### Environment variables

| Variable               | Default                 | Purpose                                                                   |
| ---------------------- | ----------------------- | ------------------------------------------------------------------------- |
| `GITLAB_E2E_URL`       | `http://localhost:8929` | Base URL of the running GitLab CE instance. Unset → suite skips entirely. |
| `GITLAB_E2E_PORT`      | `8929`                  | Port for the GitLab container; override to avoid host port conflicts.     |
| `GITLAB_ROOT_PASSWORD` | `nightgauge-test`       | Initial root password baked into the omnibus config.                      |
| `GITLAB_ROOT_TOKEN`    | (generated)             | Root-scope PAT — required by TestMain. Generated from the OAuth grant.    |
| `GITLAB_E2E_OWNER`     | `root`                  | Owner namespace used by skill-smoke scripts.                              |
| `GITLAB_E2E_REPO`      | `nightgauge-ci-test`    | Seeded project path.                                                      |
| `IB_FORGE`             | `gitlab`                | Selects the GitLab adapter for the binary.                                |

### Pinned GitLab CE version

The harness pins to `gitlab/gitlab-ce:17.6.0-ce.0` in
`tests/integration/docker-compose.gitlab.yml`. The CI cache key includes this
version string, so a version bump invalidates the cache on the next run. Update
the pin when:

1. A new minor or major version ships (cadence: approximately quarterly).
2. A GitLab security advisory requires it.
3. The forge adapter exercises an API path only available in newer CE.

After bumping, re-run `make integration-gitlab` locally to confirm the seeder
and tests still pass against the new image.

### Webhook receiver networking

The webhook tests start an `httptest.Server` inside the test binary rather
than a sidecar container (ADR-001 in
`.nightgauge/knowledge/features/3366-.../decisions.md`). The GitLab
container reaches the receiver via:

- **macOS Docker Desktop**: `host.docker.internal:<port>`
- **Linux Docker bridge**: `172.17.0.1:<port>`

The harness picks the right host string at runtime based on `runtime.GOOS`.

### Seeder fixtures

The seeder (`tests/integration/seed/seed.go`, binary at
`tests/integration/cmd/seed/main.go`) is idempotent — each resource is created
only when an existing match by deterministic name is not found. Fixtures
produced on each run:

- One private project: `root/nightgauge-ci-test` (initialized with README,
  default branch `main`)
- A 30-day root PAT named `nightgauge-test` (scopes: `api`,
  `read_repository`, `write_repository`)
- Two labels: `type:bug`, `type:feature`
- One issue board: `nightgauge-board`
- Five fixture issues, alternating label assignment
- One fixture MR on branch `feature/ci-test-mr`

### Known limitations

- **CE only**: the harness intentionally pins to GitLab CE. EE features
  (epics, multi-level group hierarchy, security dashboards) are out of scope
  for #3366.
- **OAuth app**: GitLab CE does not expose application CRUD through the
  documented public REST surface — the seeder skips OAuth app creation and
  documents this in `decisions.md`.
- **Pipeline hooks**: pipeline hook delivery requires GitLab CI to be
  enabled, which adds significant container memory pressure. The webhook
  suite asserts on Push, Merge Request, and Note hooks instead — they
  exercise the same delivery code path.

### Regenerating parity cassettes

The parity cassettes under `cmd/nightgauge/forge/testdata/gitlab-snapshots/`
are recorded from this harness (W5-1 follow-up tracked separately under
#3349). The seeder produces deterministic data so cassette diffs are
reviewable.

---

## Dockerized Mattermost Integration Harness (#3381)

The Mattermost harness exercises both directions of the Mattermost
integration end-to-end: **outbound** (post to an incoming webhook → message
lands in a channel) and **inbound** (a signed / unsigned slash command POST
→ the Go receiver in `internal/notifications/inbound` → dispatcher). It boots
a Dockerized Mattermost team-edition container with a separate Postgres,
seeds deterministic fixtures via the REST API, and runs the suite under
`tests/integration/mattermost/` with the `integration` build tag.

### Quick start (local)

```bash
make integration-mattermost
```

That target boots `mattermost/mattermost-team-edition:9.11.3` plus
`postgres:14-alpine`, waits for `/api/v4/system/ping`, runs the integration
suite (the suite seeds fixtures itself in `TestMain`), and tears the stack
down with `docker compose down -v`. Use `integration-mattermost-up` /
`integration-mattermost-down` to manage the container lifecycle separately.

### Environment variables

| Variable                    | Default                 | Purpose                                                                          |
| --------------------------- | ----------------------- | -------------------------------------------------------------------------------- |
| `MATTERMOST_E2E_URL`        | `http://localhost:8065` | Base URL of the running Mattermost instance. Unset → suite skips entirely.       |
| `MATTERMOST_E2E_PORT`       | `8065`                  | Host port for the Mattermost container; override to avoid host port conflicts.   |
| `MATTERMOST_ADMIN_USER`     | `admin`                 | System-admin username the seeder bootstraps and logs in with.                    |
| `MATTERMOST_ADMIN_PASSWORD` | `Nightgauge-Test-1`     | System-admin password. The first account on an open server becomes system-admin. |

### Pinned Mattermost version

The harness pins to `mattermost/mattermost-team-edition:9.11.3` in
`tests/integration/docker-compose.mattermost.yml`. The CI cache key includes
this version string, so a version bump invalidates the cache on the next run.
Update the pin when:

1. A new minor or major version ships (cadence: approximately quarterly).
2. A Mattermost security advisory requires it.
3. A test exercises an API path only available in a newer release.

After bumping, update the pin in both the compose file and the CI cache key,
then re-run `make integration-mattermost` locally to confirm the seeder and
tests still pass against the new image.

### Seeder fixtures

The seeder (`tests/integration/mattermost-fixtures/fixtures.go`) is
idempotent — each resource is created only when an existing match by
deterministic name is not found, so the suite is safe to re-run without a
teardown. Fixtures produced on each run:

- One open team: `nightgauge-test`
- One public channel: `ci-test-channel`
- One bot user account: `mm-ci-bot` (added to the team)
- One incoming webhook (`ci-incoming`) — the URL is captured for the
  outbound test
- One outgoing webhook (`ci-outgoing`) — the signing token is captured for
  the slash-command tests

On a fresh instance the seeder also creates the system-admin account (the
first account on an open server is granted system-admin) before logging in.

### Test design notes

- **Receiver in-process**: the slash-command tests start the inbound handler
  via `httptest.Server` rather than spawning the binary — no subprocess
  coordination needed (ADR-004).
- **Direct POST**: the signed slash-command test POSTs to the receiver
  directly from the test runner using the fixture-captured token, rather than
  triggering a real Mattermost webhook delivery (ADR-003). A full round-trip
  delivery test is deferred to #3382.
- **Token threading**: the `TokenStore` only populates from config env-refs,
  so the test threads the fixture token through the process env with
  `t.Setenv` and a synthetic `config.Config` — no production-code change to
  expose a direct setter.

### Known limitations

- **Team edition only**: the harness pins to Mattermost team edition.
  Enterprise features are out of scope for #3381.
- **No real inbound delivery**: Mattermost is not driven to deliver an
  outgoing-webhook callback to the receiver — that round-trip is deferred to
  #3382.

---

## Contract Testing Strategy (Issue #1826 Audit)

Issue #1826 audited all ~8,400 tests across the VSCode extension and SDK. The
codebase is overwhelmingly high quality — only ~22 tests were removed as
zero-value. This section codifies the audit criteria so future tests maintain
the same bar.

### What Makes a Test Zero-Value

A test is zero-value if removing it cannot cause a real bug to go undetected.
Four categories were identified:

| Category                  | Example                                                                          | Why It's Worthless                                                                |
| ------------------------- | -------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| **Logging-only**          | `expect(logger.info).toHaveBeenCalledWith('Starting pipeline')`                  | Log messages are implementation details; changing or removing them is never a bug |
| **Compile-time enforced** | `expect(disposable).toBeDefined()` as sole assertion                             | TypeScript enforces return types at compile time; a runtime check adds nothing    |
| **Tautological constant** | `expect(ENV_VAR_PREFIX).toBe('NIGHTGAUGE_')`                                     | A constant checking its own literal value — the test and code are the same string |
| **Self-fulfilling**       | Test body calls `mockLogger.info(...)` then asserts `mockLogger.info` was called | Tests the test, not the code                                                      |

### When `.toBeDefined()` IS Valid

`.toBeDefined()` is a valid assertion when used as a **TypeScript type guard**
before accessing properties with `!`:

```typescript
// ✅ VALID: Type guard enabling further assertions
const item = items.find((i) => i.number === 42);
expect(item).toBeDefined();
expect(item!.status).toBe("Ready"); // Safe because of guard above
```

It is NOT valid as a **sole assertion** that duplicates compile-time guarantees:

```typescript
// ❌ INVALID: TypeScript already enforces this return type
const disposable = registerCommand(logger);
expect(disposable).toBeDefined(); // Only assertion — adds nothing
```

### The Contract Testing Principle

Every test should verify an **observable contract** — something a caller, user,
or downstream system depends on:

- **Return values** from public functions
- **Side effects** visible outside the unit (UI messages, state changes, API
  calls)
- **Error behavior** (thrown errors, error messages shown to users)
- **State transitions** (context keys set, status bar updated)

Tests should NOT verify:

- Which internal helpers were called
- Specific log messages (unless logging IS the feature, e.g., an audit trail)
- That mocks were wired correctly (framework behavior)
- That constants equal their literal values

### Applying the Audit Checklist

When writing or reviewing tests, ask:

1. **If this test were deleted, could a real bug ship?** If no, the test is
   zero-value.
2. **Does the assertion verify something the compiler already guarantees?** If
   yes, remove it.
3. **Does the test call a mock in its own body, then assert the mock was
   called?** If yes, it's self-fulfilling — rewrite to test actual code.
4. **Is the test asserting a log message with no other behavioral assertion?**
   If yes, either add a behavioral assertion or remove the test.

---

## E2E Tests — VSCode Extension

End-to-end tests for the VSCode extension live in
`packages/nightgauge-vscode/tests/e2e/`. They wire real service classes
together with only the outermost boundary (IPC / filesystem) mocked.

### Running E2E Tests

```bash
# Run all E2E tests (from repo root)
npx -w nightgauge-vscode vitest run tests/e2e/

# Run a specific file
npx -w nightgauge-vscode vitest run tests/e2e/pipeline-execution.test.ts
```

### Test Files

| File                             | What it tests                                                                            |
| -------------------------------- | ---------------------------------------------------------------------------------------- |
| `activation-smoke.test.ts`       | Full activation chain: config load → IPC → tree view population, epic grouping, blocking |
| `activation-integration.test.ts` | Service chain: Config load → IPC init → ProjectBoardService → ProjectBoardTreeProvider   |
| `pipeline-execution.test.ts`     | Single-stage & multi-stage context file schema validation; IPC round-trip                |
| `ipc-integration.test.ts`        | IPC request/response lifecycle, event streaming, error handling, handler disposal        |

### Mocking IPC — Key Pattern

`vi.mock()` factories are hoisted before any imports are evaluated. To provide
mock function references inside the factory, use `vi.hoisted()` to create them
before the factory runs:

```typescript
// ✅ Correct: inline mock creation inside vi.hoisted()
const ipcMock = vi.hoisted(() => ({
  mockBoardList: vi.fn().mockResolvedValue([]),
  mockPipelineRun: vi.fn().mockResolvedValue({ success: true }),
  mockOn: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  mockStart: vi.fn().mockResolvedValue(undefined),
  // ...
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: ipcMock.mockBoardList,
      pipelineRun: ipcMock.mockPipelineRun,
      on: ipcMock.mockOn,
      start: ipcMock.mockStart,
    }),
  },
}));

// ❌ Wrong: calling an imported helper inside vi.hoisted()
// const ipcMock = vi.hoisted(() => setupIpcClientMock()); // → ReferenceError
```

Helper factories in `tests/mocks/ipc-client.ts` document the full interface but
**cannot** be called inside `vi.hoisted()`. Use them as reference when adding
new mock methods.

### Context File Schema Validation

Pipeline context files (`.nightgauge/pipeline/*.json`) are validated with
Zod schemas from `@nightgauge/sdk`. Use the helpers in
`tests/helpers/workspaceSetup.ts` to create isolated temp workspaces:

```typescript
import { createTempWorkspace, makeIssueContext } from "../helpers/workspaceSetup";
import { IssueContextSchema } from "@nightgauge/sdk/src/context/schemas/index.js";

const workspace = createTempWorkspace();
workspace.writeContext("issue-42.json", makeIssueContext(42));
const raw = workspace.readContext("issue-42.json");

const result = IssueContextSchema.safeParse(raw);
expect(result.success).toBe(true);

workspace.cleanup(); // always in afterEach
```

Available helpers: `makeIssueContext`, `makePlanningContext`, `makeDevContext`,
`createPipelineWorkspace` (creates all three at once).

### Checklist for Adding New E2E Tests

1. **Choose the right file** — use existing files if the scenario fits; create
   a new `tests/e2e/` file for distinct subsystems.
2. **Create hoisted mocks inline** — never call imported helpers inside
   `vi.hoisted()`.
3. **Use workspace helpers** for context file tests — never write to the real
   `.nightgauge/pipeline/` directory.
4. **Reset mocks in `beforeEach`** — call `mockXxx.mockReset()` to prevent
   test bleed. Don't use `vi.clearAllMocks()` (it clears global setup mocks).
5. **Dispose services in `afterEach`** — call `service.dispose()` and
   `workspace.cleanup()`.
6. **Verify at the boundary** — test what the user or downstream system
   observes (tree items, JSON files, IPC calls), not internal implementation.

---

## SDK Integration Tests

Integration tests validate multi-function workflows across SDK component
boundaries. They differ from unit tests in that they exercise real data flow
(file I/O, event delivery, cumulative state) rather than isolated functions.

### What They Test

- Multi-function workflows exercising SDK API contracts
- Data flow across component boundaries (e.g., write → read round-trips)
- Error handling at integration points (ContextNotFoundError, ContextValidationError)
- Event ordering and async behavior (stage:start before stage:complete)
- Cumulative token tracking across pipeline stages
- Atomic file write behavior for context handoffs

### Location

```
packages/nightgauge-sdk/src/__tests__/integration/
├── workflows/
│   ├── orchestration.integration.test.ts       # PipelineOrchestrator init, runStage(), events
│   ├── context-handoff.integration.test.ts     # ContextManager write/read, error classes
│   ├── token-tracking.integration.test.ts      # TokenTracker record(), getTotalUsage(), edge cases
│   └── event-driven-state.integration.test.ts  # EventBus on/off/once, event ordering
├── helpers/
│   ├── mocks.ts          # SDKResultMessage mock factories
│   ├── workspace.ts      # Temp workspace setup/teardown for real I/O tests
│   └── query-mocks.ts    # Mock SDKQueryFunction implementations
└── fixtures/
    ├── valid-issue-context.json
    ├── valid-planning-context.json
    └── invalid-planning-context.json
```

### How to Run

```bash
# Run integration tests only
npm run -w @nightgauge/sdk test:integration

# Run all SDK tests (unit + integration)
npx -w @nightgauge/sdk vitest run
```

### Writing New Integration Tests

1. **Use temp workspace helpers** — never write to the real `.nightgauge/pipeline/` directory. Use `createTestWorkspace()` from `helpers/workspace.ts`.
2. **Mock external dependencies at boundaries** — use `createSuccessQueryFn()` or `createFailureQueryFn()` from `helpers/query-mocks.ts` to avoid real Claude API calls.
3. **Use real EventBus and TokenTracker** — these components are simple enough that mocking them adds no value and hides real behavior.
4. **Test observable behavior** — verify what downstream systems see (files on disk, events received, cumulative totals), not internal implementation.
5. **Include both happy and error paths** — every workflow test must cover at least one error scenario.
6. **Keep runtime under 60s** — all integration suites combined target <45s. Use mocked query functions; never make real API calls.
7. **Clean up in `afterEach`** — always call `workspace.cleanup()` to remove temp directories.

### Integration Test Patterns

#### Pattern 1: Real filesystem I/O with temp workspace

```typescript
import { createTestWorkspace } from "../helpers/workspace.js";

let workspace: TestWorkspace;
let ctx: ContextManager;

beforeEach(async () => {
  workspace = await createTestWorkspace();
  ctx = new ContextManager(workspace.pipelineDir);
});

afterEach(async () => {
  await workspace.cleanup();
});
```

#### Pattern 2: Event ordering verification

```typescript
const received: string[] = [];
bus.on("stage:start", () => received.push("start"));
bus.on("stage:complete", () => received.push("complete"));

// trigger events...

expect(received).toEqual(["start", "complete"]);
```

#### Pattern 3: Cumulative state across operations

```typescript
const tracker = new TokenTracker();
// Record N stages
stages.forEach((stage) => tracker.record(stage, buildMockResultMessage(), 1000));
// Verify cumulative total
const total = tracker.getTotalUsage();
expect(total.stageCount).toBe(stages.length);
```

---

## Contract and Parity Tests

Cross-forge consumers (the VSCode extension, pipeline skills, the autonomous
scheduler) depend on GitHub and GitLab adapters returning the same logical
shape from the same logical operation. Three test layers enforce that
contract; together they form the parity surface.

### Test Layers

| Layer                | Location                                             | Purpose                                                                           |
| -------------------- | ---------------------------------------------------- | --------------------------------------------------------------------------------- |
| Cassette fixtures    | `internal/gitlab/testdata/cassettes/`                | Deterministic JSON responses replayed by `httptest.Server` stubs                  |
| Edition divergence   | `internal/gitlab/edition_test.go` (CE-vs-EE table)   | Per-feature CE/EE behaviour (weight, health, iteration, push rules, …)            |
| Cross-forge contract | `internal/forge/contract_test.go` (`RunContract`)    | Adapter-agnostic assertions run against both GitHub and GitLab `ForgeClient`      |
| Targeted parity      | `internal/forge/parity_test.go`, `parity_ci_test.go` | Specific cross-forge round-trips (Status/Priority/Size, Iteration, BlockedBy, CI) |

### `RunContract` — How It Works

`RunContract(t, adapter, client, fixtures)` runs an adapter-agnostic suite
against any `forge.ForgeClient`. The caller stands up an adapter-specific
stub server, seeds it with the fixture state, constructs the adapter, and
passes it in:

```go
// internal/forge/contract_test.go
func TestForgeContract_GitLab(t *testing.T) {
    srv := newGitlabContractStub(t)            // adapter-specific stub
    c := gitlab.NewClient(srv.URL, "tok")
    adapter := gitlab.NewForgeAdapter(c, gitlab.WithProject("o", "r"))
    RunContract(t, "GitLab", adapter, ContractFixtures{
        Owner: "o", Repo: "r", IssueNumber: 42,
        IssueTitle: "Sample issue",
        IssueLabels: []string{"bug", "priority:high"},
        IssueState: "opened",
        IncludeBoard: true,
        IncludeCI: true,
        PRNumber: 7,
    })
}
```

Subtest naming embeds adapter + method:

```
--- PASS: TestForgeContract_GitLab/GitLab/Issues/GetIssue
--- PASS: TestForgeContract_GitLab/GitLab/Board/GetItem
--- FAIL: TestForgeContract_GitHub/GitHub/Issues/ListIssues
```

so a regression in either adapter is attributed to the broken method by name,
not by line number.

`IncludeBoard` / `IncludeCI` flags let callers opt into the richer sub-suites.
GitHub currently runs only the Issues sub-suite — its Board (projectV2) and
CI (PR commits→checkSuites) GraphQL traversals are exercised in dedicated
parity files alongside their domain (`parity_test.go`, `parity_ci_test.go`).

### Cassette Fixtures

JSON files under `internal/gitlab/testdata/cassettes/<service>/<method>.json`
are static, handcrafted responses matching the GitLab REST API's shape.
Conventions:

- **Numeric IDs are fixed** (issue IID 42, project ID 5)
- **No timestamps** unless required, in which case they are fixed strings
- **Max 5–10 KB per file** — fixtures should encode the minimum the assertion
  needs
- **`web_url` uses `https://gitlab.example.com/...`**, never a real host

`internal/gitlab/cassettes_test.go` runs at `go test` time and asserts:

1. Every `*.json` under `testdata/cassettes/` parses as valid JSON
2. Each file is under the size cap
3. None contain time-dependent markers (`now()`, `$TIME$`, `{{now}}`)

To add a new cassette: capture the real response, trim to the minimum,
remove or fix timestamps, save under `<service>/<method-slug>.json`, and
serve it from a `stubGitLabServer.handle()` call in the test.

### Edition (CE vs EE) Divergence Cases

`TestEdition_CEvsEE_FeatureDivergence` is a single table-driven test in
`internal/gitlab/edition_test.go` covering ≥10 CE-vs-EE feature divergences:

- Weight / health_status field rejection on CE
- Iteration → milestone fallback on CE; native `iteration_id` on EE
- Push rules (EE-only) returning 404 on CE
- External status checks (Ultimate-only) returning 404 elsewhere
- Approval rules visibility differences
- And more — see the `cases` slice in the test for the canonical list.

Each case runs twice (`/EE` and `/CE` subtests) so failure surfaces pinpoint
the edition: `TestEdition_CEvsEE_FeatureDivergence/weight_field/CE`.

To add a new divergence case, append a `{name, run, wantCEErrIs, wantEEOK}`
entry to the `cases` slice. The `run` closure receives a fresh stub server
and exercises the feature path; it returns the error to be classified.

### `make test-parity`

The `test-parity` Makefile target runs the contract suite and prints a
per-method PASS/FAIL matrix:

```bash
make test-parity
# ...
# === Parity Pass/Fail Matrix ===
# PASS:    TestForgeContract_GitLab/GitLab/Issues/GetIssue
# PASS:    TestForgeContract_GitLab/GitLab/Board/GetItem
# PASS:    TestForgeContract_GitHub/GitHub/Issues/GetIssue
# PASS:    TestParityContract_StatusPriority_Size
# PASS:    TestParityContract_BlockedBy_RoundTrip
# ...
```

Use this target locally before opening a PR that touches either adapter, and
in CI as a gate — a single FAIL line in the matrix immediately attributes the
broken adapter+method.

The target invokes:

```bash
go test -run 'TestParityContract|TestForgeContract' ./... -v -count=1
```

`-count=1` disables result caching so the matrix reflects the current code,
not a stale pass.

### Coverage Gate for `internal/gitlab/`

The `internal/gitlab/` package is held to ≥85% statement coverage to match
the `internal/github/` baseline. Run locally:

```bash
go test ./internal/gitlab/... -coverprofile=/tmp/gl.out -count=1
go tool cover -func=/tmp/gl.out | tail -1
```

When coverage drops, look at the `0.0%` and sub-50% functions — those are
typically the right place to add a narrow boundary test. See
`internal/gitlab/coverage_test.go` for examples of compact tests for
previously-uncovered functions.

## Cross-Model Skill Evaluation Harness (#3814)

Skills are portable SKILL.md instruction files the pipeline runs against a model
chosen at spawn time (a tier alias — `haiku` / `sonnet` / `opus` — passed to the
`claude` CLI `--model` flag). A skill that works on Opus may need more explicit
detail to work on Haiku, and we actively change model routing and bump model
versions. The **cross-model skill evaluation harness** makes regressions from a
skill refactor or a model bump detectable: it runs a small set of representative
scenarios for a skill against each tier and reports pass/fail per
`(scenario, model)` cell.

See **[docs/SKILL_EVALUATION.md](SKILL_EVALUATION.md)** for the scenario format,
assertion reference, and how to add scenarios.

### Quick start

```bash
# Mock mode (deterministic, zero API cost — the default, what CI would run):
npx tsx scripts/evaluate-skills.ts
npx tsx scripts/evaluate-skills.ts --skills feature-planning,pr-create
npx tsx scripts/evaluate-skills.ts --baseline .nightgauge/skill-evals/baseline.jsonl

# Live mode (real `claude --print --model <tier>` calls — opt-in, NOT for CI):
NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-skills.ts --mode live --skills pr-merge
```

The runner prints a pass/fail matrix and writes a JSONL run record to
`.nightgauge/skill-evals/` (gitignored). It exits non-zero when any cell
regresses versus a `--baseline` (or, with no baseline, when any cell fails) so a
future CI job can adopt it unchanged.

### Two-tier mode (mirrors #2092)

Like the `PLATFORM_TEST_URL` pattern from #2092, the harness has two tiers:

- **mock** (default) — resolves each cell's output from a deterministic fixture
  keyed by `(scenarioId, model)` under `evals/fixtures/<skill>/`. No API calls,
  no quota, fully repeatable. This is the only mode CI runs and what the
  harness's own unit tests use.
- **live** (`NIGHTGAUGE_SKILL_EVAL_LIVE=1`) — spawns the `claude` CLI by
  **tier alias**, exactly as the live pipeline does, so a concrete-version bump
  (Opus 4.8 → 4.9) is itself a regression the harness catches. Live mode relies
  on ambient `claude` auth; no API keys are read, stored, or logged.

### Harness unit tests

The harness ships its own mock-mode test suite (no live API):

```bash
npx -w @nightgauge/sdk vitest run tests/eval/
```

Coverage: the assertion-engine truth table (every assertion type, pass + fail),
schema validation (malformed scenarios rejected), matrix expansion, JSONL
round-trip, and the regression diff (a baseline `pass → fail` flip is flagged a
regression; a no-baseline cell is `added`, never a regression).

## Pipeline Regression Detection

Synthetic regression tests guard against failure classes that were explicitly
eliminated from the pipeline. Unlike integration tests, these run without Docker
or a live GitHub API — they exercise in-process gate logic only.

### What It Guards

Issue #3261 eliminated the `skill-no-op` failure class, which occurred when a
skill exited 0 but produced no actual state change. Without a CI guard, any
future change to `internal/orchestrator/`, `HeadlessOrchestrator.ts`, or
`skills/**` could silently re-introduce the class.

The synthetic regression suite (Issue #3270) asserts four invariants on every
PR that touches the relevant code paths:

1. Every stage's `StageGate.Verify()` returns `KindOK` — not `KindNoOp`.
2. `V2RunRecord.OutcomeType` ≠ `"skill-no-op"`.
3. `Tokens.EstimatedCostUSD` < $0.50.
4. No stage has `FailureCategory == "stop-hook-error"`.

### Fixture

**`tests/fixtures/pipeline/synthetic-noop.json`** — a minimal issue fixture
(number 9999, `size:XS`, `type:chore`) representing a single-line README edit.
Issue 9999 is a sentinel sentinel number — it does not exist on GitHub and must
never be used as input to a real pipeline run.

### How to Run Locally

```bash
# Run only the primary KindNoOp regression check (fast, no gh required):
go test ./tests/synthetic/... -run TestSyntheticNoOpRegression -count=1 -v

# Run the full synthetic suite:
go test ./tests/synthetic/... -count=1 -v
```

The full suite completes in under 10 seconds. The `TestSyntheticNoOpRegression`
sub-tests for `pr-create` and `pr-merge` make `gh pr view` calls and may be
slow (~3 s each) when no GitHub credentials are present; they will still pass
because those gates return `KindFail` (not `KindNoOp`) on network failure.

### CI coverage

There is **no** dedicated synthetic-regression workflow. `tests/synthetic/` is a
normal Go package, so `TestSyntheticNoOpRegression` runs inside the `go test ./...`
step of the `Go build & test` job in
[.github/workflows/ci.yml](../.github/workflows/ci.yml) — it gates every pull
request, with no path filter of its own to keep in sync.

### Adding a New Regression Class

When a new failure class is eliminated and needs a guard:

1. Add the assertion to `tests/synthetic/regression_test.go` (new `Test*`
   function or a new sub-case in `TestSyntheticNoOpRegression`).
2. Update `tests/fixtures/pipeline/synthetic-noop.json` if the fixture needs new
   fields (bump `schema_version` if required).
3. Document the new invariant in this section.

No workflow path filter needs extending: `go test ./...` already picks up
anything added to `tests/synthetic/`.

## A content check must distinguish reading a value from naming one

A guard that pins a declaration to its consumer has three strengths, and only
the third is worth anything:

| Assertion                                           | What it actually proves                                                                       |
| --------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `existsSync(consumer)`                              | The path resolves. Nothing about consumption.                                                 |
| `readFileSync(consumer).includes(name)`             | The name appears in the file — as a memento key, a declaration, a schema entry, or a comment. |
| The consumer performs a **read** keyed by that name | It is consumed.                                                                               |

`settingsSurfaceInventory.test.ts` shipped the first form (#968). Every VS Code
settings namespace was pinned to a `runtime_consumer` path, and the test only
checked that the path resolved — so `nightgauge.orchestration.*` and
`nightgauge.agentTeams.*` passed while nothing read them. A user could set a
`maxUsd` budget cap and get no cap, no warning and no log line.

**The second form is the trap worth naming, because it looks like a real fix.**
Tightening to a substring check would still have passed on:

- **memento keys** — `"nightgauge.outputWindow.state"` is `globalState`, not a
  setting read;
- **the manifest that declares the settings** — the file generating
  `contributes.configuration` naturally contains every setting name;
- **the Zod schema** — same;
- **prose in a comment**, including a comment explaining the defect.

And the lenient variant is worse than the strict one. Falling back to bare leaf
identifiers made `agentTeams` pass on the word `enabled` appearing anywhere in a
14k-line file — a second vacuous assertion wearing the fix's clothes.

The working form pairs the two halves that only co-occur in a genuine read: a
`getConfiguration()` call whose section prefixes the setting, **and** the
remaining leaf as a string literal. Generalised: assert the _mechanism_ of
consumption, not the presence of the identifier.

**Then run the AC that makes it real** — restore one deleted entry and watch the
test fail with a message that names the row. A guard you have not seen go red is
a guard you have not tested.

## A deletion sweep verified by substring grep flags its own neighbours

Removing `moveQueueItemUp` / `moveQueueItemDown` (#966), the residual check
`grep -c "moveQueueItem"` returned 1 against a tree that was actually clean: the
surviving `removeQueueItem` **contains** `moveQueueItem` as a substring.

The failure runs in the dangerous direction too — a sweep that greps for a
too-short fragment reports work remaining that is already done, and one that
greps for a fragment shared with a survivor can mask a real leftover. Anchor
residual greps on the full identifier with word boundaries, or grep the exact
declaration form (`"nightgauge.moveQueueItemUp"` with quotes), and read the hits
rather than trusting the count.

## Author

nightgauge

## To pin wiring, delete the call site — not the logic

A test that calls the fixed function directly proves the **logic**. It says
nothing about whether anything _calls_ it, and those are different bugs with the
same green suite.

This caught three separate fixes in one session, each time on the first draft,
each time while the author was actively watching for it:

| Fix  | First-draft test                               | The mutation it survived                     |
| ---- | ---------------------------------------------- | -------------------------------------------- |
| #991 | fired `sched.epicCheckpoint(42)` directly      | deleting the call site in `epic.go`          |
| #992 | called `as.sweepSurvivalRecords(ctx)` directly | moving the call back below the slot gate     |
| #994 | asserted the resolver's return value           | deleting the re-resolve from `recordOutcome` |

In every case the whole defect _was_ the wiring — an unreachable rail, a call
below a gate, a read at the wrong time — so a test that skipped the caller
verified the one thing that was never broken.

**The rule is mechanical, not a matter of remembering:**

1. Write the test.
2. Delete the **call site** — not the function body, not a condition inside it.
3. If the suite stays green, the test does not pin the wiring. Drive the
   production caller instead.

Driving the caller usually needs a seam, and this codebase already has the shape:
an injectable function field defaulted in the constructor (`buildGraphFn`,
`markRefinedFn`, `evaluatePostMergeFn`). That is cheaper than it looks and is the
difference between a guard and a decoration.

Related: `docs/FAILURE_TAXONOMY.md` § construction-site / unpinned-wiring names
the production defect. This is how the _test_ for it goes wrong.
