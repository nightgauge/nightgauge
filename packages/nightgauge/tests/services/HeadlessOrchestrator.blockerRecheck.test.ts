/**
 * HeadlessOrchestrator.blockerRecheck.test.ts
 *
 * Issue #3618 — a stage-declared blocked verdict that names a CLOSED blocker
 * gets one base refresh through `nightgauge pr-stage recheck-blocker`, as the
 * Go scheduler does (#2897 ADR-003): re-run when the base advanced, stop
 * merge_conflict on a conflict, otherwise book the verdict with a note.
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

type Action =
  { action: "rerun" } | { action: "stop"; reason: string } | { action: "book"; note: string };
type Internals = {
  worktreeOverride: string | undefined;
  blockerRechecked: boolean;
  recheckClosedBlockers(
    n: number,
    signal: { rationale: string; evidence: string[] }
  ): Promise<Action>;
};

const SIGNAL = { rationale: "BLOCKED — depends on #77", evidence: ["acme/lib#5 not on main"] };
const CONFLICT =
  "[merge-conflict] resumed worktree conflicts with origin/main in a.go — resolve the merge by hand";

function reply(fields: Record<string, unknown>) {
  execFileMock.mockResolvedValue({
    stdout:
      JSON.stringify({
        ran: true,
        rerun: false,
        closed: ["#77"],
        note: "",
        stop_reason: "",
        conflict_files: [],
        ...fields,
      }) + "\n",
    stderr: "",
  });
}

describe("HeadlessOrchestrator — closed named blocker re-check (#3618)", () => {
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

  it("re-runs the stage when the base advanced, and only once per run", async () => {
    reply({ rerun: true });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({ action: "rerun" });
    expect(execFileMock).toHaveBeenCalledWith(
      "/bin/nightgauge",
      [
        "pr-stage",
        "recheck-blocker",
        "42",
        "--workdir",
        "/wt/issue-42",
        "--detail",
        "BLOCKED — depends on #77\nacme/lib#5 not on main",
      ],
      expect.objectContaining({ cwd: "/wt/issue-42" })
    );
    // A second blocked outcome in the same run books without another call.
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(execFileMock).toHaveBeenCalledTimes(1);
  });

  it("an up-to-date base books the verdict with the note", async () => {
    const note = " (named blocker(s) #77 closed; base already contains origin/main)";
    reply({ note });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({ action: "book", note });
  });

  it("a refresh that could not run books with its note", async () => {
    const note = " (named blocker(s) #77 closed; base refresh could not run)";
    reply({ note });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({ action: "book", note });
  });

  it("a conflict stops the run with the merge-conflict text", async () => {
    reply({ stop_reason: CONFLICT, conflict_files: ["a.go"] });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "stop",
      reason: CONFLICT,
    });
  });

  it("open blockers (ran:false) book unchanged and leave the re-check available", async () => {
    reply({ ran: false, closed: [] });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(orch.blockerRechecked).toBe(false);
  });

  it("a spawn error books the verdict unchanged", async () => {
    execFileMock.mockRejectedValue(new Error("ETIMEDOUT"));
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(logger.warn).toHaveBeenCalled();
  });

  it("a missing binary books unchanged without spawning", async () => {
    resolveMock.mockResolvedValue(null);
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(execFileMock).not.toHaveBeenCalled();
  });

  it("malformed output books unchanged and leaves the re-check available", async () => {
    execFileMock.mockResolvedValue({ stdout: "not json", stderr: "" });
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(orch.blockerRechecked).toBe(false);
  });

  it("spawns nothing without a worktree", async () => {
    orch.worktreeOverride = undefined;
    await expect(orch.recheckClosedBlockers(42, SIGNAL)).resolves.toEqual({
      action: "book",
      note: "",
    });
    expect(execFileMock).not.toHaveBeenCalled();
  });
});
