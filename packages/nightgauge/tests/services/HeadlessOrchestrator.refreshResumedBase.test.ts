/**
 * HeadlessOrchestrator.refreshResumedBase.test.ts
 *
 * Issue #2962 — a resumed worktree is brought up to date with origin/<base>
 * through `nightgauge pr-stage refresh-base` before any stage runs, as the Go
 * scheduler does (#2897). A conflict stops the run before any stage
 * dispatches; a refresh that cannot be attempted is logged and the run goes on.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

const execFileMock = vi.hoisted(() => vi.fn());
const resolveMock = vi.hoisted(() => vi.fn());

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const { promisify } = await vi.importActual<typeof import("util")>("util");
  const fn = Object.assign((...args: unknown[]) => execFileMock(...args), {
    [promisify.custom]: (...args: unknown[]) => execFileMock(...args),
  });
  return { ...actual, execFile: fn };
});

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: resolveMock }) },
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "m", source: "stage-default" }),
}));

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  workspace: { workspaceFolders: [], getConfiguration: vi.fn(() => ({ get: vi.fn() })) },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

type Internals = {
  worktreeOverride: string | undefined;
  resumedAtPrMerge: boolean;
  refreshResumedBase(n: number): Promise<string | null>;
  baseRefreshStop(
    n: number,
    start: number
  ): Promise<{ success: boolean; failedStage?: string; error?: Error } | null>;
};

const SKIPPED = "Base refresh skipped — continuing on the tree as found";
const CONFLICT =
  "[merge-conflict] resumed worktree conflicts with origin/main in a.txt — resolve the merge by hand";

function reply(fields: Record<string, unknown>) {
  execFileMock.mockResolvedValue({
    stdout:
      JSON.stringify({
        base: "main",
        behind: 0,
        ahead: 0,
        outcome: "up-to-date",
        conflict_files: [],
        skipped: "",
        stop_reason: "",
        ...fields,
      }) + "\n",
    stderr: "",
  });
}

describe("HeadlessOrchestrator — resumed-worktree base refresh (#2962)", () => {
  let logger: Logger;
  let orch: Internals;

  beforeEach(() => {
    vi.clearAllMocks();
    resolveMock.mockResolvedValue("/bin/nightgauge");
    logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger;
    const state = { onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }) };
    orch = new HeadlessOrchestrator(state as unknown as PipelineStateService, logger, {
      contextFileWaitMs: 0,
    }) as unknown as Internals;
    orch.worktreeOverride = "/wt/issue-42";
  });

  it("calls the binary on the worktree and continues when up to date", async () => {
    reply({});
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(execFileMock).toHaveBeenCalledWith(
      "/bin/nightgauge",
      ["pr-stage", "refresh-base", "42", "--workdir", "/wt/issue-42"],
      expect.objectContaining({ cwd: "/wt/issue-42", timeout: 120_000 })
    );
    expect(logger.info).toHaveBeenCalledWith(
      "Resumed worktree base refresh",
      expect.objectContaining({ outcome: "up-to-date", behind: 0, base: "main" })
    );
  });

  it("continues after a fast-forward", async () => {
    reply({ behind: 2, outcome: "merged" });
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(logger.info).toHaveBeenCalledWith(
      "Resumed worktree base refresh",
      expect.objectContaining({ outcome: "merged", behind: 2 })
    );
  });

  it("a conflict stops the run before any stage dispatches", async () => {
    reply({
      behind: 1,
      ahead: 1,
      outcome: "conflict",
      conflict_files: ["a.txt"],
      stop_reason: CONFLICT,
    });
    await expect(orch.refreshResumedBase(42)).resolves.toBe(CONFLICT);

    const result = await orch.baseRefreshStop(42, Date.now());
    expect(result?.success).toBe(false);
    expect(result?.failedStage).toBe("pipeline-start");
    expect(result?.error?.message).toBe(CONFLICT);
    expect(result).not.toHaveProperty("startRefusal");
    expect((orch as unknown as { isRunning: boolean }).isRunning).toBe(false);
  });

  it("no stop when the refresh is clean", async () => {
    reply({ behind: 1, outcome: "merged" });
    await expect(orch.baseRefreshStop(42, Date.now())).resolves.toBeNull();
  });

  it("logs and continues when the refresh could not be attempted", async () => {
    reply({ skipped: "HEAD is not on a branch" });
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      "Base refresh skipped — continuing on the tree as found",
      expect.objectContaining({ reason: "HEAD is not on a branch" })
    );
  });

  it("logs and continues when the binary fails or prints garbage", async () => {
    execFileMock.mockRejectedValueOnce(new Error("timed out"));
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    execFileMock.mockResolvedValueOnce({ stdout: "not json", stderr: "" });
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(logger.warn).toHaveBeenNthCalledWith(
      1,
      SKIPPED,
      expect.objectContaining({ err: expect.any(Error) })
    );
    expect(logger.warn).toHaveBeenNthCalledWith(
      2,
      SKIPPED,
      expect.objectContaining({ err: expect.any(SyntaxError) })
    );
  });

  it("logs and continues when the binary cannot be found", async () => {
    resolveMock.mockResolvedValue(null);
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      SKIPPED,
      expect.objectContaining({ reason: "nightgauge binary not found" })
    );
    expect(execFileMock).not.toHaveBeenCalled();
  });

  it("never calls the binary without a worktree or on a pr-merge resume", async () => {
    orch.worktreeOverride = undefined;
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    orch.worktreeOverride = "/wt/issue-42";
    orch.resumedAtPrMerge = true;
    await expect(orch.refreshResumedBase(42)).resolves.toBeNull();
    expect(execFileMock).not.toHaveBeenCalled();
  });
});
