/**
 * AgentCommandDispatcher.test.ts
 *
 * Pins #2334: every command type the platform's router delivers to an agent
 * is acknowledged, exactly once, by the handlers the extension actually wires
 * (the real TriggerCommandHandler and RunVerbCommandHandler, over fakes for
 * the IPC client and the run manager). Also pins the daemon relay (#2335): a
 * relayed command is handled and acknowledged under the agent it was
 * addressed to.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

vi.mock("vscode", () => ({}));

import {
  AgentCommandDispatcher,
  AGENT_COMMAND_RELAY_EVENT,
  ROUTER_DELIVERED_COMMAND_TYPES,
  subscribeToDaemonRelay,
} from "../../src/services/AgentCommandDispatcher";
import { TriggerCommandHandler } from "../../src/services/TriggerCommandHandler";
import { RunVerbCommandHandler } from "../../src/services/RunVerbCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import type { RemoteVerbResult } from "../../src/services/ConcurrentPipelineManager";

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

function makeIpc() {
  return {
    agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "platform-run-1" }),
    issueView: vi.fn().mockResolvedValue({ title: "An issue", labels: [] }),
    queueValidatePin: vi.fn().mockResolvedValue({ ok: true }),
  };
}

/** A run manager whose every verb answers `verbResult`. */
function makeRuns(verbResult: RemoteVerbResult) {
  return {
    isRunning: vi.fn().mockReturnValue(false),
    setPendingRemoteRunId: vi.fn(),
    clearPendingRemoteRunId: vi.fn(),
    fillSlots: vi.fn().mockResolvedValue(undefined),
    cancelByRemoteRunId: vi.fn().mockResolvedValue(verbResult),
    approveByRemoteRunId: vi.fn().mockReturnValue(verbResult),
    rejectByRemoteRunId: vi.fn().mockReturnValue(verbResult),
    pauseByRemoteRunId: vi.fn().mockResolvedValue(verbResult),
    resumeByRemoteRunId: vi.fn().mockResolvedValue(verbResult),
  };
}

function build(verbResult: RemoteVerbResult) {
  const ipc = makeIpc();
  const runs = makeRuns(verbResult);
  const queue = { enqueue: vi.fn().mockResolvedValue({ issueNumber: 7 }) };
  const logger = makeLogger();
  const trigger = new TriggerCommandHandler(
    ipc as never,
    runs as never,
    queue as never,
    logger as never
  );
  const verbs = new RunVerbCommandHandler(runs as never, ipc as never, logger as never);
  const dispatcher = new AgentCommandDispatcher(trigger, verbs, ipc as never, logger as never);
  dispatcher.setAgentId("agent-ext");
  return { dispatcher, ipc, runs, queue };
}

/** A command of `type` as the platform's router publishes it. */
function routedCommand(type: string, n: number): ReceivedCommand {
  return {
    id: `cmd-${type}-${n}`,
    type,
    payload:
      type === "trigger"
        ? { owner: "acme", repo: "api", issueNumber: 7 }
        : { runId: "platform-run-1", stage: "pr-merge", gateType: "merge", reason: "no" },
    createdAt: "2026-10-01T00:00:00.000Z",
    owner: "acme",
    repo: "api",
  };
}

/** Let every fire-and-forget handler run to completion. */
async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0));
}

