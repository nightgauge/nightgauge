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
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from "node:fs";
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
  it("plans every artifact for a fresh worktree", () => {
    const steps = planPackagingArtifacts(fixture());

    // Both data files plus the notices generator. Asserted on the DESTINATIONS
    // rather than on a script name, so renaming an npm script cannot make this
    // pin a stale literal.
    expect(steps).toHaveLength(3);
    const joined = steps.map((s) => s.args.join(" ")).join("\n");
    expect(joined).toContain("dist/failure-taxonomy.yaml");
    expect(joined).toContain("dist/model-registry.json");
    expect(joined).toContain("generate-third-party-notices.mjs");
    // Every step says why it is running — the log line is the whole reason a
    // reader is not surprised by a build happening inside a test run.
    for (const step of steps) expect(step.reason.length).toBeGreaterThan(0);
  });

  /** A fully provisioned package dir: both data files and the notices. */
  function provisioned(): string {
    const dir = fixture();
    mkdirSync(join(dir, "dist"));
    writeFileSync(join(dir, "dist", "failure-taxonomy.yaml"), "taxonomy:\n");
    writeFileSync(join(dir, "dist", "model-registry.json"), "{}");
    writeFileSync(join(dir, "THIRD_PARTY_NOTICES"), "");
    return dir;
  }

  it("plans nothing when every artifact is already present", () => {
    expect(planPackagingArtifacts(provisioned())).toEqual([]);
  });

  it("plans the data files for a dist/ that exists but holds only the bundle", () => {
    // THE CASE THE FIRST VERSION MISSED. `build:types` and `build:bundle` each
    // create dist/ with neither data file, and build:types runs first in the
    // full chain — so an interrupted build, or a bare `npm run watch`, lands
    // here. A directory-level gate saw dist/ and planned nothing, leaving four
    // of the five red tests exactly as they were.
    const dir = fixture();
    mkdirSync(join(dir, "dist"));
    writeFileSync(join(dir, "dist", "extension.cjs"), "// bundle");
    writeFileSync(join(dir, "THIRD_PARTY_NOTICES"), "");

    const joined = planPackagingArtifacts(dir)
      .map((s) => s.args.join(" "))
      .join("\n");
    expect(joined).toContain("dist/failure-taxonomy.yaml");
    expect(joined).toContain("dist/model-registry.json");
  });

  it("plans only the missing half", () => {
    const dir = provisioned();
    rmSync(join(dir, "THIRD_PARTY_NOTICES"));
    expect(planPackagingArtifacts(dir).map((s) => s.command)).toEqual(["node"]);

    const dir2 = provisioned();
    rmSync(join(dir2, "dist", "model-registry.json"));
    const steps = planPackagingArtifacts(dir2);
    expect(steps).toHaveLength(1);
    expect(steps[0].args.join(" ")).toContain("dist/model-registry.json");
  });

  it("never rebuilds a PRESENT data file — the #436 stale-copy guard must stay able to fail", () => {
    const dir = provisioned();

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

  it("does not heal a stale sibling while provisioning an absent one", () => {
    // The reason the copy is per FILE and not a call to `build:assets`: that
    // script recopies BOTH data files, so provisioning the missing taxonomy
    // would silently overwrite a stale model-registry.json and disarm #436.
    const dir = provisioned();
    const stale = join(dir, "dist", "model-registry.json");
    writeFileSync(stale, '{"stale":true}');
    rmSync(join(dir, "dist", "failure-taxonomy.yaml"));

    const { runner, calls } = recorder();
    ensurePackagingArtifacts(dir, runner);

    expect(calls).toHaveLength(1);
    expect(calls[0].args.join(" ")).toContain("dist/failure-taxonomy.yaml");
    expect(calls.map((c) => c.args.join(" ")).join("\n")).not.toContain("model-registry.json");
    expect(readFileSync(stale, "utf8")).toBe('{"stale":true}');
  });

  it("executes exactly the planned steps for a fresh tree", () => {
    const dir = fixture();
    const { runner, calls } = recorder();

    ensurePackagingArtifacts(dir, runner);

    expect(calls).toEqual(planPackagingArtifacts(dir));
    expect(existsSync(join(dir, "dist"))).toBe(false); // the recorder did not build
  });
});
