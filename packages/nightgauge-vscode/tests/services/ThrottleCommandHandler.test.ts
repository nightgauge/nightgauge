/**
 * ThrottleCommandHandler.test.ts
 *
 * A platform `throttle` command makes the window read its own workspace's
 * throttle and apply it, and is acknowledged once per command id (#2337).
 * The payload itself is never applied: it names no workspace.
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("vscode", () => ({}));

import { ThrottleCommandHandler } from "../../src/services/ThrottleCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";
import {
  WorkspaceThrottleSync,
  type ThrottleRefreshResult,
} from "../../src/services/WorkspaceThrottleSync";
import {
  WorkspaceThrottleState,
  type WorkspaceThrottle,
} from "../../src/services/WorkspaceThrottle";

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

const SET = { action: "set", maxConcurrent: 1, resumeAt: "2099-10-02T14:00:00.000Z" };
const CLEARED = { action: "cleared", maxConcurrent: null, resumeAt: null };

describe("ThrottleCommandHandler", () => {
  let ipc: ReturnType<typeof makeIpc>;
  let logger: ReturnType<typeof makeLogger>;
  let sync: { isActive: Mock<() => boolean>; refresh: Mock<() => Promise<ThrottleRefreshResult>> };
  let handler: ThrottleCommandHandler;

  beforeEach(() => {
    ipc = makeIpc();
    logger = makeLogger();
    sync = {
      isActive: vi.fn(() => true),
      refresh: vi.fn(async (): Promise<ThrottleRefreshResult> => "applied"),
    };
    handler = new ThrottleCommandHandler(sync, ipc, logger as never);
    handler.setAgentId("agent-ext");
  });

  it.each([
    ["set", SET],
    ["cleared", CLEARED],
  ])(
    "reads the workspace's throttle on a %s and acknowledges it as applied",
    async (_a, payload) => {
      await handler.consume(throttleCmd(payload));

      expect(sync.refresh).toHaveBeenCalledTimes(1);
      expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledWith(
        "agent-ext",
        "cmd-throttle-1",
        "applied",
        undefined
      );
    }
  );

  it("refuses an invalid payload without reading anything", async () => {
    await handler.consume(throttleCmd({ action: "set", maxConcurrent: -2 }));

    expect(sync.refresh).not.toHaveBeenCalled();
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

  it("reads and acknowledges a throttle delivered twice only once", async () => {
    handler.handle(throttleCmd(SET));
    await handler.consume(throttleCmd(SET));

    expect(sync.refresh).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
  });

  it("refuses the command when the workspace's throttle could not be read", async () => {
    sync.refresh.mockResolvedValueOnce("failed");
    await handler.consume(throttleCmd(SET));
    const [, , outcome, detail] = ipc.agentAcknowledgeCommand.mock.calls[0];
    expect(outcome).toBe("rejected");
    expect(detail).toMatch(/^apply-failed: /);
  });

  // A window with no platform session follows no throttle: it leaves the
  // command to the windows that do, and answers a later copy once signed in.
  it("leaves the command unacknowledged while the window has no platform session", async () => {
    sync.isActive.mockReturnValue(false);
    await handler.consume(throttleCmd(SET));
    expect(sync.refresh).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();

    sync.isActive.mockReturnValue(true);
    await handler.consume(throttleCmd(SET));
    expect(sync.refresh).toHaveBeenCalledTimes(1);
    expect(ipc.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
  });

  it("does not acknowledge a command whose read ended with the session", async () => {
    sync.refresh.mockResolvedValueOnce("inactive");
    await handler.consume(throttleCmd(SET));
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });

  // Every window of the machine reads the throttle and acknowledges it; the
  // platform keeps the first ack and refuses the rest.
  it("only warns when its ack is refused", async () => {
    ipc.agentAcknowledgeCommand.mockRejectedValue(new Error("HTTP 409: already acknowledged"));
    await expect(handler.consume(throttleCmd(SET))).resolves.toBeUndefined();
    expect(sync.refresh).toHaveBeenCalledTimes(1);
    expect(logger.warn).toHaveBeenCalled();
    expect(logger.error).not.toHaveBeenCalled();
  });

  it("ignores every other command type", () => {
    handler.handle({ ...throttleCmd(SET), type: "pause" });
    handler.handle({ ...throttleCmd(SET), type: "trigger" });
    expect(sync.refresh).not.toHaveBeenCalled();
    expect(ipc.agentAcknowledgeCommand).not.toHaveBeenCalled();
  });

  // A newer clear and an older set handled back to back (the window's own
  // stream and the daemon's relay, or a backlog replay), with the kept state
  // persisting asynchronously: the cap applied last is the platform's state,
  // never the older command's payload.
  it("ends on the platform's newest state when commands are handled out of order", async () => {
    let platform: WorkspaceThrottle | null = { maxConcurrent: 1, resumeAt: null };
    const reader = {
      read: vi.fn(async () => {
        await new Promise((resolve) => setTimeout(resolve, 1));
        return platform;
      }),
    };
    const dispatch = { setWorkspaceThrottle: vi.fn() };
    const values = new Map<string, unknown>();
    const memento = {
      get: (key: string) => values.get(key),
      update: async (key: string, value: unknown) => {
        await new Promise((resolve) => setTimeout(resolve, 1));
        if (value === undefined) values.delete(key);
        else values.set(key, value);
      },
    };
    const realSync = new WorkspaceThrottleSync(
      reader,
      new WorkspaceThrottleState(dispatch, memento as never, logger as never),
      () => "alpha",
      logger as never
    );
    await realSync.activate();
    expect(dispatch.setWorkspaceThrottle).toHaveBeenLastCalledWith({
      maxConcurrent: 1,
      resumeAt: null,
    });

    const real = new ThrottleCommandHandler(realSync, ipc, logger as never);
    real.setAgentId("agent-ext");
    platform = null; // cleared at 12:05, after the set at 12:00
    await Promise.all([
      real.consume(throttleCmd(CLEARED, { id: "cmd-new", createdAt: "2026-10-02T12:05:00.000Z" })),
      real.consume(
        throttleCmd(SET, {
          id: "cmd-old",
          createdAt: "2026-10-02T12:00:00.000Z",
          agentId: "agent-daemon",
        })
      ),
    ]);

    expect(dispatch.setWorkspaceThrottle).toHaveBeenLastCalledWith(null);
    expect(dispatch.setWorkspaceThrottle).not.toHaveBeenCalledWith({
      maxConcurrent: 1,
      resumeAt: "2099-10-02T14:00:00.000Z",
    });
    expect(values.size).toBe(0);
    expect(ipc.agentAcknowledgeCommand.mock.calls.map((c) => [c[1], c[2]])).toEqual(
      expect.arrayContaining([
        ["cmd-new", "applied"],
        ["cmd-old", "applied"],
      ])
    );
  });
});
