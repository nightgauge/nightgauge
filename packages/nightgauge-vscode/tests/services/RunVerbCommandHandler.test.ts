/**
 * RunVerbCommandHandler.test.ts
 *
 * Every run verb the handler consumes is acknowledged exactly once, and the
 * ack's outcome tells an applied verb from a no-op (#2334).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({}));

import {
  RunVerbCommandHandler,
  RUN_VERB_COMMAND_TYPES,
  type RunVerbTarget,
} from "../../src/services/RunVerbCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import type { RemoteVerbResult } from "../../src/services/ConcurrentPipelineManager";

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

/** Every verb on the run manager answers `result`. */
function makeRuns(result: RemoteVerbResult): {
  [K in keyof RunVerbTarget]: ReturnType<typeof vi.fn>;
} {
  return {
    cancelByRemoteRunId: vi.fn().mockResolvedValue(result),
    approveByRemoteRunId: vi.fn().mockReturnValue(result),
    rejectByRemoteRunId: vi.fn().mockReturnValue(result),
    pauseByRemoteRunId: vi.fn().mockResolvedValue(result),
    resumeByRemoteRunId: vi.fn().mockResolvedValue(result),
  };
}

function makeIpc() {
  return { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
}

function verbCmd(type: string, extra: Partial<ReceivedCommand> = {}): ReceivedCommand {
  return {
    id: `cmd-${type}`,
    type,
    payload: { runId: "run-1" },
    createdAt: "2026-10-01T00:00:00.000Z",
    owner: "acme",
    repo: "api",
    ...extra,
  };
}

const METHOD: Record<string, keyof RunVerbTarget> = {
  cancel: "cancelByRemoteRunId",
  approve: "approveByRemoteRunId",
  reject: "rejectByRemoteRunId",
  pause: "pauseByRemoteRunId",
  resume: "resumeByRemoteRunId",
};

describe("RunVerbCommandHandler", () => {
  let ipc: ReturnType<typeof makeIpc>;
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    ipc = makeIpc();
    logger = makeLogger();
  });

  it.each(RUN_VERB_COMMAND_TYPES)(
    "an applied %s is acknowledged once, as applied",
    async (verb) => {
      const runs = makeRuns("applied");
      const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
      handler.setAgentId("agent-ext");

      await handler.consume(verbCmd(verb), verb);

      expect(runs[METHOD[verb]]).toHaveBeenCalledWith("run-1");
      expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
        "agent-ext",
        `cmd-${verb}`,
        "applied",
        undefined
      );
    }
  );

  it.each(RUN_VERB_COMMAND_TYPES)(
    "a %s with no local run is acknowledged once, as a rejected no-op",
    async (verb) => {
      const handler = new RunVerbCommandHandler(
        makeRuns("no-active-run") as never,
        ipc,
        logger as never
      );
      handler.setAgentId("agent-ext");

      await handler.consume(verbCmd(verb), verb);

      expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      const [, , outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
      expect(outcome).toBe("rejected");
      expect(detail).toMatch(/^no-active-run: /);
    }
  );

  it("names each kind of no-op in the ack's reason", async () => {
    const cases: Array<[RemoteVerbResult, string, RegExp]> = [
      ["no-waiting-gate", "approve", /^no-waiting-gate: /],
      ["already-paused", "pause", /^already-paused: /],
      ["not-paused", "resume", /^not-paused: /],
      ["no-run-state", "pause", /^no-run-state: /],
    ];
    for (const [result, verb, reason] of cases) {
      const ack = makeIpc();
      const handler = new RunVerbCommandHandler(makeRuns(result) as never, ack, logger as never);
      handler.setAgentId("agent-ext");
      await handler.consume(verbCmd(verb), verb as never);
      expect(ack.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      expect(ack.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
      expect(ack.agentAcknowledgeCommand.mock.calls[0][3]).toMatch(reason);
    }
  });

  it("acknowledges a payload with no runId once, as rejected, and applies nothing", async () => {
    const runs = makeRuns("applied");
    const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("cancel", { payload: {} }), "cancel");

    expect(runs.cancelByRemoteRunId).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-ext",
      "cmd-cancel",
      "rejected",
      "invalid-payload: runId is required"
    );
  });

  it("acknowledges a verb that throws once, as rejected, without leaking the error", async () => {
    const runs = makeRuns("applied");
    runs.cancelByRemoteRunId.mockRejectedValue(new Error("/Users/someone/secret path"));
    const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("cancel"), "cancel");

    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    const [, , outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
    expect(outcome).toBe("rejected");
    expect(detail).not.toContain("/Users");
  });

  it("acknowledges a relayed verb under the agent the platform addressed", async () => {
    const handler = new RunVerbCommandHandler(makeRuns("applied") as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("pause", { agentId: "agent-daemon" }), "pause");

    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-daemon",
      "cmd-pause",
      "applied",
      undefined
    );
  });

  it("leaves a verb for a repo not open in this window to the window that has it", async () => {
    const runs = makeRuns("no-active-run");
    const workspace = { findRepositoryByGitHub: vi.fn().mockReturnValue(undefined) };
    const handler = new RunVerbCommandHandler(
      runs as never,
      ipc,
      logger as never,
      workspace as never
    );
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("cancel"), "cancel");

    expect(workspace.findRepositoryByGitHub).toHaveBeenCalledWith("acme/api");
    expect(runs.cancelByRemoteRunId).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });

  it("consumes a verb for a repo open in this window", async () => {
    const workspace = { findRepositoryByGitHub: vi.fn().mockReturnValue({ name: "api" }) };
    const handler = new RunVerbCommandHandler(
      makeRuns("applied") as never,
      ipc,
      logger as never,
      workspace as never
    );
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("resume"), "resume");

    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
  });

  it("logs a failed ack instead of throwing", async () => {
    ipc.agentAcknowledgeCommand.mockRejectedValue(new Error("HTTP 409: already acked"));
    const handler = new RunVerbCommandHandler(makeRuns("applied") as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    await expect(handler.consume(verbCmd("approve"), "approve")).resolves.toBeUndefined();
    expect(logger.error).toHaveBeenCalled();
  });

  // At-least-once delivery (#2334 review): a pause delivered twice while the
  // first copy is still persisting its pause must not be acked
  // `already-paused` by the second copy, ahead of the first copy's `applied`.
  it("applies and acknowledges a verb delivered twice only once", async () => {
    let paused = false;
    let release: () => void = () => {};
    const runs = makeRuns("applied");
    runs.pauseByRemoteRunId.mockImplementation(async () => {
      if (paused) return "already-paused";
      paused = true; // the flag moves at once; persisting it takes a while
      await new Promise<void>((resolve) => (release = resolve));
      return "applied";
    });
    const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    const first = handler.consume(verbCmd("pause"), "pause");
    const second = handler.consume(verbCmd("pause"), "pause");
    release();
    await Promise.all([first, second]);

    expect(runs.pauseByRemoteRunId).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-ext",
      "cmd-pause",
      "applied",
      undefined
    );

    // A copy after the ack reached the platform is dropped.
    await handler.consume(verbCmd("pause"), "pause");
    expect(runs.pauseByRemoteRunId).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
  });

  it("re-sends the same ack, without re-applying, when a redelivery follows a failed ack", async () => {
    const runs = makeRuns("applied");
    ipc.agentAcknowledgeCommand
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValue({ runId: "" });
    const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
    handler.setAgentId("agent-ext");

    await handler.consume(verbCmd("cancel"), "cancel");
    // The platform delivers it again on the next reconnect; by then the run
    // is gone, and a fresh decision would wrongly say no-active-run.
    runs.cancelByRemoteRunId.mockResolvedValue("no-active-run");
    await handler.consume(verbCmd("cancel"), "cancel");
    await handler.consume(verbCmd("cancel"), "cancel");

    expect(runs.cancelByRemoteRunId).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(2);
    for (const call of ipc.agentAcknowledgeCommand.mock.calls) {
      expect(call).toEqual(["agent-ext", "cmd-cancel", "applied", undefined]);
    }
  });

  // A platform pause or resume shows in the window as the local commands
  // show it (#2334 review): only when it took effect.
  it("shows an applied pause and resume in the window, and nothing for a no-op", async () => {
    const pauseUi = {
      paused: vi.fn().mockResolvedValue(undefined),
      resumed: vi.fn().mockResolvedValue(undefined),
    };
    const applied = new RunVerbCommandHandler(
      makeRuns("applied") as never,
      ipc,
      logger as never,
      undefined,
      pauseUi
    );
    applied.setAgentId("agent-ext");
    await applied.consume(verbCmd("pause"), "pause");
    expect(pauseUi.paused).toHaveBeenCalledWith("run-1");
    await applied.consume(verbCmd("resume"), "resume");
    expect(pauseUi.resumed).toHaveBeenCalledWith("run-1");
    await applied.consume(verbCmd("cancel"), "cancel");
    expect(pauseUi.paused).toHaveBeenCalledTimes(1);
    expect(pauseUi.resumed).toHaveBeenCalledTimes(1);

    const noop = new RunVerbCommandHandler(
      makeRuns("already-paused") as never,
      ipc,
      logger as never,
      undefined,
      pauseUi
    );
    noop.setAgentId("agent-ext");
    await noop.consume(verbCmd("pause", { id: "cmd-pause-2" }), "pause");
    expect(pauseUi.paused).toHaveBeenCalledTimes(1);
  });

  it("still acknowledges a pause whose window update fails", async () => {
    const pauseUi = {
      paused: vi.fn().mockRejectedValue(new Error("no status bar")),
      resumed: vi.fn(),
    };
    const handler = new RunVerbCommandHandler(
      makeRuns("applied") as never,
      ipc,
      logger as never,
      undefined,
      pauseUi
    );
    handler.setAgentId("agent-ext");
    await handler.consume(verbCmd("pause"), "pause");
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-ext",
      "cmd-pause",
      "applied",
      undefined
    );
  });

  it("ignores every type that is not a run verb", () => {
    const runs = makeRuns("applied");
    const handler = new RunVerbCommandHandler(runs as never, ipc, logger as never);
    handler.handle(verbCmd("trigger"));
    handler.handle(verbCmd("throttle"));
    for (const fn of Object.values(runs)) expect(fn).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });
});
