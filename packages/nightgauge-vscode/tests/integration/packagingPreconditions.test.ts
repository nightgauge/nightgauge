/**
 * The packaging suites' own precondition, as a test (#1400).
 *
 * tests/globalSetup.ts provisions the gitignored artifacts that
 * failureTaxonomyPackaging / modelRegistryPackaging / claudeAgentSdkPackaging
 * assert on, so a fresh worktree does not start with five red tests. This suite
 * drives the SHIPPED planner against temp fixtures rather than restating its
 * rules — the direction #498 took the skillRunner mirror tests.
 *
 * The load-bearing case is the last one. `build:assets` is a copy from the SDK
 * source into dist/, and modelRegistryPackaging's "dist copy deep-equals the
 * SDK source" assertion (#436) exists to catch that copy going stale. If setup
 * ever re-copies unconditionally, the two sides become equal by construction
 * and that guard can never fail again. So "an existing dist/ is left exactly as
 * it was" is not a nicety — it is what keeps a different suite's guard alive,
 * and it is asserted here on bytes.
 */
import { describe, it, expect } from "vitest";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  planPackagingArtifacts,
  ensurePackagingArtifacts,
  type PackagingStep,
} from "../globalSetup";

/** A throwaway package directory standing in for packages/nightgauge-vscode. */
function fixture(): string {
  return mkdtempSync(join(tmpdir(), "ng-packaging-"));
}

/** Records what it was asked to run instead of running it. */
function recorder(): { runner: (s: PackagingStep) => void; calls: PackagingStep[] } {
  const calls: PackagingStep[] = [];
  return { runner: (s) => void calls.push(s), calls };
}

describe("packaging artifact provisioning (#1400)", () => {
  it("plans both steps for a fresh worktree, dist/ first", () => {
    const steps = planPackagingArtifacts(fixture());

    expect(steps.map((s) => s.args.join(" "))).toEqual([
      "run build:assets",
      expect.stringContaining("generate-third-party-notices.mjs"),
    ]);
    // Every step says why it is running — the log line is the whole reason a
    // reader is not surprised by a build happening inside a test run.
    for (const step of steps) expect(step.reason.length).toBeGreaterThan(0);
  });

  it("plans nothing when both artifacts are already present", () => {
    const dir = fixture();
    mkdirSync(join(dir, "dist"));
    writeFileSync(join(dir, "THIRD_PARTY_NOTICES"), "");

    expect(planPackagingArtifacts(dir)).toEqual([]);
  });

  it("plans only the missing half", () => {
    const distOnly = fixture();
    mkdirSync(join(distOnly, "dist"));
    expect(planPackagingArtifacts(distOnly).map((s) => s.command)).toEqual(["node"]);

    const noticesOnly = fixture();
    writeFileSync(join(noticesOnly, "THIRD_PARTY_NOTICES"), "");
    expect(planPackagingArtifacts(noticesOnly).map((s) => s.command)).toEqual(["npm"]);
  });

  it("never rebuilds an existing dist/ — the #436 stale-copy guard must stay able to fail", () => {
    const dir = fixture();
    mkdirSync(join(dir, "dist"));
    writeFileSync(join(dir, "THIRD_PARTY_NOTICES"), "");

    // Deliberately WRONG bytes: a dist copy that has drifted from the SDK
    // source is exactly what modelRegistryPackaging.test.ts must catch.
    const stale = join(dir, "dist", "model-registry.json");
    writeFileSync(stale, '{"stale":true}');

    const { runner, calls } = recorder();
    ensurePackagingArtifacts(dir, runner);

    expect(calls, "setup ran a command over an already-provisioned tree").toEqual([]);
    expect(
      readFileSync(stale, "utf8"),
      "setup overwrote a stale dist copy — the #436 drift guard can no longer fail"
    ).toBe('{"stale":true}');
  });

  it("executes exactly the planned steps for a fresh tree", () => {
    const dir = fixture();
    const { runner, calls } = recorder();

    ensurePackagingArtifacts(dir, runner);

    expect(calls).toEqual(planPackagingArtifacts(dir));
    expect(existsSync(join(dir, "dist"))).toBe(false); // the recorder did not build
  });
});
