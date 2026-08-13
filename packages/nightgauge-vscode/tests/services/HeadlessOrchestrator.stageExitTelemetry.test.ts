/**
 * HeadlessOrchestrator.stageExitTelemetry.test.ts
 *
 * Regression guard for Issue #109: `recordStageExitDiagnostic()` passed literal
 * `undefined` for the exit code and every token/cost field, so 100% of the
 * exit records written through the IPC (VSCode) dispatch path landed on disk
 * with `"tokens": {}` and no `exit_code` — while the runtime state for the very
 * same stage showed millions of tokens and dollars of spend.
 *
 * These tests pin the forwarding contract at the TS boundary:
 *   - a token-consuming stage forwards non-zero token/cost figures,
 *   - a deterministic no-LLM stage still forwards nothing (zero is legitimate;
 *     values are never synthesized),
 *   - a signal-killed stage forwards `undefined` rather than a fake exit 0.
 *
 * @see docs/STAGE_EXIT_DIAGNOSTIC.md
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { StageRunResult } from "../../src/orchestrator/stages/StageRunner";
import type { Logger } from "../../src/utils/logger";

const recordStageExit = vi.fn().mockResolvedValue({ recorded: true });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: recordStageExit,
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

// Keep the real CLI out of the module graph — these tests never spawn a stage.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "default" }),
}));

/** Positional argument layout of `IpcClient.diagnosticsRecordStageExit`. */
const ARG = {
  stage: 2,
  success: 3,
  runId: 4,
  exitCode: 7,
  idleMsAtExit: 11,
  inputTokens: 12,
  outputTokens: 13,
  cacheReadTokens: 14,
  cacheCreationTokens: 15,
  costUsd: 16,
  signal: 17,
  signalSource: 18,
  sessionId: 19,
  lastBashCommand: 20,
  lastBashExit: 21,
  recentBash: 22,
  stopHookErrored: 23,
} as const;

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

type RecordStageExitDiagnostic = (
  stage: string,
  issueNumber: number,
  result: StageRunResult,
  stageStartTime: number
) => Promise<void>;

function makeOrch(): { recordStageExitDiagnostic: RecordStageExitDiagnostic } {
  const stateService = {
    getRunId: vi.fn().mockReturnValue("01900130-0000-7000-8000-000000000369"),
  };
  const orch = new HeadlessOrchestrator(stateService as never, makeLogger(), {
    contextFileWaitMs: 0,
  } as never);
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue("/tmp/ws");
  return orch as unknown as { recordStageExitDiagnostic: RecordStageExitDiagnostic };
}

