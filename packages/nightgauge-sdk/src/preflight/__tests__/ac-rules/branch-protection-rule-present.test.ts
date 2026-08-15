import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import branchProtectionRule from "../../ac-rules/branch-protection-rule-present.js";

describe("branch-protection-rule-present rule", () => {
  describe("applies()", () => {
    it("matches branch protection on main with required check", () => {
      const r = branchProtectionRule.applies(
        "Branch protection on `main` requires required check `build`"
      );
      expect(r).toEqual({ requiredCheck: "build" });
    });

    it("matches branch protection without specific check", () => {
      const r = branchProtectionRule.applies("Branch protection on main is enabled");
      expect(r).toEqual({ requiredCheck: "" });
    });

    it("returns null when AC does not reference branch protection", () => {
      expect(branchProtectionRule.applies("Job `build` runs on main")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    let workdir: string;

    beforeEach(async () => {
      workdir = await mkdtemp(path.join(os.tmpdir(), "ac-bp-"));
    });

    afterEach(async () => {
      await rm(workdir, { recursive: true, force: true });
    });

    it("classifies undetectable when gh cannot resolve the repo", async () => {
      // Empty workdir is not a git repo and `gh repo view` will fail.
      const r = await branchProtectionRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { requiredCheck: "build" },
      });
      expect(r.classification).toBe("undetectable");
      expect(r.reason).toMatch(/gh|repo|not authenticated/i);
    });
  });
});

describe("gh invocation is bounded (#562 follow-up)", () => {
  // Regression pin for the defect that turned the deterministic AC-reconcile
  // integration test into a coin flip on CI: `runCommand` spawned `gh` and
  // awaited 'close' with no ceiling, so the rule's cost was whatever the
  // network happened to cost that minute. Unbounded here means unbounded in
  // production preflight too — this rule must be able to answer
  // "undetectable", never to hang.
  let binDir: string;
  let workdir: string;
  let realPath: string | undefined;

  beforeEach(async () => {
    binDir = await mkdtemp(path.join(os.tmpdir(), "bp-slow-gh-"));
    workdir = await mkdtemp(path.join(os.tmpdir(), "bp-slow-wd-"));
    // A `gh` that never returns. `sleep` is a child of this shell, so it also
    // reproduces the orphaned-grandchild case that kept the event loop alive
    // even after the child itself was killed.
    await writeFile(path.join(binDir, "gh"), "#!/bin/sh\nsleep 60\n", { mode: 0o755 });
    realPath = process.env.PATH;
    process.env.PATH = `${binDir}:${process.env.PATH ?? ""}`;
  });

  afterEach(async () => {
    if (realPath === undefined) delete process.env.PATH;
    else process.env.PATH = realPath;
    await rm(binDir, { recursive: true, force: true });
    await rm(workdir, { recursive: true, force: true });
  });

  it("gives up on a hanging gh instead of waiting for it", async () => {
    const started = Date.now();
    const res = await branchProtectionRule.evaluate({
      workdir,
      ac: {
        index: 0,
        text: "Branch protection on `main` requires required check `build`",
        checkbox_state: "unchecked",
      },
      extracted: { requiredCheck: "build" },
    });
    const elapsed = Date.now() - started;

    // Two chained calls at a 2s ceiling each is the worst case; 60s is what
    // the unbounded version cost. Anything at or above 30s means the ceiling
    // is gone, whatever the classification says.
    expect(elapsed).toBeLessThan(30_000);
    expect(res.classification).toBe("undetectable");
  }, 40_000);
});
