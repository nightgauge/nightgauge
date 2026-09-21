/**
 * phaseCallbackForwarding.test.ts
 *
 * Issue #1939 — the gap-fill shipped by #1926 (`fix(#1924)`) was inert.
 *
 * Phase gap-fill was implemented correctly at every end: the SDK inferrer
 * returned the jumped-over phases, `utils/skillRunner.ts` invoked
 * `callbacks?.onPhasePassed?.()` at four sites, `PipelineBridge` supplied the
 * callback, `phaseTracker.passPhase` and `PipelineStateService.markPhasePassed`
 * wrote it, and `internal/ipc/server.go`'s `case "passed"` arm persisted it to
 * Go's RuntimeState. Not one `passed` record was ever produced, because
 * `SkillRunner.runStage` builds the runner's callback object as an EXPLICIT
 * property list -- not a spread of its own `callbacks` -- and nobody added the
 * new property. An unnamed callback is `undefined`, silently.
 *
 * Observed on run 01a0c3e3 (#1928): `feature-planning` recorded 3 of 14 phases
 * complete and 11 `unreported`; `feature-dev` 4 of 18 and 14; `feature-validate`
 * 6 of 23 and 17. Zero `passed` across all three -- the exact distribution
 * #1924 was filed to remove.
 *
 * Asserting on the inferrer is what missed this: every layer's own tests passed.
 * This pins the HOP. The required set is derived from the runner rather than
 * hardcoded, so a phase callback added to `runStageSkillHeadless` tomorrow fails
 * here until each forwarding site names it -- the bug class, not the instance.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const RUNNER_PATH = path.resolve(__dirname, "../../src/utils/skillRunner.ts");
const SKILL_RUNNER_PATH = path.resolve(__dirname, "../../src/services/SkillRunner.ts");
const HEADLESS_PATH = path.resolve(__dirname, "../../src/services/HeadlessOrchestrator.ts");

const runnerSource = readFileSync(RUNNER_PATH, "utf-8");

/**
 * Every phase callback `runStageSkillHeadless` actually invokes on its caller.
 * Derived, so the guard cannot go stale against the runner it guards.
 */
function phaseCallbacksInvokedByRunner(source: string): string[] {
  const names = new Set<string>();
  for (const m of source.matchAll(/callbacks\?\.(onPhase\w+)\?\.\(/g)) {
    names.add(m[1]);
  }
  return [...names].sort();
}

/**
 * The callback object literal a call site hands to `runStageSkillHeadless`.
 * Sliced by brace balance from the call's opening paren, which is enough to
 * separate one call site's literal from the rest of a very large file.
 */
function runnerCallbackLiteral(source: string, filePath: string): string {
  const call = source.indexOf("runStageSkillHeadless(");
  expect(call, `${filePath} must still call runStageSkillHeadless`).toBeGreaterThan(-1);

  const open = source.indexOf("{", call);
  expect(open, `${filePath} must pass a callback object literal`).toBeGreaterThan(-1);

  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") depth++;
    else if (source[i] === "}") {
      depth--;
      if (depth === 0) return source.slice(open, i + 1);
    }
  }
  throw new Error(`${filePath}: unbalanced callback literal after runStageSkillHeadless(`);
}

describe("phase callback forwarding (#1939)", () => {
  const required = phaseCallbacksInvokedByRunner(runnerSource);

  it("the runner invokes both the start and the ordering-derived phase arms", () => {
    // A guard derived from an empty set would pass vacuously forever.
    expect(required).toContain("onPhaseStart");
    expect(required).toContain("onPhasePassed");
  });

  // Only sites that forward onPhaseStart are relaying phase progress at all;
  // the other runStageSkillHeadless callers forward no phase events by design
  // and must not be dragged into this contract.
  for (const [label, filePath] of [
    ["SkillRunner.runStage", SKILL_RUNNER_PATH],
    ["HeadlessOrchestrator", HEADLESS_PATH],
  ] as const) {
    it(`${label} forwards every phase callback the runner invokes`, () => {
      const source = readFileSync(filePath, "utf-8");
      const literal = runnerCallbackLiteral(source, filePath);

      if (!literal.includes("onPhaseStart:")) return; // not a phase-relaying site

      for (const name of required) {
        expect(
          literal.includes(`${name}:`),
          `${label} relays phase progress but never forwards ${name}, so ` +
            `callbacks?.${name}?.() in skillRunner.ts is undefined and every ` +
            `${name} event is dropped (#1939)`
        ).toBe(true);
      }
    });
  }

  it("SkillRunner re-keys the passed event to the requested stage, as it does for start", () => {
    // The runner reports the stage it DETECTED; the durable record is keyed on
    // the stage we were asked to run. A passed record filed under the wrong
    // stage is worse than none.
    const literal = runnerCallbackLiteral(
      readFileSync(SKILL_RUNNER_PATH, "utf-8"),
      SKILL_RUNNER_PATH
    );
    const passedArm = literal.slice(literal.indexOf("onPhasePassed:"));
    expect(passedArm.slice(0, 200)).toContain("callbackStage");
  });
});
