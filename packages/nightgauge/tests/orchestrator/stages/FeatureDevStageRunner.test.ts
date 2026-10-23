/**
 * FeatureDevStageRunner unit tests
 *
 * Verifies that the feature-dev runner:
 * - Returns failure when planning-{N}.json prerequisite is missing
 * - Reads planning hints and passes adjusted sizeLabel to executeSkill
 * - Falls back to issueSizeLabel when no planning hints exist
 * - Enriches stateService.setMeta with planning complexity metrics
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition Part 1
 * @see Issue #1333 - Planning-aware budget enforcement
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { FeatureDevStageRunner } from "../../../src/orchestrator/stages/FeatureDevStageRunner";
import type { StageRunContext, StageRunResult } from "../../../src/orchestrator/stages/StageRunner";
import type { PipelineStateService } from "../../../src/services/PipelineStateService";

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return { ...actual, existsSync: vi.fn(), readFileSync: vi.fn() };
});

import * as fs from "fs";

const PREREQ_PATH = "/workspace/.nightgauge/pipeline/planning-42.json";

function makeSuccessResult(sizeLabel?: string): StageRunResult {
  return { success: true, stage: "feature-dev", durationMs: 10 };
}

function makeMockStateService(): PipelineStateService {
  return { setMeta: vi.fn() } as unknown as PipelineStateService;
}

function makeCtx(overrides?: Partial<StageRunContext>): StageRunContext {
  return {
    stage: "feature-dev",
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

/** Build planning-{N}.json content as a string */
function makePlanningContent(
  sizeLabel?: string,
  filesToCreate?: string[],
  filesToModify?: string[]
): string {
  return JSON.stringify({
    complexity_assessment: sizeLabel ? { size_label: sizeLabel } : undefined,
    files_to_create: filesToCreate ?? [],
    files_to_modify: filesToModify ?? [],
  });
}

describe("FeatureDevStageRunner", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("prerequisite validation", () => {
    it("returns success: false when planning-{N}.json is missing", async () => {
      vi.mocked(fs.existsSync).mockReturnValue(false);
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx();

      const result = await runner.run(ctx);

      expect(result.success).toBe(false);
      expect(ctx.executeSkill).not.toHaveBeenCalled();
    });

    it("returns error describing missing prerequisite when planning file absent", async () => {
      vi.mocked(fs.existsSync).mockReturnValue(false);
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx();

      const result = await runner.run(ctx);

      expect(result.error).toBeInstanceOf(Error);
      expect(result.error?.message).toContain("feature-dev");
      expect(result.error?.message).toContain("feature-planning");
    });

    it("checks the correct context path for the prerequisite", async () => {
      vi.mocked(fs.existsSync).mockReturnValue(false);
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx();

      await runner.run(ctx);

      expect(ctx.getContextPath).toHaveBeenCalledWith("planning", 42);
    });
  });

  describe("budget size adjustment from planning hints", () => {
    beforeEach(() => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
    });

    it("passes issueSizeLabel unchanged when planning file has no assessedSize", async () => {
      vi.mocked(fs.readFileSync).mockReturnValue(makePlanningContent(undefined, [], []));
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ issueSizeLabel: "M" });

      await runner.run(ctx);

      expect(ctx.executeSkill).toHaveBeenCalledWith({ sizeLabel: "M" });
    });

    it("passes upgraded sizeLabel when planner assessed higher complexity than issue label", async () => {
      // Issue label is "M", planner assessed "L" — expect "L"
      vi.mocked(fs.readFileSync).mockReturnValue(makePlanningContent("L", [], []));
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ issueSizeLabel: "M" });

      await runner.run(ctx);

      const callArg = vi.mocked(ctx.executeSkill).mock.calls[0]?.[0];
      expect(callArg?.sizeLabel).toBe("L");
    });

    it("does not downgrade sizeLabel when planner assessed lower complexity", async () => {
      // Issue label is "L", planner assessed "S" — resolveEffectiveSize should keep "L"
      vi.mocked(fs.readFileSync).mockReturnValue(makePlanningContent("S", [], []));
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ issueSizeLabel: "L" });

      await runner.run(ctx);

      const callArg = vi.mocked(ctx.executeSkill).mock.calls[0]?.[0];
      expect(callArg?.sizeLabel).toBe("L");
    });

    it("passes issueSizeLabel as sizeLabel when planning file read fails", async () => {
      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("read error");
      });
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ issueSizeLabel: "S" });

      await runner.run(ctx);

      expect(ctx.executeSkill).toHaveBeenCalledWith({ sizeLabel: "S" });
    });
  });

  describe("stateService.setMeta enrichment", () => {
    beforeEach(() => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
    });

    it("calls stateService.setMeta with complexity and file_count from planning hints", async () => {
      vi.mocked(fs.readFileSync).mockReturnValue(
        makePlanningContent("L", ["a.ts", "b.ts"], ["c.ts"])
      );
      const stateService = makeMockStateService();
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ stateService, issueSizeLabel: "M" });

      await runner.run(ctx);

      expect(stateService.setMeta).toHaveBeenCalledWith(
        expect.objectContaining({
          complexity: "L",
          file_count: 3, // 2 create + 1 modify
        })
      );
    });

    it("does not call stateService.setMeta when planning hints are absent", async () => {
      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("read error");
      });
      const stateService = makeMockStateService();
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ stateService });

      await runner.run(ctx);

      expect(stateService.setMeta).not.toHaveBeenCalled();
    });
  });

  describe("success path delegation", () => {
    beforeEach(() => {
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(fs.readFileSync).mockReturnValue(makePlanningContent("M", [], []));
    });

    it("returns the result from executeSkill unchanged", async () => {
      const expected: StageRunResult = { success: true, stage: "feature-dev", durationMs: 99 };
      const runner = new FeatureDevStageRunner();
      const ctx = makeCtx({ executeSkill: vi.fn().mockResolvedValue(expected) });

      const result = await runner.run(ctx);

      expect(result).toBe(expected);
    });
  });
});
