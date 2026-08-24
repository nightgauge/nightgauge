/**
 * runStage.runIdentity.test.ts
 *
 * ADR-017 (#370) — the `nightgauge.runStage` command as a DISPATCH.
 *
 * Two invariants, both previously unpinned at the command level:
 *
 * 1. Decision 10 — the command drives the singleton's raw notify sites
 *    directly (it does not go through `HeadlessOrchestrator.runStage`; it
 *    spawns the stage itself), so it must RECEIVE OR MINT an identity, and
 *    release what it minted at the STAGE's terminal — not at the end of the
 *    command, which returns while the stage is still running.
 * 2. §7.2 (D8) — exactly ONE `running` transition per attempt, carrying the
 *    pid the synchronous spawn callback captured.
 *
 * @see docs/decisions/017-runtime-identity-keying.md
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

const CHILD_PID = 515151;

const {
  mockRegisterCommand,
  mockShowErrorMessage,
  mockShowInformationMessage,
  mockShowInputBox,
  mockRunStageSkillHeadless,
} = vi.hoisted(() => ({
  mockRegisterCommand: vi.fn(),
  mockShowErrorMessage: vi.fn(),
  mockShowInformationMessage: vi.fn(),
  mockShowInputBox: vi.fn(),
  mockRunStageSkillHeadless: vi.fn(),
}));

vi.mock("vscode", () => ({
  window: {
    showErrorMessage: mockShowErrorMessage,
    showInformationMessage: mockShowInformationMessage,
    showWarningMessage: vi.fn(),
    showQuickPick: vi.fn(),
    showInputBox: mockShowInputBox,
  },
  commands: { registerCommand: mockRegisterCommand, executeCommand: vi.fn() },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
    getConfiguration: () => ({ get: vi.fn() }),
  },
}));

vi.mock("../../src/utils/skillRunner", () => ({
  runStageSkillHeadless: mockRunStageSkillHeadless,
  runStageSkillInteractive: vi.fn(),
  getStageLabel: vi.fn((s: string) => s),
  sendInputToActiveProcess: vi.fn(),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getDefaultStageExecutionMode: vi.fn(() => "headless"),
  getExecutionAdapter: vi.fn(() => "claude"),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn().mockResolvedValue({ owner: "nightgauge", repo: "nightgauge" }),
}));

import { registerRunStageCommand } from "../../src/commands/runStage";
import type { PipelineStateService } from "../../src/services/PipelineStateService";

function makeStateService(overrides: Record<string, unknown> = {}) {
  let installed: string | null = null;
  const svc = {
    getRunId: vi.fn(() => installed),
    // PipelineStateService.beginRun is (runId, repo, issueNumber); the double
    // declared only the first parameter, so a faithful three-argument call
    // read as an arity error the moment this file was typechecked (#499).
    beginRun: vi.fn((runId: string, _repo: string, _issueNumber: number) => {
      if (installed !== null) throw new Error(`Issue #370 is already running (run x…).`);
      installed = runId;
    }),
    endRun: vi.fn((runId: string) => {
      if (installed === runId) installed = null;
    }),
    getState: vi.fn().mockResolvedValue({ issue_number: 370, stages: {} }),
    getExecutionMode: vi.fn().mockResolvedValue("manual"),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    isPaused: vi.fn().mockResolvedValue(false),
    resumePipeline: vi.fn().mockResolvedValue(true),
    validateStageTransition: vi.fn().mockResolvedValue({ allowed: true }),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    failStage: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
    updateTokens: vi.fn().mockResolvedValue(undefined),
    recordToolCall: vi.fn(),
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
    ...overrides,
    /** Test-only view of what the command left installed. */
    _installed: () => installed,
  };
  return svc;
}

function makeDeps() {
  return {
    logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
    statusBar: {
      showRunning: vi.fn(),
      showComplete: vi.fn(),
      showError: vi.fn(),
      showIdle: vi.fn(),
    },
    treeProvider: {
      getCurrentIssueNumber: vi.fn((): number | undefined => 370),
      updateStageStatus: vi.fn(),
    },
    outputWindow: {
      reveal: vi.fn(),
      setIssueNumber: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
      showToolSummary: vi.fn(),
      setMode: vi.fn(),
      addToolCall: vi.fn(),
      updateToolCall: vi.fn(),
    },
  };
}

