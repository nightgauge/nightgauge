/**
 * #2357: every run a window answers a verb for is listed in the machine's
 * ledger, so another window never refuses it ahead of the holder.
 *
 * A window can hold a run only through its queue: after a reload the queue
 * the daemon kept still carries a triggered run, while the window's memory
 * holds nothing. When the holder's copy of a verb arrives after another
 * window's grace, only the listing keeps that window from refusing it first.
 *
 * Real pipeline managers, run-verb handlers and a shared RemoteRunLedger
 * directory; the queue is a fixture.
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

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "held-run-listing-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() };
}

/** A window whose queue holds `queued` (items with their platform run ids). */
function makeWindow(
  pid: number,
  platform: { agentAcknowledgeCommand: ReturnType<typeof vi.fn> },
  queued: Array<{ issueNumber: number; remoteRunId: string }>
) {
  const waiting = queued.map((q) => ({
    ...q,
    title: `Issue #${q.issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: "2026-10-02T00:00:00.000Z",
    repoName: "acme/api",
  }));
  const queueService = {
    getQueue: vi.fn(async () => ({ items: [...waiting], status: "waiting" })),
    removeRemoteRun: vi.fn(async (remoteRunId: string) => {
      const i = waiting.findIndex((w) => w.remoteRunId === remoteRunId);
      if (i < 0) return false;
      waiting.splice(i, 1);
      return true;
    }),
  };
  const ledger = new RemoteRunLedger(dir, { windowId: pid, isAlive: () => true });
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
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
  /** What bootstrap/services.ts does when it wires the ledger. */
  const wire = async () => {
    await ledger.publish(manager.heldRemoteRunIds());
    await manager.syncQueuedRemoteRuns();
    await ledger.publish(manager.heldRemoteRunIds());
  };
  return { ledger, manager, handler, waiting, wire };
}

function cancel(id = "cmd-cancel"): ReceivedCommand {
  return {
    id,
    type: "cancel",
    payload: { runId: "run-9" },
    createdAt: "2026-10-02T00:00:00.000Z",
    owner: "acme",
    repo: "api",
  };
}

describe("a run held only through the queue after a reload (#2357)", () => {
  it("is listed, so the platform gets only the holder's answer, however late its copy", async () => {
    const platform = { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
    const holder = makeWindow(101, platform, [{ issueNumber: 9, remoteRunId: "run-9" }]);
    const other = makeWindow(102, platform, []);
    await holder.wire();
    await other.wire();
    expect(holder.manager.heldRemoteRunIds()).toEqual(["run-9"]);

    // The other window's copy arrives first, and its grace passes.
    await other.handler.consume(cancel(), "cancel");
    expect(platform.agentAcknowledgeCommand).not.toHaveBeenCalled();

    // The holder's copy arrives late; the cancel applies to the queued run.
    await holder.handler.consume(cancel(), "cancel");
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0].slice(1, 3)).toEqual([
      "cmd-cancel",
      "applied",
    ]);
    expect(holder.waiting).toEqual([]);
  });

  it("is refused once when no window holds it any more", async () => {
    const platform = { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
    const a = makeWindow(101, platform, []);
    const b = makeWindow(102, platform, []);
    await a.wire();
    await b.wire();

    await Promise.all([
      a.handler.consume(cancel(), "cancel"),
      b.handler.consume(cancel(), "cancel"),
    ]);
    expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(platform.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
  });
});
