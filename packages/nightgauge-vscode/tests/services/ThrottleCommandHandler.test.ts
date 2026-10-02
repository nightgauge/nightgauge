/**
 * ThrottleCommandHandler.test.ts
 *
 * A platform `throttle` command is applied to this window's dispatch and
 * acknowledged `applied`, once per command id (#2337).
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("vscode", () => ({}));

import { ThrottleCommandHandler } from "../../src/services/ThrottleCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import type { WorkspaceThrottle } from "../../src/services/WorkspaceThrottle";

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

function makeIpc() {
  return { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
}

function throttleCmd(payload: unknown, extra: Partial<ReceivedCommand> = {}): ReceivedCommand {
  return {
    id: "cmd-throttle-1",
    type: "throttle",
    payload,
    createdAt: "2026-10-02T12:00:00.000Z",
    ...extra,
  };
}

const SET = { action: "set", maxConcurrent: 1, resumeAt: "2026-10-02T14:00:00.000Z" };
const CLEARED = { action: "cleared", maxConcurrent: null, resumeAt: null };

describe("ThrottleCommandHandler", () => {
  let ipc: ReturnType<typeof makeIpc>;
  let logger: ReturnType<typeof makeLogger>;
  let state: { apply: Mock<(throttle: WorkspaceThrottle | null) => Promise<void>> };
  let handler: ThrottleCommandHandler;

  beforeEach(() => {
    ipc = makeIpc();
    logger = makeLogger();
    state = { apply: vi.fn<(throttle: WorkspaceThrottle | null) => Promise<void>>() };
    state.apply.mockResolvedValue(undefined);
    handler = new ThrottleCommandHandler(state, ipc, logger as never);
    handler.setAgentId("agent-ext");
  });

  it("applies a set and acknowledges it as applied", async () => {
    await handler.consume(throttleCmd(SET));

    expect(state.apply).toHaveBeenCalledWith({
      maxConcurrent: 1,
      resumeAt: "2026-10-02T14:00:00.000Z",
    });
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-ext",
      "cmd-throttle-1",
      "applied",
      undefined
    );
  });

  it("applies a clear as no throttle and acknowledges it as applied", async () => {
    await handler.consume(throttleCmd(CLEARED));

    expect(state.apply).toHaveBeenCalledWith(null);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-ext",
      "cmd-throttle-1",
      "applied",
      undefined
    );
  });

  it("refuses an invalid payload without applying anything", async () => {
    await handler.consume(throttleCmd({ action: "set", maxConcurrent: -2 }));

    expect(state.apply).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
    const [, , outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
    expect(outcome).toBe("rejected");
    expect(detail).toMatch(/^invalid-payload: /);
  });

  it("acknowledges a relayed throttle under the agent the platform addressed", async () => {
    await handler.consume(throttleCmd(SET, { agentId: "agent-daemon" }));
    expect(ipc.agentAcknowledgeCommand.mock.calls[0].slice(0, 3)).toEqual([
      "agent-daemon",
      "cmd-throttle-1",
      "applied",
    ]);
  });

  it("applies and acknowledges a throttle delivered twice only once", async () => {
    handler.handle(throttleCmd(SET));
    await handler.consume(throttleCmd(SET));

    expect(state.apply).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
  });

  it("does not let an older throttle command undo a newer one", async () => {
    await handler.consume(
      throttleCmd(CLEARED, { id: "cmd-new", createdAt: "2026-10-02T12:05:00.000Z" })
    );
    await handler.consume(
      throttleCmd(SET, { id: "cmd-old", createdAt: "2026-10-02T12:00:00.000Z" })
    );

    expect(state.apply).toHaveBeenCalledTimes(1);
    expect(state.apply).toHaveBeenCalledWith(null);
    const old = ipc.agentAcknowledgeCommand.mock.calls.find((c) => c[1] === "cmd-old");
    expect(old?.[2]).toBe("rejected");
    expect(old?.[3]).toMatch(/^superseded: /);
  });

  it("refuses the command when applying it fails", async () => {
    state.apply.mockRejectedValueOnce(new Error("boom"));
    await handler.consume(throttleCmd(SET));
    const [, , outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
    expect(outcome).toBe("rejected");
    expect(detail).toMatch(/^apply-failed: /);
  });

  // Every window of the machine applies the throttle and acknowledges it; the
  // platform keeps the first ack and refuses the rest.
  it("still applies the throttle when its ack is refused, and only warns", async () => {
    ipc.agentAcknowledgeCommand.mockRejectedValue(new Error("HTTP 409: already acknowledged"));
    await expect(handler.consume(throttleCmd(SET))).resolves.toBeUndefined();
    expect(state.apply).toHaveBeenCalledTimes(1);
    expect(logger.warn).toHaveBeenCalled();
    expect(logger.error).not.toHaveBeenCalled();
  });

  it("ignores every other command type", () => {
    handler.handle({ ...throttleCmd(SET), type: "pause" });
    handler.handle({ ...throttleCmd(SET), type: "trigger" });
    expect(state.apply).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });
});
