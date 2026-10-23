/**
 * HeadlessOrchestrator.failureDetail.test.ts
 *
 * #1329: a terminal failure before any stage persists no reason unless the
 * completion funnel forwards the error text. The Go notifyComplete handler
 * persists `failureDetail` as the run record's terminal_failure_detail and
 * writes a `pre-dispatch` exit record from it — but only if the extension
 * puts it on the wire. These tests pin that firePipelineComplete forwards
 * `result.error.message` and sends nothing on success.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  HeadlessOrchestrator,
  type PipelineRunResult,
} from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn().mockImplementation(function WorktreeManager() {
    return { cleanup: vi.fn().mockResolvedValue(undefined) };
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

type Funnel = { firePipelineComplete: (r: PipelineRunResult, n?: number) => void };

describe("HeadlessOrchestrator forwards the terminal failure reason (#1329)", () => {
  let orch: HeadlessOrchestrator;
  const notifyPipelineComplete = vi.fn().mockResolvedValue(undefined);

  beforeEach(() => {
    notifyPipelineComplete.mockClear();
    orch = new HeadlessOrchestrator(null, makeLogger());
    (orch as unknown as { stateService: unknown }).stateService = {
      finalizeRun: vi.fn(),
      notifyPipelineComplete,
    };
  });

  it("puts result.error.message on the wire as failureDetail for a pre-stage failure", () => {
    const err = new Error("ENOENT: no such file or directory, open '.nightgauge/config.yaml'");
    (orch as unknown as Funnel).firePipelineComplete(
      baseResult({ success: false, error: err }),
      1329
    );

    expect(notifyPipelineComplete).toHaveBeenCalledTimes(1);
    expect(notifyPipelineComplete.mock.calls[0][0]).toMatchObject({
      success: false,
      failureDetail: err.message,
    });
  });

  it("sends no failureDetail on success", () => {
    (orch as unknown as Funnel).firePipelineComplete(baseResult({ success: true }), 1329);

    expect(notifyPipelineComplete).toHaveBeenCalledTimes(1);
    expect(notifyPipelineComplete.mock.calls[0][0].failureDetail).toBeUndefined();
  });
});
