/**
 * IssuePickupStageRunner unit tests
 *
 * Verifies that the issue-pickup runner:
 * - Delegates directly to ctx.executeSkill() with no adjustments
 * - Returns the result from executeSkill unchanged
 *
 * @see Issue #2768 — HeadlessOrchestrator decomposition Part 1
 */

import { describe, it, expect, vi } from "vitest";
import { IssuePickupStageRunner } from "../../../src/orchestrator/stages/IssuePickupStageRunner";
import type { StageRunContext, StageRunResult } from "../../../src/orchestrator/stages/StageRunner";

function makeSuccessResult(): StageRunResult {
  return { success: true, stage: "issue-pickup", durationMs: 10 };
}

function makeCtx(overrides?: Partial<StageRunContext>): StageRunContext {
  return {
    stage: "issue-pickup",
    issueNumber: 42,
    workspaceRoot: "/workspace",
    issueSizeLabel: "M",
    stateService: undefined,
    logger: undefined,
    executeSkill: vi.fn().mockResolvedValue(makeSuccessResult()),
    getContextPath: vi.fn().mockReturnValue("/workspace/.nightgauge/pipeline/issue-42.json"),
    ...overrides,
  };
}

describe("IssuePickupStageRunner", () => {
  it("delegates directly to ctx.executeSkill", async () => {
    const runner = new IssuePickupStageRunner();
    const ctx = makeCtx();

    await runner.run(ctx);

    expect(ctx.executeSkill).toHaveBeenCalledOnce();
  });

  it("passes empty options to executeSkill (no sizeLabel override)", async () => {
    const runner = new IssuePickupStageRunner();
    const ctx = makeCtx();

    await runner.run(ctx);

    expect(ctx.executeSkill).toHaveBeenCalledWith({});
  });

  it("returns the result from executeSkill unchanged", async () => {
    const runner = new IssuePickupStageRunner();
    const expected = makeSuccessResult();
    const ctx = makeCtx({ executeSkill: vi.fn().mockResolvedValue(expected) });

    const result = await runner.run(ctx);

    expect(result).toBe(expected);
  });

  it("does not call getContextPath (no prerequisite check)", async () => {
    const runner = new IssuePickupStageRunner();
    const ctx = makeCtx();

    await runner.run(ctx);

    expect(ctx.getContextPath).not.toHaveBeenCalled();
  });
});
