/**
 * HeadlessOrchestrator.worktreeCleanup.test.ts
 *
 * Verifies the firePipelineComplete completion funnel calls
 * WorktreeManager.cleanup() for every terminal outcome (success, failure,
 * blocked) but skips it for a deferred run, and that branch deletion is
 * only ever requested when the PR is forge-confirmed merged. See Issue #106.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  HeadlessOrchestrator,
  type PipelineRunResult,
} from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

const cleanupMock = vi.fn().mockResolvedValue(undefined);

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn().mockImplementation(function WorktreeManager() {
    return { cleanup: cleanupMock };
  }),
}));

function makeLogger(): Logger {
  return {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  } as unknown as Logger;
}

function baseResult(overrides: Partial<PipelineRunResult> = {}): PipelineRunResult {
  return {
    success: true,
    completedStages: [],
    skippedStages: [],
    deferredStages: [],
    totalDurationMs: 1000,
    ...overrides,
  } as PipelineRunResult;
}

describe("HeadlessOrchestrator worktree cleanup wiring (#106)", () => {
  let orch: HeadlessOrchestrator;

  beforeEach(() => {
    cleanupMock.mockClear();
    orch = new HeadlessOrchestrator(null, makeLogger());
  });

  it("cleans up (worktree only) on a successful run without a confirmed merge", () => {
    (
      orch as unknown as { firePipelineComplete: (r: PipelineRunResult, n?: number) => void }
    ).firePipelineComplete(baseResult({ success: true }), 42);

    expect(cleanupMock).toHaveBeenCalledWith(42, false);
  });

  it("requests branch deletion when prMergedGroundTruth is true", () => {
    (orch as unknown as { prMergedGroundTruth: boolean }).prMergedGroundTruth = true;

    (
      orch as unknown as { firePipelineComplete: (r: PipelineRunResult, n?: number) => void }
    ).firePipelineComplete(baseResult({ success: true }), 42);

    expect(cleanupMock).toHaveBeenCalledWith(42, true);
  });

  it("still cleans up the worktree on a failed run (not only on success)", () => {
    (
      orch as unknown as { firePipelineComplete: (r: PipelineRunResult, n?: number) => void }
    ).firePipelineComplete(
      baseResult({
        success: false,
        failedStage: "feature-dev" as PipelineRunResult["failedStage"],
      }),
      42
    );

    expect(cleanupMock).toHaveBeenCalledWith(42, false);
  });

  it("skips cleanup entirely for a deferred run", () => {
    (
      orch as unknown as { firePipelineComplete: (r: PipelineRunResult, n?: number) => void }
    ).firePipelineComplete(baseResult({ success: false, deferred: true }), 42);

    expect(cleanupMock).not.toHaveBeenCalled();
  });

  it("skips cleanup when no issue number is available", () => {
    (
      orch as unknown as { firePipelineComplete: (r: PipelineRunResult, n?: number) => void }
    ).firePipelineComplete(baseResult({ success: true }), undefined);

    expect(cleanupMock).not.toHaveBeenCalled();
  });
});
