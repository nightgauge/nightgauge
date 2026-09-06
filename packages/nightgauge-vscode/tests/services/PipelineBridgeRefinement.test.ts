/**
 * PipelineBridgeRefinement.test.ts — the refinement stage on the Go↔TS bridge.
 *
 * The autonomous scheduler dispatches refinement over the same
 * `pipeline.runStage` wire every pipeline stage uses, and holds its refinement
 * semaphore slot until `pipeline.stageResult` comes back. Before #1529 the
 * extension had no handling for it at all, so refinement was a CLI-only
 * feature while the product default said it was on.
 *
 * @see Issue #1529 — refinement has no execution path in extension (IPC) mode
 * @see Issue #503 — the slot is held to completion, in every mode
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockRunStage = vi.fn();
const mockAbort = vi.fn();
const mockIpcCall = vi.fn();
const skillRunnerInstances: Array<{ runStage: typeof mockRunStage; abort: typeof mockAbort }> = [];

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      command: undefined,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
  },
  StatusBarAlignment: { Left: 1, Right: 2 },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }] },
}));

vi.mock("../../src/services/SkillRunner", () => ({
  SkillRunner: vi.fn(function () {
    const instance = {
      runStage: mockRunStage,
      abort: mockAbort,
      isRunning: false,
    };
    skillRunnerInstances.push(instance);
    return instance;
  }),
}));

import { PipelineBridge } from "../../src/services/PipelineBridge";
import { Logger } from "../../src/utils/logger";

function makeIpcClient() {
  const handlers = new Map<string, (data: unknown) => void>();
  return {
    on: vi.fn((event: string, handler: (data: unknown) => void) => {
      handlers.set(event, handler);
      return { dispose: vi.fn() };
    }),
    call: mockIpcCall,
    emit: (event: string, data: unknown) => {
      handlers.get(event)?.(data);
    },
  };
}

/** The envelope Go's refineViaStageRunner emits (internal/orchestrator/autonomous.go). */
function makeRefinementParams(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    stage: "issue-refine",
    issueNumber: 531,
    model: "sonnet",
    timeoutMs: 300_000,
    // The TARGET repo's checkout, resolved by the Go scheduler — the daemon is
    // multi-repo, so this is not the launch root.
    worktreeDir: "/repos/acme-flutter",
    repo: "acme/acme-flutter",
    runId: "01890a5d-ac96-774b-bcce-b302099a8057",
    ...overrides,
  };
}

describe("PipelineBridge — refinement stage (#1529)", () => {
  let ipc: ReturnType<typeof makeIpcClient>;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    skillRunnerInstances.length = 0;
    ipc = makeIpcClient();
    logger = new Logger("PipelineBridgeRefinement-Test");
    mockIpcCall.mockResolvedValue({});
    mockRunStage.mockResolvedValue({
      success: true,
      exitCode: 0,
      inputTokens: 1200,
      outputTokens: 340,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.02,
      durationMs: 1000,
    });
  });

  it("runs the refine skill in the target repo checkout and answers with a stage result", async () => {
    new PipelineBridge(ipc as any, logger);

    ipc.emit("pipeline.runStage", makeRefinementParams());

    await vi.waitFor(() => {
      expect(mockRunStage).toHaveBeenCalledTimes(1);
    });

    expect(mockRunStage.mock.calls[0][0]).toMatchObject({
      stage: "issue-refine",
      issueNumber: 531,
      model: "sonnet",
      // The refinement runs where the issue lives, not where the daemon was
      // launched — the whole point of Go resolving the repo root.
      worktreeDir: "/repos/acme-flutter",
      repo: "acme/acme-flutter",
      runId: "01890a5d-ac96-774b-bcce-b302099a8057",
    });

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({
          stage: "issue-refine",
          issueNumber: 531,
          success: true,
          exitCode: 0,
          inputTokens: 1200,
          outputTokens: 340,
        })
      );
    });
  });

  it("reports a failed refinement instead of leaving Go holding the slot", async () => {
    mockRunStage.mockResolvedValue({
      success: false,
      exitCode: 2,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
      durationMs: 10,
      errorText: "[stall-killed] issue-refine terminated",
    });

    new PipelineBridge(ipc as any, logger);
    ipc.emit("pipeline.runStage", makeRefinementParams());

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({
          stage: "issue-refine",
          success: false,
          exitCode: 2,
          errorText: "[stall-killed] issue-refine terminated",
        })
      );
    });
  });

  it("still answers when the executor throws — a silent swallow parks a refinement slot", async () => {
    mockRunStage.mockRejectedValue(new Error("spawn failed"));

    new PipelineBridge(ipc as any, logger);
    ipc.emit("pipeline.runStage", makeRefinementParams());

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({
          stage: "issue-refine",
          success: false,
          exitCode: 1,
          errorText: expect.stringContaining("spawn failed"),
        })
      );
    });
  });

  it("does not drive the pipeline stage UI — refinement is not a pipeline stage", async () => {
    const statusBar = { showRunning: vi.fn(), showComplete: vi.fn(), showError: vi.fn() };
    const treeProvider = { updateStageStatus: vi.fn() };
    const outputWindow = {
      show: vi.fn(),
      setIssueNumber: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
      addStallWarning: vi.fn(),
      removeStallWarnings: vi.fn(),
    };

    new PipelineBridge(
      ipc as any,
      logger,
      null,
      null,
      null,
      outputWindow as any,
      statusBar as any,
      treeProvider as any
    );

    ipc.emit("pipeline.runStage", makeRefinementParams());

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ stage: "issue-refine" })
      );
    });

    expect(statusBar.showRunning).not.toHaveBeenCalled();
    expect(treeProvider.updateStageStatus).not.toHaveBeenCalled();
    expect(outputWindow.updateStageStatus).not.toHaveBeenCalled();
  });

  it("aborts only the pipeline executor — refinement runs on its own handle", async () => {
    new PipelineBridge(ipc as any, logger);

    // Two executors: the pipeline's and refinement's. One shared handle would
    // let pipeline.abort kill a refinement (and vice versa).
    expect(skillRunnerInstances).toHaveLength(2);

    ipc.emit("pipeline.abort", { issueNumber: 531, reason: "context_cancelled" });

    expect(skillRunnerInstances[0].abort).toHaveBeenCalledTimes(1);
  });
});