describe("HeadlessOrchestrator.recordStageExitDiagnostic — token/cost forwarding (#109)", () => {
  let orch: ReturnType<typeof makeOrch>;

  beforeEach(() => {
    recordStageExit.mockClear();
    orch = makeOrch();
  });

  it("forwards a token-consuming stage's totals and exit code", async () => {
    const result: StageRunResult = {
      success: true,
      stage: "feature-planning",
      durationMs: 784194,
      exitTelemetry: {
        exitCode: 0,
        idleMsAtExit: 412,
        sessionId: "2166f3de-675d-4b11-be2f-4c0b640dce2a",
        tokenUsage: {
          inputTokens: 4937274,
          outputTokens: 49252,
          cacheReadTokens: 4937180,
          cacheCreationTokens: 61204,
          costUsd: 3.7963349,
        },
      },
    };

    await orch.recordStageExitDiagnostic("feature-planning", 307, result, Date.now() - 784194);

    expect(recordStageExit).toHaveBeenCalledTimes(1);
    const args = recordStageExit.mock.calls[0];
    expect(args[ARG.stage]).toBe("feature-planning");
    expect(args[ARG.success]).toBe(true);
    expect(args[ARG.runId]).toBe("01900130-0000-7000-8000-000000000369");
    // A real exit 0 must survive as 0 — Go stores it in a *int so "clean exit"
    // stays distinguishable from "never observed".
    expect(args[ARG.exitCode]).toBe(0);
    expect(args[ARG.inputTokens]).toBe(4937274);
    expect(args[ARG.outputTokens]).toBe(49252);
    expect(args[ARG.cacheReadTokens]).toBe(4937180);
    expect(args[ARG.cacheCreationTokens]).toBe(61204);
    expect(args[ARG.costUsd]).toBeCloseTo(3.7963349, 6);
    expect(args[ARG.idleMsAtExit]).toBe(412);
    expect(args[ARG.sessionId]).toBe("2166f3de-675d-4b11-be2f-4c0b640dce2a");
  });

  it("forwards the kill-path forensic anchors alongside the burn", async () => {
    const result: StageRunResult = {
      success: false,
      stage: "feature-dev",
      durationMs: 1200000,
      error: new Error("[stall-killed] no output for 15m"),
      exitTelemetry: {
        // A SIGKILLed process never produced an exit code.
        exitCode: null,
        signal: "SIGKILL",
        signalSource: "stall-kill",
        tokenUsage: {
          inputTokens: 812345,
          outputTokens: 9120,
          cacheReadTokens: 800000,
          cacheCreationTokens: 4096,
          costUsd: 1.42,
        },
      },
    };

    await orch.recordStageExitDiagnostic("feature-dev", 307, result, Date.now() - 1200000);

    const args = recordStageExit.mock.calls[0];
    expect(args[ARG.exitCode]).toBeUndefined();
    expect(args[ARG.signal]).toBe("SIGKILL");
    expect(args[ARG.signalSource]).toBe("stall-kill");
    // The whole point of #296 + #109: a killed stage records its real burn.
    expect(args[ARG.inputTokens]).toBe(812345);
    expect(args[ARG.costUsd]).toBe(1.42);
  });

  // `diagnosticsRecordStageExit` is generated with POSITIONAL parameters from
  // the Go struct's field order, so inserting a field mid-struct silently
  // shifts every argument after it. This pins the ring's slot and the two
  // neighbours that would absorb the shift — the same class of quiet
  // layer-skip that #154 was. (#156)
  it("forwards the Bash ring to the exit record, in its own argument slot", async () => {
    const result: StageRunResult = {
      success: false,
      stage: "feature-validate",
      durationMs: 60000,
      error: new Error("[validation-failed] feature-validate reported failure"),
      exitTelemetry: {
        exitCode: 0,
        lastBashCommand: "true",
        lastBashExit: 0,
        recentBash: [
          { cmd: "npm run -w nightgauge-vscode vitest run", exit: 1 },
          { cmd: "true", exit: 0 },
        ],
        stopHookErrored: false,
      },
    };

    await orch.recordStageExitDiagnostic("feature-validate", 156, result, Date.now() - 60000);

    const args = recordStageExit.mock.calls[0];
    expect(args[ARG.lastBashCommand]).toBe("true");
    expect(args[ARG.lastBashExit]).toBe(0);
    expect(args[ARG.recentBash]).toEqual([
      { cmd: "npm run -w nightgauge-vscode vitest run", exit: 1 },
      { cmd: "true", exit: 0 },
    ]);
    // If the ring landed in stopHookErrored's slot, this would be an array.
    expect(args[ARG.stopHookErrored]).toBe(false);
  });

  it("records zero for a deterministic no-LLM stage instead of synthesizing", async () => {
    const result: StageRunResult = {
      success: true,
      stage: "pipeline-start",
      durationMs: 12,
    };

    await orch.recordStageExitDiagnostic("pipeline-start", 307, result, Date.now() - 12);

    const args = recordStageExit.mock.calls[0];
    expect(args[ARG.exitCode]).toBeUndefined();
    expect(args[ARG.inputTokens]).toBeUndefined();
    expect(args[ARG.outputTokens]).toBeUndefined();
    expect(args[ARG.cacheReadTokens]).toBeUndefined();
    expect(args[ARG.cacheCreationTokens]).toBeUndefined();
    expect(args[ARG.costUsd]).toBeUndefined();
  });
});
