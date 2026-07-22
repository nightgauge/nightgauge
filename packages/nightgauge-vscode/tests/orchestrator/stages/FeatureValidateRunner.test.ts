/**
 * FeatureValidateRunner unit tests
 *
 * Verifies that the feature-validate runner:
 * - Returns failure when dev-{N}.json prerequisite is missing
 * - Delegates to ctx.executeSkill() when prerequisite exists
 * - Does not apply any budget size adjustment
 *
 * @see Issue #2769 — HeadlessOrchestrator decomposition Part 2
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { FeatureValidateRunner } from "../../../src/orchestrator/stages/FeatureValidateRunner";
import type { StageRunContext, StageRunResult } from "../../../src/orchestrator/stages/StageRunner";

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return { ...actual, existsSync: vi.fn() };
});

import * as fs from "fs";

const PREREQ_PATH = "/workspace/.nightgauge/pipeline/dev-42.json";

function makeSuccessResult(): StageRunResult {
  return { success: true, stage: "feature-validate", durationMs: 10 };
}

function makeCtx(overrides?: Partial<StageRunContext>): StageRunContext {
  return {
    stage: "feature-validate",
    issueNumber: 42,
    workspaceRoot: "/workspace",
    issueSizeLabel: "M",
    stateService: undefined,
    logger: undefined,
    executeSkill: vi.fn().mockResolvedValue(makeSuccessResult()),
    getContextPath: vi.fn().mockReturnValue(PREREQ_PATH),
    ...overrides,
  };
}

describe("FeatureValidateRunner", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("when prerequisite dev-{N}.json exists", () => {
    beforeEach(() => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
    });

    it("delegates to ctx.executeSkill", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      await runner.run(ctx);

      expect(ctx.executeSkill).toHaveBeenCalledOnce();
    });

    it("passes empty options to executeSkill (no sizeLabel override)", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      await runner.run(ctx);

      expect(ctx.executeSkill).toHaveBeenCalledWith({});
    });

    it("returns the result from executeSkill unchanged", async () => {
      const runner = new FeatureValidateRunner();
      const expected = makeSuccessResult();
      const ctx = makeCtx({ executeSkill: vi.fn().mockResolvedValue(expected) });

      const result = await runner.run(ctx);

      expect(result).toBe(expected);
    });
  });

  describe("when prerequisite dev-{N}.json is missing", () => {
    beforeEach(() => {
      vi.mocked(fs.existsSync).mockReturnValue(false);
    });

    it("returns success: false without calling executeSkill", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      const result = await runner.run(ctx);

      expect(result.success).toBe(false);
      expect(ctx.executeSkill).not.toHaveBeenCalled();
    });

    it("returns an error describing the missing prerequisite", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      const result = await runner.run(ctx);

      expect(result.error).toBeInstanceOf(Error);
      expect(result.error?.message).toContain("feature-validate");
      expect(result.error?.message).toContain("feature-dev");
    });

    it("includes the correct stage in the failure result", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      const result = await runner.run(ctx);

      expect(result.stage).toBe("feature-validate");
    });

    it("checks the correct context path for the prerequisite", async () => {
      const runner = new FeatureValidateRunner();
      const ctx = makeCtx();

      await runner.run(ctx);

      expect(ctx.getContextPath).toHaveBeenCalledWith("dev", 42);
    });
  });
});
