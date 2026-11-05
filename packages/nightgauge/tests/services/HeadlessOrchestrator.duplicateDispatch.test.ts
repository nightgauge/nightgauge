/**
 * #188 — runPipeline second line of defense: the per-issue in-flight
 * registry spans ALL orchestrator instances in the extension host, so a
 * duplicate dispatch through a second instance (the dogfood pr-merge deadlock shape —
 * the per-instance isRunning throw cannot see it) is refused with a failed
 * result instead of double-running.
 */

import { describe, it, expect, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// #2693 — hermetic: no case may spawn a process. Offline, the real inner run
// shelled out to `gh`/`git` and blew the timeout; any spawn now fails loudly.
function throwingChildProcess(original: Record<string, unknown>) {
  const fns = ["exec", "execSync", "execFile", "execFileSync", "spawn", "spawnSync", "fork"];
  const mocked: Record<string, unknown> = { ...original };
  for (const fn of fns) {
    mocked[fn] = vi.fn(() => {
      throw new Error(`hermetic test: child_process.${fn} called`);
    });
  }
  return { ...mocked, default: mocked };
}
vi.mock("child_process", async (importOriginal) =>
  throwingChildProcess(await importOriginal<Record<string, unknown>>())
);
vi.mock("node:child_process", async (importOriginal) =>
  throwingChildProcess(await importOriginal<Record<string, unknown>>())
);

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
    vi.restoreAllMocks();
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
    let registeredDuringRun = false;
    const success = {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 0,
    };
    // Stub the inner run: this case asserts only the registry gate.
    const inner = vi
      .spyOn(HeadlessOrchestrator.prototype as any, "runPipelineInner")
      .mockImplementation(async () => {
        registeredDuringRun = (HeadlessOrchestrator as any).activePipelineIssues.has(234);
        return success;
      });
    const orch = new HeadlessOrchestrator(null, createMockLogger());

    const result = await orch.runPipeline(234);

    expect(inner).toHaveBeenCalledTimes(1);
    expect(inner.mock.calls[0][0]).toBe(234);
    expect(result).toBe(success);
    expect(registeredDuringRun).toBe(true);
    // finally-cleanup: 234 must not linger in the registry.
    expect((HeadlessOrchestrator as any).activePipelineIssues.has(234)).toBe(false);
  });
});
