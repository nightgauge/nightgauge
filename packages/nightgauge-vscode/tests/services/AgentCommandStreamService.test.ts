/**
 * AgentCommandStreamService.test.ts
 *
 * Unit tests for AgentCommandStreamService — SSE lifecycle, command dispatch,
 * Last-Event-ID persistence, event-type filtering, and reconnect behavior.
 *
 * @see Issue #3550 — VSCode subscribe to GET /v1/agents/{id}/commands
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ── VSCode mock ───────────────────────────────────────────────────────────────

const mockGlobalStateStore = new Map<string, unknown>();
const mockGlobalState = {
  get: vi.fn((key: string) => mockGlobalStateStore.get(key)),
  update: vi.fn((key: string, value: unknown) => {
    mockGlobalStateStore.set(key, value);
    return Promise.resolve();
  }),
  keys: vi.fn(() => [] as readonly string[]),
  setKeysForSync: vi.fn(),
};

vi.mock("vscode", () => ({
  ExtensionContext: class {},
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// ── fetch mock ────────────────────────────────────────────────────────────────

function makeSSEStream(events: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let idx = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (idx < events.length) {
        controller.enqueue(encoder.encode(events[idx++]));
      } else {
        controller.close();
      }
    },
  });
}

const mockFetch = vi.fn();
global.fetch = mockFetch;

// ── Imports (after mocks) ─────────────────────────────────────────────────────

import {
  AgentCommandStreamService,
  type CommandHandler,
  type ReceivedCommand,
} from "../../src/services/AgentCommandStreamService";

// ── Test helpers ──────────────────────────────────────────────────────────────

function makeContext() {
  return { globalState: mockGlobalState } as unknown as import("vscode").ExtensionContext;
}

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as import("../../src/utils/logger").Logger;
}

function makeTokenStorage(token: string | null = "test-token") {
  return {
    retrieve: vi.fn().mockResolvedValue(token),
    store: vi.fn(),
    delete: vi.fn(),
  };
}

function makeHandler(): CommandHandler & { received: ReceivedCommand[] } {
  const received: ReceivedCommand[] = [];
  return {
    received,
    handle: vi.fn((cmd: ReceivedCommand) => {
      received.push(cmd);
    }),
  };
}

function sseCommandChunk(id: string, cmd: Partial<ReceivedCommand>): string {
  return `id: ${id}\nevent: command\ndata: ${JSON.stringify(cmd)}\n\n`;
}

function sseChunkNoEvent(id: string, data: unknown): string {
  return `id: ${id}\ndata: ${JSON.stringify(data)}\n\n`;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("AgentCommandStreamService", () => {
  beforeEach(() => {
    mockGlobalStateStore.clear();
    mockGlobalState.get.mockClear();
    mockGlobalState.update.mockClear();
    mockFetch.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("start() opens fetch connection with correct URL and auth headers", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("agent-123");

    await new Promise((r) => setTimeout(r, 20));

    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.com/v1/agents/agent-123/commands");
    expect((init.headers as Record<string, string>)["Authorization"]).toBe("Bearer test-token");
    expect((init.headers as Record<string, string>)["Accept"]).toBe("text/event-stream");

    svc.dispose();
  });

  it("start() is a no-op when agentId is empty string", async () => {
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("");
    await new Promise((r) => setTimeout(r, 10));
    expect(mockFetch).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("start() is idempotent — second call with same agentId does nothing", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("agent-abc");
    svc.start("agent-abc");
    await new Promise((r) => setTimeout(r, 20));

    expect(mockFetch).toHaveBeenCalledOnce();
    svc.dispose();
  });

  it("start() hands the agentId to the handler (setAgentId) so trigger acks work (#3544)", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const setAgentId = vi.fn();
    const handler: CommandHandler = { handle: vi.fn(), setAgentId };
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-ack");

    // Set synchronously, before the (async) stream connects — a command that
    // arrives immediately must find the agentId already in place to ack.
    expect(setAgentId).toHaveBeenCalledWith("agent-ack");

    await new Promise((r) => setTimeout(r, 20));
    svc.dispose();
  });

  it("start() tolerates a handler that does not implement setAgentId", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const handler: CommandHandler = { handle: vi.fn() }; // setAgentId is optional
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );

    expect(() => svc.start("agent-nohandler")).not.toThrow();
    await new Promise((r) => setTimeout(r, 20));
    svc.dispose();
  });

  it("dispose() aborts the fetch and clears reconnect timer", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("agent-xyz");
    await new Promise((r) => setTimeout(r, 10));
    svc.dispose();

    // After dispose, no further fetch calls should happen
    await new Promise((r) => setTimeout(r, 10));
    expect(mockFetch).toHaveBeenCalledOnce();
  });

  it("dispatches event: command events to CommandHandler", async () => {
    const cmd = {
      id: "cmd-1",
      type: "run-pipeline",
      payload: { issue: 42 },
      createdAt: "2026-01-01T00:00:00Z",
    };
    const events = [sseCommandChunk("evt-1", cmd)];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-1");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).toHaveBeenCalledOnce();
    expect(handler.received[0]).toMatchObject({ id: "cmd-1", type: "run-pipeline" });
    svc.dispose();
  });

  it("normalizes the platform's `commandId` field to `id` so acks aren't empty (#3551)", async () => {
    // The platform publishes the command id as `commandId`, not `id`. Without
    // normalization, cmd.id is undefined and agent.acknowledgeCommand fails with
    // "commandId is required", leaving the routed pipeline unstarted.
    const wire = {
      commandId: "cmd-platform-1",
      type: "trigger",
      payload: { issueNumber: 42, repo: "nightgauge/nightgauge" },
      createdAt: "2026-01-01T00:00:00Z",
    };
    const events = [sseCommandChunk("evt-1", wire)];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-norm");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).toHaveBeenCalledOnce();
    expect(handler.received[0].id).toBe("cmd-platform-1");
    expect(handler.received[0].type).toBe("trigger");
    svc.dispose();
  });

  it("ignores non-command event types", async () => {
    const events = [
      `id: evt-1\nevent: heartbeat\ndata: {}\n\n`,
      `id: evt-2\nevent: ping\ndata: {"status":"ok"}\n\n`,
    ];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-2");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("sends Last-Event-ID header when stored in globalState", async () => {
    mockGlobalStateStore.set("nightgauge.agentCommands.lastEventId", "stored-42");
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream([]),
    });

    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("agent-3");
    await new Promise((r) => setTimeout(r, 20));

    const [, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)["Last-Event-ID"]).toBe("stored-42");
    svc.dispose();
  });

  it("persists Last-Event-ID to globalState after receiving id: field", async () => {
    const cmd = {
      id: "cmd-x",
      type: "run-pipeline",
      payload: {},
      createdAt: "2026-01-01T00:00:00Z",
    };
    const events = [sseCommandChunk("evt-99", cmd)];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      makeHandler()
    );
    svc.start("agent-4");
    await new Promise((r) => setTimeout(r, 50));

    expect(mockGlobalState.update).toHaveBeenCalledWith(
      "nightgauge.agentCommands.lastEventId",
      "evt-99"
    );
    svc.dispose();
  });

  it("handleCommandEvent ignores malformed JSON without throwing", async () => {
    const events = [`id: evt-bad\nevent: command\ndata: not-valid-json\n\n`];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const logger = makeLogger();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      logger,
      handler
    );
    svc.start("agent-5");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("malformed"));
    svc.dispose();
  });

  it("SSE keepalive lines (starting with :) do not trigger handler", async () => {
    const events = [`id: evt-k\nevent: command\ndata: :keep-alive\n\n`];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-6");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("events with no event: field are dispatched (default to command)", async () => {
    const cmd = {
      id: "cmd-noev",
      type: "run-pipeline",
      payload: {},
      createdAt: "2026-01-01T00:00:00Z",
    };
    const events = [sseChunkNoEvent("evt-noev", cmd)];
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: "OK",
      body: makeSSEStream(events),
    });

    const handler = makeHandler();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(),
      makeContext(),
      makeLogger(),
      handler
    );
    svc.start("agent-7");
    await new Promise((r) => setTimeout(r, 50));

    expect(handler.handle).toHaveBeenCalledOnce();
    svc.dispose();
  });

  it("skips connect when no access token is available", async () => {
    const logger = makeLogger();
    const svc = new AgentCommandStreamService(
      () => "https://api.example.com",
      makeTokenStorage(null),
      makeContext(),
      logger,
      makeHandler()
    );
    svc.start("agent-8");
    await new Promise((r) => setTimeout(r, 20));

    expect(mockFetch).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("no access token"));
    svc.dispose();
  });

  // ── Reconnect backoff (#3554) ───────────────────────────────────────────────

  describe("reconnect backoff", () => {
    /**
     * Flush the chain of microtasks created by:
     *   runStream() (catch/then) → scheduleReconnect() → setTimeout queued.
     * One Promise.resolve() awaits the rejection settle; a second awaits the
     * .catch handler body that calls scheduleReconnect synchronously.
     */
    async function flushReconnectScheduling() {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    }

    it("uses 1s → 2s → 4s → 8s → 16s → 30s → 30s sequence on repeated failure", async () => {
      vi.useFakeTimers();
      const randSpy = vi.spyOn(Math, "random").mockReturnValue(0.5); // jitter = 0

      // All seven fetches reject — drives the full saturation curve.
      mockFetch.mockRejectedValue(new Error("boom"));

      const svc = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svc.start("agent-backoff");

      const expectedDelays = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000];

      // Wait for the initial fetch rejection → scheduleReconnect cycle.
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(1);

      for (let i = 0; i < expectedDelays.length; i++) {
        // Just-before-the-delay: no new fetch yet.
        await vi.advanceTimersByTimeAsync(expectedDelays[i] - 1);
        expect(mockFetch).toHaveBeenCalledTimes(i + 1);

        // Cross the boundary: the scheduled connect fires, awaits the fetch,
        // sees rejection, and queues the next reconnect.
        await vi.advanceTimersByTimeAsync(1);
        await flushReconnectScheduling();
        expect(mockFetch).toHaveBeenCalledTimes(i + 2);
      }

      svc.dispose();
      randSpy.mockRestore();
    });

    it("applies ±20% jitter envelope to scheduled delay", async () => {
      vi.useFakeTimers();
      mockFetch.mockRejectedValue(new Error("boom"));

      // Lower bound: Math.random = 0 → jitter multiplier = -1 → 0.8x base.
      const randLow = vi.spyOn(Math, "random").mockReturnValue(0);
      const svcLow = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svcLow.start("agent-low");
      await flushReconnectScheduling();

      // 800ms is the lower bound — fetch should not fire yet at 799.
      await vi.advanceTimersByTimeAsync(799);
      expect(mockFetch).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1);
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(2);

      svcLow.dispose();
      randLow.mockRestore();
      mockFetch.mockReset();
      mockFetch.mockRejectedValue(new Error("boom"));

      // Upper bound: Math.random ~ 1 → jitter ~ +1 → 1.2x base ≈ 1200ms.
      const randHigh = vi.spyOn(Math, "random").mockReturnValue(0.999999);
      const svcHigh = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svcHigh.start("agent-high");
      await flushReconnectScheduling();

      // Just before 1200ms: still no second fetch.
      await vi.advanceTimersByTimeAsync(1199);
      expect(mockFetch).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1);
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(2);

      svcHigh.dispose();
      randHigh.mockRestore();
    });

    it("resets backoff to 1s after a successful reconnect", async () => {
      vi.useFakeTimers();
      const randSpy = vi.spyOn(Math, "random").mockReturnValue(0.5);

      // Call 1: throws → schedules reconnect at 1s.
      // Call 2: ok (empty stream closes cleanly) → resets counter, schedules reconnect at 1s.
      // Call 3: throws → would be 2s if not reset; should be 1s.
      mockFetch
        .mockRejectedValueOnce(new Error("first boom"))
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          statusText: "OK",
          body: makeSSEStream([]),
        })
        .mockRejectedValueOnce(new Error("third boom"));

      const svc = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svc.start("agent-reset");

      // Initial fetch rejects → reconnect at 1s.
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(1);

      // First retry: succeeds, empty stream closes immediately, triggers reconnect at 1s.
      await vi.advanceTimersByTimeAsync(1_000);
      await flushReconnectScheduling();
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(2);

      // Third connect should fire at 1s (counter was reset), not 2s.
      await vi.advanceTimersByTimeAsync(999);
      expect(mockFetch).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(1);
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(3);

      svc.dispose();
      randSpy.mockRestore();
    });

    it("schedules reconnect on clean stream close (no thrown error)", async () => {
      vi.useFakeTimers();
      const randSpy = vi.spyOn(Math, "random").mockReturnValue(0.5);

      // Empty stream resolves and closes cleanly → triggers reconnect on .then() path.
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: "OK",
        body: makeSSEStream([]),
      });
      mockFetch.mockRejectedValue(new Error("subsequent failure"));

      const logger = makeLogger();
      const svc = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        logger,
        makeHandler()
      );
      svc.start("agent-close");

      await flushReconnectScheduling();
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(1);

      // First successful connect resets attempt → reconnect at 1s.
      await vi.advanceTimersByTimeAsync(1_000);
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(2);
      expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("stream closed by server"));

      svc.dispose();
      randSpy.mockRestore();
    });

    it("sends Last-Event-ID header on the reconnect attempt", async () => {
      vi.useFakeTimers();
      const randSpy = vi.spyOn(Math, "random").mockReturnValue(0.5);

      const cmd = {
        id: "cmd-r",
        type: "run-pipeline",
        payload: {},
        createdAt: "2026-01-01T00:00:00Z",
      };
      const events = [sseCommandChunk("evt-77", cmd)];
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: "OK",
        body: makeSSEStream(events),
      });
      mockFetch.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: "OK",
        body: makeSSEStream([]),
      });

      const svc = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svc.start("agent-leid");

      // Let first stream deliver the event and close cleanly.
      await flushReconnectScheduling();
      await flushReconnectScheduling();
      await flushReconnectScheduling();
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(1);

      // Drive the reconnect timer.
      await vi.advanceTimersByTimeAsync(1_000);
      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(2);

      const [, secondInit] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((secondInit.headers as Record<string, string>)["Last-Event-ID"]).toBe("evt-77");

      svc.dispose();
      randSpy.mockRestore();
    });

    it("dispose() cancels a pending reconnect timer", async () => {
      vi.useFakeTimers();
      const randSpy = vi.spyOn(Math, "random").mockReturnValue(0.5);
      mockFetch.mockRejectedValue(new Error("boom"));

      const svc = new AgentCommandStreamService(
        () => "https://api.example.com",
        makeTokenStorage(),
        makeContext(),
        makeLogger(),
        makeHandler()
      );
      svc.start("agent-dispose");

      await flushReconnectScheduling();
      expect(mockFetch).toHaveBeenCalledTimes(1);

      // Cancel before the 1s reconnect timer fires.
      svc.dispose();

      // Advance well past every possible delay.
      await vi.advanceTimersByTimeAsync(60_000);
      await flushReconnectScheduling();

      expect(mockFetch).toHaveBeenCalledTimes(1);
      randSpy.mockRestore();
    });
  });
});
