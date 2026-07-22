/**
 * #188 — runPipeline second line of defense: the per-issue in-flight
 * registry spans ALL orchestrator instances in the extension host, so a
 * duplicate dispatch through a second instance (the bowlsheet#233 shape —
 * the per-instance isRunning throw cannot see it) is refused with a failed
 * result instead of double-running.
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("HeadlessOrchestrator.runPipeline — duplicate dispatch refusal (#188)", () => {
  afterEach(() => {
    (HeadlessOrchestrator as any).activePipelineIssues.clear();
  });

  it("refuses when the issue already has a pipeline in flight on ANOTHER instance", async () => {
    const logger = createMockLogger();
    const second = new HeadlessOrchestrator(null, logger);

    // Simulate instance A mid-run for issue 233 (the registry entry is added
    // at runPipeline entry and removed in its finally).
    (HeadlessOrchestrator as any).activePipelineIssues.add(233);

    const result = await second.runPipeline(233);

    expect(result.success).toBe(false);
    expect(result.completedStages).toEqual([]);
    expect(result.error?.message).toContain("Duplicate dispatch refused");
    expect(logger.error).toHaveBeenCalledWith(
      expect.stringContaining("Duplicate runPipeline dispatch refused"),
      expect.objectContaining({ issueNumber: 233 })
    );
  });

  it("a refusal leaves the original registry entry intact", async () => {
    const second = new HeadlessOrchestrator(null, createMockLogger());
    (HeadlessOrchestrator as any).activePipelineIssues.add(233);

    await second.runPipeline(233);

    // The refusal path must not clear the REAL run's registration.
    expect((HeadlessOrchestrator as any).activePipelineIssues.has(233)).toBe(true);
  });

  it("does not refuse a different issue", async () => {
    (HeadlessOrchestrator as any).activePipelineIssues.add(233);
    // Registry check for a different issue passes through to the inner run —
    // assert only the registry gate here (the inner run is exercised by the
    // existing pipeline test suites): remove 234 immediately by observing
    // that the guard did not produce the refusal error.
    const orch = new HeadlessOrchestrator(null, createMockLogger());
    const result = await orch.runPipeline(234).catch((err: Error) => ({
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      error: err,
      totalDurationMs: 0,
    }));
    expect(result.error?.message ?? "").not.toContain("Duplicate dispatch refused");
    // finally-cleanup: 234 must not linger in the registry.
    expect((HeadlessOrchestrator as any).activePipelineIssues.has(234)).toBe(false);
  });
});
