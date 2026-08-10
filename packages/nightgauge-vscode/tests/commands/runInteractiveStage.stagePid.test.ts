/**
 * runInteractiveStage.stagePid.test.ts
 *
 * ADR-017 (#370) §7.2 / C18 — the interactive stage driver is a POPULATION, and
 * a ladder arm no run in a population writes is not a fallback for it.
 *
 * This command drives real pipeline stages through the singleton's raw notify
 * sites, and its traffic profile is the thinnest in the product: ONE `running`
 * transition, then nothing at all until the stage ends, because a conversation
 * emits no token stream. So thirty minutes in, arm 1 (the lease) is stale, arm 4
 * (the file's age) is stale with it — nothing re-persists between transitions —
 * arm 2 is structurally false (not a scheduler run) and arm 5 expired after 120
 * seconds. Exactly one arm is left: the pid of the child the spawn returned.
 *
 * Sending that transition BEFORE the spawn, with no pid, is what made the whole
 * population invisible to the ladder: a live interactive session past the
 * window is reconciled as an orphan — terminal `pipeline_done(success=false)`,
 * snapshot removed, stages and cost lost.
 *
 * @see docs/decisions/017-runtime-identity-keying.md §7.2
 * @see internal/ipc/pipeline_orphan_reconcile.go (the consumer)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

const CHILD_PID = 626262;

const { mockRegisterCommand, mockRunStageSkillInteractive, mockGetExecutionAdapter } = vi.hoisted(
  () => ({
    mockRegisterCommand: vi.fn(),
    mockRunStageSkillInteractive: vi.fn(),
    mockGetExecutionAdapter: vi.fn(() => "claude"),
  })
);

vi.mock("vscode", () => ({
  window: {
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(() => ({ then: (cb: (v?: string) => void) => cb(undefined) })),
    showWarningMessage: vi.fn(),
    showQuickPick: vi.fn(),
    showInputBox: vi.fn(),
  },
  commands: { registerCommand: mockRegisterCommand, executeCommand: vi.fn() },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
    getConfiguration: () => ({ get: vi.fn() }),
  },
}));

vi.mock("../../src/utils/skillRunner", () => ({
  runStageSkillInteractive: mockRunStageSkillInteractive,
  getStageLabel: vi.fn((s: string) => s),
  getNextStage: vi.fn(() => undefined),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getExecutionAdapter: mockGetExecutionAdapter,
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn().mockResolvedValue({ owner: "nightgauge", repo: "nightgauge" }),
}));

import { registerRunInteractiveStageCommand } from "../../src/commands/runInteractiveStage";
import type { PipelineStateService } from "../../src/services/PipelineStateService";

function makeStateService() {
  let installed: string | null = null;
  return {
    getRunId: vi.fn(() => installed),
    beginRun: vi.fn((runId: string) => {
      if (installed !== null) throw new Error("already running");
      installed = runId;
    }),
    endRun: vi.fn((runId: string) => {
      if (installed === runId) installed = null;
    }),
    getExecutionMode: vi.fn().mockResolvedValue("manual"),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    isPaused: vi.fn().mockResolvedValue(false),
    resumePipeline: vi.fn().mockResolvedValue(true),
    validateStageTransition: vi.fn().mockResolvedValue({ allowed: true }),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    failStage: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
    _installed: () => installed,
  };
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
      getCurrentIssueNumber: vi.fn(() => 370),
      updateStageStatus: vi.fn(),
    },
    outputWindow: {
      reveal: vi.fn(),
      setIssueNumber: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
      setMode: vi.fn(),
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
  registerRunInteractiveStageCommand(
    deps.logger as never,
    deps.statusBar as never,
    deps.treeProvider as never,
    deps.outputWindow as never,
    svc as unknown as PipelineStateService
  );
  return { handler, deps };
}

/** Let the fire-and-forget transition's continuation run. */
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("nightgauge.runInteractiveStage — the running transition carries the stage child's pid", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetExecutionAdapter.mockReturnValue("claude");
  });

  it("sends ONE running transition, after the spawn, carrying the pid", async () => {
    const svc = makeStateService();
    mockRunStageSkillInteractive.mockImplementation((_s: string, _i: number, callbacks: any) => {
      // The spawn surfaces the child synchronously, exactly as the headless
      // path does (skillRunner's onStageChildSpawned contract).
      callbacks?.onStageChildSpawned?.(CHILD_PID);
      return { process: { pid: CHILD_PID }, writeToStdin: vi.fn() };
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);
    await flush();

    expect(svc.startStage).toHaveBeenCalledTimes(1);
    expect(svc.startStage.mock.calls[0][0]).toBe("feature-dev");
    // The whole point: without this the interactive population has NO ladder
    // arm at all past the liveness window.
    expect(svc.startStage.mock.calls[0][1]).toMatchObject({ stagePid: CHILD_PID });
    expect(svc.setStageExecutionMode).toHaveBeenCalledWith("feature-dev", "interactive");
  });

  it("sends the transition after the spawn, not before it", async () => {
    const order: string[] = [];
    const svc = makeStateService();
    svc.startStage.mockImplementation(async () => {
      order.push("startStage");
    });
    mockRunStageSkillInteractive.mockImplementation((_s: string, _i: number, callbacks: any) => {
      order.push("spawn");
      callbacks?.onStageChildSpawned?.(CHILD_PID);
      return { process: { pid: CHILD_PID }, writeToStdin: vi.fn() };
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);
    await flush();

    expect(order).toEqual(["spawn", "startStage"]);
  });

  it("sends NO running transition when the launch fails before a child exists", async () => {
    // An error stub (no process, no stdin writer) means the stage never
    // started. A `running` transition here would leave Go holding a stage
    // nothing will ever complete — and it is what the pre-spawn call did.
    const svc = makeStateService();
    mockRunStageSkillInteractive.mockImplementation((_s: string, _i: number, callbacks: any) => {
      callbacks?.onComplete?.({ success: false, exitCode: null, error: new Error("no adapter") });
      return { process: null, kill: vi.fn() };
    });

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);
    await flush();

    expect(svc.startStage).not.toHaveBeenCalled();
    expect(svc._installed()).toBeNull();
  });

  it("omits stagePid on the Codex TUI sub-path, which spawns no child in this host", async () => {
    // #4024 launches the Codex TUI in a VSCode terminal: the extension host
    // never holds a ChildProcess, so there is no stage-child pid to name. The
    // field is OMITTED rather than sent as 0 — `stagePid: 0` is the wire's way
    // of saying "no child is executing this run", which is the arm-3 answer
    // this sub-path cannot honestly give either way.
    mockGetExecutionAdapter.mockReturnValue("codex");
    const svc = makeStateService();
    mockRunStageSkillInteractive.mockImplementation(() => ({
      process: null,
      writeToStdin: vi.fn(),
    }));

    const { handler } = register(svc);
    await handler("feature-dev" as PipelineStage);
    await flush();

    expect(svc.startStage).toHaveBeenCalledTimes(1);
    expect(svc.startStage.mock.calls[0][1]).toEqual({});
  });
});