describe("AgentCommandDispatcher", () => {
  beforeEach(() => vi.clearAllMocks());

  // The platform's router delivers exactly these: a `trigger`, and the five
  // verbs a client issues against an existing run. A new routed type must be
  // added here AND handled.
  it("knows every command type the platform's router delivers", () => {
    expect([...ROUTER_DELIVERED_COMMAND_TYPES].sort()).toEqual(
      ["approve", "cancel", "pause", "reject", "resume", "trigger"].sort()
    );
  });

  it.each([["applied"], ["no-active-run"]] as Array<[RemoteVerbResult]>)(
    "acknowledges every router-delivered command exactly once (verbs %s)",
    async (verbResult) => {
      const { dispatcher, ipc } = build(verbResult);
      const sent = ROUTER_DELIVERED_COMMAND_TYPES.map((type, n) => routedCommand(type, n));

      for (const cmd of sent) dispatcher.handle(cmd);
      await settle();

      const ackedIds = ipc.agentAcknowledgeCommand.mock.calls.map((c) => c[1]);
      expect(ackedIds.sort()).toEqual(sent.map((c) => c.id).sort());
      for (const call of ipc.agentAcknowledgeCommand.mock.calls) {
        expect(call[0]).toBe("agent-ext");
      }
      const verbOutcomes = ipc.agentAcknowledgeCommand.mock.calls
        .filter((c) => !String(c[1]).startsWith("cmd-trigger"))
        .map((c) => c[2]);
      expect(new Set(verbOutcomes)).toEqual(
        new Set([verbResult === "applied" ? "applied" : "rejected"])
      );
    }
  );

  // The platform delivers at least once: a command published while the
  // stream replays its backlog arrives twice, and an unacknowledged one again
  // on every reconnect. Each is still carried out once and acknowledged once.
  it("acknowledges every router-delivered command once when each arrives twice", async () => {
    const { dispatcher, ipc, runs, queue } = build("applied");
    // The platform keeps the first ack of a command and refuses any later one.
    const accepted: string[] = [];
    ipc.agentAcknowledgeCommand.mockImplementation(async (_agentId: string, id: string) => {
      if (accepted.includes(id)) throw new Error("HTTP 409: already acknowledged");
      accepted.push(id);
      return { runId: "platform-run-1" };
    });
    const sent = [
      ...ROUTER_DELIVERED_COMMAND_TYPES.map((type, n) => routedCommand(type, n)),
      { ...routedCommand("throttle", 9), payload: { action: "cleared" } },
    ];

    for (const cmd of sent) dispatcher.handle(cmd);
    for (const cmd of sent) dispatcher.handle({ ...cmd });
    await settle();

    expect(accepted.sort()).toEqual(sent.map((c) => c.id).sort());
    // No verb was applied twice, and no refusal was sent twice.
    for (const verb of [
      runs.cancelByRemoteRunId,
      runs.approveByRemoteRunId,
      runs.rejectByRemoteRunId,
      runs.pauseByRemoteRunId,
      runs.resumeByRemoteRunId,
    ]) {
      expect(verb).toHaveBeenCalledTimes(1);
    }
    const throttleAcks = ipc.agentAcknowledgeCommand.mock.calls.filter(
      (c) => c[1] === "cmd-throttle-9"
    );
    expect(throttleAcks).toHaveLength(1);
    // The trigger's second copy is refused by the platform, so it starts nothing.
    expect(queue.enqueue).toHaveBeenCalledTimes(1);
  });

  it("acknowledges an unsupported command type once, as rejected", async () => {
    const { dispatcher, ipc } = build("applied");
    dispatcher.handle({ ...routedCommand("throttle", 0), payload: { action: "cleared" } });
    await settle();

    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    const [agentId, id, outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
    expect([agentId, id, outcome]).toEqual(["agent-ext", "cmd-throttle-0", "rejected"]);
    expect(detail).toMatch(/^unsupported-command: /);
  });

  it("handles a command the daemon relayed, acknowledging under the daemon's agent", async () => {
    const { dispatcher, ipc, runs, queue } = build("applied");
    const frame = {
      commandId: "cmd-relayed-1",
      type: "trigger",
      commandType: "trigger",
      payload: { owner: "acme", repo: "api", issueNumber: 7 },
      owner: "acme",
      repo: "api",
      issueNumber: 7,
      createdAt: "2026-10-01T00:00:00.000Z",
    };

    dispatcher.handleRelayed({ agentId: "agent-daemon", frame });
    await settle();

    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand.mock.calls[0].slice(0, 2)).toEqual([
      "agent-daemon",
      "cmd-relayed-1",
    ]);
    expect(queue.enqueue).toHaveBeenCalledTimes(1);
    expect(runs.fillSlots).toHaveBeenCalledTimes(1);

    dispatcher.handleRelayed({
      agentId: "agent-daemon",
      frame: {
        ...frame,
        commandId: "cmd-relayed-2",
        type: "cancel",
        commandType: "cancel",
        payload: { runId: "r" },
      },
    });
    await settle();
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(2);
    expect(ipc.agentAcknowledgeCommand.mock.calls[1]).toEqual([
      "agent-daemon",
      "cmd-relayed-2",
      "applied",
      undefined,
    ]);
  });

  // The daemon's side of the relay writes exactly the line in this fixture
  // (internal/ipc TestAgentCommandEvent_MatchesTheSharedFixture); IPC codegen
  // covers methods, not events, so the shared file is what pins the shape.
  it("handles the agent.command event the daemon emits, through the relay subscription", async () => {
    const fixture = JSON.parse(
      readFileSync(
        resolve(__dirname, "../../../../internal/ipc/testdata/agent-command-event.json"),
        "utf-8"
      )
    ) as { event: string; data: unknown };
    expect(fixture.event).toBe(AGENT_COMMAND_RELAY_EVENT);

    const { dispatcher, ipc, runs } = build("applied");
    const handlers = new Map<string, (data: unknown) => void>();
    const ipcEvents = {
      on: vi.fn((event: string, handler: (data: unknown) => void) => {
        handlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
    };
    const subscription = subscribeToDaemonRelay(ipcEvents, dispatcher);
    expect(typeof subscription.dispose).toBe("function");

    handlers.get(fixture.event)?.(fixture.data);
    await settle();

    expect(runs.pauseByRemoteRunId).toHaveBeenCalledWith("run-7");
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand.mock.calls[0]).toEqual([
      "agent-daemon",
      "cmd-relay-1",
      "applied",
      undefined,
    ]);
  });

  it("drops a malformed relay event without acknowledging anything", async () => {
    const { dispatcher, ipc } = build("applied");
    dispatcher.handleRelayed(null);
    dispatcher.handleRelayed({ agentId: "", frame: { commandId: "x", type: "cancel" } });
    dispatcher.handleRelayed({ agentId: "agent-daemon", frame: "not json" });
    await settle();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });
});
