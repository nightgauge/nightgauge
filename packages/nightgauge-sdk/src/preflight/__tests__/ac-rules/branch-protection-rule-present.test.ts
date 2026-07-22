import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm } from "node:fs/promises";
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
