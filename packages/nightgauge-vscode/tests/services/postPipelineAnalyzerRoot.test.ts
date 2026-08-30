/**
 * Issue #1229: the estimator's learning loop wrote to one repo and read from
 * another, so it could never close.
 *
 * #1213 gave the calibration loop its missing writer (`tokens.per_stage[*].model`)
 * and the loop still did not learn. The reason was a root drift one level up:
 *
 *   WRITER  `PostPipelineAnalyzer.analyze(this.getPersistentRoot(), …)`
 *           → `mainRepoRoot`, which the slot factory seeds from the RUNNER
 *             root — ONE FIXED PATH FOR EVERY SLOT.
 *   READER  `runPreFlightBudgetCheck` → `resolveMainRepoRoot(getWorkingDirectory())`
 *           → the worktree walked back to ITS OWN repo.
 *
 * In a single-repo workspace the two agree and nothing is visible. On a
 * cross-repo dispatch they name different repos, and the observed behaviour was
 * exactly that: three consecutive runs in one dogfood workspace repo rewrote
 * `stage-model-calibration.json` under a SIBLING repo, with a
 * `total_records_analyzed` of 38 that never moved because the analyzer was
 * re-reading that sibling's history every time. The dispatched repo's estimator
 * went on reporting `historical-p75` forever, looking for a table one directory
 * over.
 *
 * **Why a source assertion.** The defect is not in any function's logic — every
 * function involved was correct in isolation. It is the CHOICE OF ARGUMENT at
 * one call site, and the wrong choice is invisible in a single-repo fixture,
 * which is what every behavioural test of this path builds. A test that
 * constructs the multi-repo case would have to stand up an orchestrator, a slot
 * factory and two repo roots to assert one string. This is the same guard shape
 * the Go side uses for `stageTokensKnownGaps` in #1213: make the regression turn
 * a test red instead of silently restoring a dead loop.
 *
 * @see docs/FAILURE_TAXONOMY.md — defect class `dual-path-drift`, `path-parity`
 * @see packages/nightgauge-vscode/tests/utils/budgetHistoryRoot.test.ts — #1017,
 *      the READER half of this same drift
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "fs";
import { resolve } from "path";

const ORCHESTRATOR = resolve(__dirname, "../../src/services/HeadlessOrchestrator.ts");

/** The `PostPipelineAnalyzer.analyze(...)` invocation, arguments included. */
function analyzeCallSite(source: string): string {
  const start = source.indexOf("await PostPipelineAnalyzer.analyze(");
  expect(start, "the analyze() call site moved or was renamed").toBeGreaterThan(-1);
  const end = source.indexOf(");", start);
  expect(end, "unterminated analyze() call").toBeGreaterThan(start);
  return source.slice(start, end);
}

describe("post-pipeline analysis is rooted at the RUN's repo (#1229)", () => {
  const source = readFileSync(ORCHESTRATOR, "utf-8");

  it("passes the run repo root, not the runner's persistent root", () => {
    const call = analyzeCallSite(source);

    expect(
      call,
      "analyze() must be rooted at getRunRepoRoot(). getPersistentRoot() is the " +
        "RUNNER root — one fixed path for every slot — so on a cross-repo " +
        "dispatch the analyzer reads a sibling repo's history and rewrites that " +
        "sibling's calibration tables, and the estimator's loop never closes."
    ).toContain("this.getRunRepoRoot()");

    expect(call, "getPersistentRoot() is the regression this test exists to catch").not.toContain(
      "this.getPersistentRoot()"
    );
  });

  it("still passes the WORKTREE separately, so #1084's gate metrics survive", () => {
    // The second root is not redundant with the first: gate metrics are written
    // by a skill running IN the worktree, so a repo-root-only read never sees
    // them. Narrowing this call to one argument would silently re-break #1084.
    expect(analyzeCallSite(source)).toContain("this.getWorkingDirectory()");
  });

  it("getRunRepoRoot derives from the working directory rather than the runner root", () => {
    // The pin (`setRunRepoRoot`) is only applied by ConcurrentPipelineManager.
    // The interactive cross-repo path uses `setRepoOverride()` and never pins,
    // so a fallback to getPersistentRoot() reintroduces the same drift on that
    // path. The working directory always belongs to the run.
    const body = source.slice(
      source.indexOf("private getRunRepoRoot(): string {"),
      source.indexOf("private getWorkingDirectory(): string {")
    );
    expect(body).toContain("resolveMainRepoRoot(this.getWorkingDirectory())");
    expect(body, "falling back to the runner root is the #1229 drift, one path over").not.toContain(
      "this.getPersistentRoot()"
    );
  });
});
