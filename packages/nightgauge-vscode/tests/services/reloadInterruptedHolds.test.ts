/**
 * #2339 and #2357: a paused run a window reload ended, held by exactly one
 * window of the machine.
 *
 * Every window on a worktree of one clone scans the same paused snapshots
 * (the pipeline state directory is the clone's), so when such windows
 * activate together each finds the same run. Only the window that claims it
 * in the machine's ledger holds it: the platform gets one answer per verb, a
 * resume is refused with the reason, and a cancel ends the run by consuming
 * its snapshot. A claim whose window is gone is taken over.
 *
 * The windows share a real RemoteRunLedger directory and run real pipeline
 * managers and run-verb handlers; only the snapshot fields and the process
 * liveness are fixtures.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (data: any) => this.listeners.forEach((l) => l(data));
    dispose = vi.fn();
  },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test-repo" } }] },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: { executeCommand: vi.fn().mockResolvedValue(undefined) },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return { getRepoRoot: vi.fn().mockReturnValue("/test-repo") };
  }),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({ maxConcurrent: 1 }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { RemoteRunLedger } from "../../src/services/RemoteRunLedger";
import { RunVerbCommandHandler } from "../../src/services/RunVerbCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import {
  ReloadInterruptedRunHolds,
  reloadInterruptedRemoteRun,
} from "../../src/utils/reloadInterruptedRun";

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "reload-interrupted-holds-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() };
}

/** The paused snapshot both windows find: its owning daemon died with the reload. */
function writeSnapshot(): string {
  const pipelineDir = path.join(dir, "clone-pipeline");
  fs.mkdirSync(pipelineDir, { recursive: true });
  const file = path.join(pipelineDir, "runtime-2339-0190a0b0-0000-7000-8000-000000000000.json");
  fs.writeFileSync(
    file,
    JSON.stringify({ paused: true, issueNumber: 2339, remoteRunId: "run-2339", ownerPid: 999_999 })
  );
  return file;
}

/** One editor window: its ledger view, holds, pipeline manager and verb handler. */
function makeWindow(
  pid: number,
  platform: { agentAcknowledgeCommand: ReturnType<typeof vi.fn> },
  isAlive: (pid: number) => boolean = () => true
) {
  const ledger = new RemoteRunLedger(path.join(dir, "ledger"), { windowId: pid, isAlive });
  const holds = new ReloadInterruptedRunHolds(ledger);
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    {
      getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
      removeRemoteRun: vi.fn().mockResolvedValue(false),
    } as any,
    vi.fn(),
    makeLogger() as any,
    { maxConcurrent: 1 }
  );
  manager.onHeldRemoteRunsChanged((runIds) => void ledger.publish(runIds));
  const handler = new RunVerbCommandHandler(
    manager,
    platform as never,
    makeLogger() as never,
    undefined,
    undefined,
    { ledger, graceMs: 1 }
  );
  handler.setAgentId("agent-machine");
  /** The activation scan finding the snapshot, as bootstrap/services.ts runs it. */
  const scan = async (file: string): Promise<boolean> => {
    const runtime = JSON.parse(fs.readFileSync(file, "utf8"));
    const interrupted = reloadInterruptedRemoteRun(runtime, () => false);
    if (!interrupted) return false;
    return holds.found(interrupted, async () => {
      await fs.promises.unlink(file).catch((err: NodeJS.ErrnoException) => {
        if (err.code !== "ENOENT") throw err;
      });
    });
  };
  /** The pipeline manager is created after the scan, then the ledger listing starts. */
  const activate = async () => {
    holds.attach(manager);
    await ledger.publish(manager.heldRemoteRunIds());
  };
  return { ledger, holds, manager, handler, scan, activate };
}

function verb(type: "cancel" | "pause" | "resume", id = `cmd-${type}`): ReceivedCommand {
  return {
    id,
    type,
    payload: { runId: "run-2339" },
    createdAt: "2026-10-02T00:00:00.000Z",
    owner: "acme",
    repo: "api",
  };
}

describe("a paused run a window reload ended, held by one window (#2339)", () => {
  it("is held by one of two windows that scan its snapshot together, which alone answers", async () => {
    const platform = { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
    const file = writeSnapshot();
    const a = makeWindow(101, platform);
    const b = makeWindow(102, platform);

    const [heldByA, heldByB] = await Promise.all([a.scan(file), b.scan(file)]);
    expect([heldByA, heldByB].filter(Boolean)).toHaveLength(1);
    await Promise.all([a.activate(), b.activate()]);
    const [holder, other] = heldByA ? [a, b] : [b, a];
    expect(await holder.manager.holdsRemoteRun("run-2339")).toBe(true);
    expect(await other.manager.holdsRemoteRun("run-2339")).toBe(false);
    expect(other.manager.heldRemoteRunIds()).toEqual([]);

    // A platform resume is refused once, with the reason, by the holder.
    await Promise.all([
      a.handler.consume(verb("resume"), "resume"),
      b.handler.consume(verb("resume"), "resume"),
    ]);
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0].slice(1)).toEqual([
      "cmd-resume",
      "rejected",
      "resume-in-window: a window reload ended the paused run; only its window can resume it",
    ]);

    // A pause finds it paused already.
    await Promise.all([
      a.handler.consume(verb("pause"), "pause"),
      b.handler.consume(verb("pause"), "pause"),
    ]);
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(2);
    expect(platform.agentAcknowledgeCommand.mock.calls[1][2]).toBe("already_resolved");

    // A platform cancel ends the run: its snapshot is consumed, so no Resume
    // prompt or later activation brings it back, and nobody holds it.
    await Promise.all([
      a.handler.consume(verb("cancel"), "cancel"),
      b.handler.consume(verb("cancel"), "cancel"),
    ]);
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(3);
    expect(platform.agentAcknowledgeCommand.mock.calls[2].slice(1, 3)).toEqual([
      "cmd-cancel",
      "applied",
    ]);
    expect(fs.existsSync(file)).toBe(false);
    expect(await holder.manager.holdsRemoteRun("run-2339")).toBe(false);
    // The claim was given up with it.
    expect(await other.ledger.claimRun("run-2339")).toBe(true);
  });

  it("is taken over from a window that is gone, and given up by the window's Resume", async () => {
    const platform = { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
    const file = writeSnapshot();
    const gone = makeWindow(101, platform);
    expect(await gone.scan(file)).toBe(true);

    // The window that claimed it reloaded: its process is gone.
    const back = makeWindow(103, platform, (pid) => pid !== 101);
    expect(await back.scan(file)).toBe(true);
    await back.activate();
    expect(await back.manager.holdsRemoteRun("run-2339")).toBe(true);

    // Its Resume starts a new run, which does not serve the platform run.
    await back.holds.resumed("run-2339");
    expect(await back.manager.holdsRemoteRun("run-2339")).toBe(false);
    const another = makeWindow(104, platform);
    expect(await another.ledger.claimRun("run-2339")).toBe(true);
  });
});