function register(svc: unknown) {
  let handler!: (item?: PipelineStage) => Promise<void>;
  mockRegisterCommand.mockImplementation((_n: string, h: (...a: unknown[]) => unknown) => {
    handler = h as (item?: PipelineStage) => Promise<void>;
    return { dispose: vi.fn() };
  });
  const deps = makeDeps();
  registerRunStageCommand(
    null,
    deps.logger as never,
    deps.statusBar as never,
    deps.treeProvider as never,
    deps.outputWindow as never,
    svc as unknown as PipelineStateService
  );
  return { handler, deps };
}

describe("nightgauge.runStage — receives or mints, and books ONE running transition", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("mints an identity, sends ONE running transition with the pid, releases at the stage terminal", async () => {
    const svc = makeStateService();
    let complete!: (r: unknown) => Promise<void>;
    mockRunStageSkillHeadless.mockImplementation((_s: string, _i: number, callbacks: any) => {
      callbacks?.onStageChildSpawned?.(CHILD_PID);
      complete = callbacks.onComplete;
      return { process: { pid: CHILD_PID } };
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);

    // MINTED, and the stage is emitting under it while it runs.
    expect(svc.beginRun).toHaveBeenCalledTimes(1);
    expect(svc._installed()).not.toBeNull();
    expect(svc.endRun).not.toHaveBeenCalled();

    // ONE running transition, carrying the child's pid.
    expect(svc.startStage).toHaveBeenCalledTimes(1);
    expect(svc.startStage.mock.calls[0][0]).toBe("feature-dev");
    expect(svc.startStage.mock.calls[0][1]).toMatchObject({ stagePid: CHILD_PID });

    // The stage terminal is what releases.
    await complete({ success: true });
    expect(svc.endRun).toHaveBeenCalledTimes(1);
    expect(svc._installed()).toBeNull();
  });

  it("RECEIVES an already-installed identity and never releases it", async () => {
    // A stage run driven while a pipeline owns the singleton must not end that
    // pipeline's run when this single stage finishes.
    const svc = makeStateService();
    svc.beginRun("019fe7dd-0000-7000-8000-000000000001", "nightgauge/nightgauge", 370);
    svc.beginRun.mockClear();

    let complete!: (r: unknown) => Promise<void>;
    mockRunStageSkillHeadless.mockImplementation((_s: string, _i: number, callbacks: any) => {
      callbacks?.onStageChildSpawned?.(CHILD_PID);
      complete = callbacks.onComplete;
      return { process: { pid: CHILD_PID } };
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);
    await complete({ success: true });

    expect(svc.beginRun).not.toHaveBeenCalled();
    expect(svc.endRun).not.toHaveBeenCalled();
    expect(svc._installed()).toBe("019fe7dd-0000-7000-8000-000000000001");
  });

  it("releases when the launch fails before a child exists", async () => {
    const svc = makeStateService();
    mockRunStageSkillHeadless.mockImplementation(() => {
      throw new Error("spawn failed");
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);

    expect(svc.beginRun).toHaveBeenCalledTimes(1);
    expect(svc.endRun).toHaveBeenCalledTimes(1);
    expect(svc._installed()).toBeNull();
  });

  it("dispatches with a strictly valid prompted issue number", async () => {
    const svc = makeStateService();
    mockShowInputBox.mockResolvedValue("793");
    mockRunStageSkillHeadless.mockReturnValue({ process: { pid: CHILD_PID } });

    const { handler, deps } = register(svc);
    deps.treeProvider.getCurrentIssueNumber.mockReturnValue(undefined);
    await handler("feature-dev" as PipelineStage);

    expect(mockRunStageSkillHeadless.mock.calls[0]?.slice(0, 3)).toEqual([
      "feature-dev",
      793,
      expect.any(Object),
    ]);
  });

  it.each(["12junk", "1.5", "-4"])(
    "rejects malformed prompted issue number %s without dispatching",
    async (input) => {
      const svc = makeStateService();
      mockShowInputBox.mockResolvedValue(input);

      const { handler, deps } = register(svc);
      deps.treeProvider.getCurrentIssueNumber.mockReturnValue(undefined);
      await handler("feature-dev" as PipelineStage);

      expect(mockRunStageSkillHeadless).not.toHaveBeenCalled();
      expect(mockShowErrorMessage).toHaveBeenCalledWith(
        "Please enter a valid positive issue number"
      );
    }
  );
});
