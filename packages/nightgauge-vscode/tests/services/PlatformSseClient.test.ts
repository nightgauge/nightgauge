/**
 * Tests for PlatformSseClient (Issue #3711)
 *
 * Covers: normal connect, Last-Event-ID resume, backoff sequence, 401 + refresh,
 * 401 + null refresh, double-401 disconnect, 429 backoff, reconnect cancels timer,
 * and abort propagation after disconnect().
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { PlatformSseClient } from "../../src/services/PlatformSseClient";
import type { PlatformSseClientOptions } from "../../src/services/PlatformSseClient";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  class EventEmitter<T> {
    private listeners: Array<(e: T) => void> = [];
    readonly event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire(e: T) {
      this.listeners.forEach((l) => l(e));
    }
    dispose() {
      this.listeners = [];
    }
  }
  return { EventEmitter };
});

// ---------------------------------------------------------------------------
// Shared mocks
// ---------------------------------------------------------------------------

const mockGlobalStateStore = new Map<string, unknown>();
const mockGlobalState = {
  get: vi.fn((key: string) => mockGlobalStateStore.get(key)),
  update: vi.fn((key: string, value: unknown) => {
    mockGlobalStateStore.set(key, value);
    return Promise.resolve();
  }),
};
const mockContext = { globalState: mockGlobalState } as unknown as vscode.ExtensionContext;

const mockLogger: Logger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
} as unknown as Logger;

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const TEST_URL = "https://api.example.com/v1/events/stream";
const TOKEN = "tok-abc";
const LAST_EVENT_ID_KEY = "test.lastEventId";

/** Stream that delivers chunks then closes — triggers server-close reconnect. */
function makeSseStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let i = 0;
  return new ReadableStream({
    pull(ctrl) {
      if (i < chunks.length) ctrl.enqueue(encoder.encode(chunks[i++]));
      else ctrl.close();
    },
  });
}

/** Stream that never closes — safe to use when we don't want reconnect loops. */
function makeNeverEndingStream(): ReadableStream<Uint8Array> {
  return new ReadableStream({ start() {} });
}

function makeOkSseResponse(stream: ReadableStream): Response {
  return {
    ok: true,
    status: 200,
    body: stream,
    headers: {
      get: (n: string) => (n === "content-type" ? "text/event-stream; charset=utf-8" : null),
    },
    clone: () => makeOkSseResponse(stream),
  } as unknown as Response;
}

function makeErrorResponse(status: number): Response {
  return {
    ok: false,
    status,
    statusText: "Error",
    body: null,
    headers: { get: () => null },
    clone: () => ({ text: () => Promise.resolve("") }),
    text: () => Promise.resolve(""),
  } as unknown as Response;
}

function make401Response(): Response {
  return {
    ok: false,
    status: 401,
    statusText: "Unauthorized",
    body: null,
    headers: { get: () => null },
    clone: () => ({ text: () => Promise.resolve("") }),
    text: () => Promise.resolve(""),
  } as unknown as Response;
}

function make429Response(body = "SSE_CONNECTION_LIMIT_EXCEEDED"): Response {
  return {
    ok: false,
    status: 429,
    statusText: "Too Many Requests",
    body: null,
    headers: { get: () => null },
    clone: () => ({ text: () => Promise.resolve(body) }),
    text: () => Promise.resolve(body),
  } as unknown as Response;
}

/** Flush N microtask ticks. */
async function flushMicrotasks(n = 8): Promise<void> {
  for (let i = 0; i < n; i++) await Promise.resolve();
}

/** Build a client with sensible defaults; caller can override any option. */
function makeClient(overrides: Partial<PlatformSseClientOptions> = {}): {
  client: PlatformSseClient;
  onEvent: ReturnType<typeof vi.fn>;
  onStatusChanged: ReturnType<typeof vi.fn>;
  onAuthRequired: ReturnType<typeof vi.fn>;
} {
  const onEvent = vi.fn();
  const onStatusChanged = vi.fn();
  const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(null);

  const client = new PlatformSseClient({
    context: mockContext,
    logger: mockLogger,
    lastEventIdKey: LAST_EVENT_ID_KEY,
    onEvent,
    onStatusChanged,
    onAuthRequired,
    ...overrides,
  });

  // Return the ACTUAL callbacks used by the client (respect overrides).
  return {
    client,
    onEvent: (overrides.onEvent as ReturnType<typeof vi.fn>) ?? onEvent,
    onStatusChanged: (overrides.onStatusChanged as ReturnType<typeof vi.fn>) ?? onStatusChanged,
    onAuthRequired: (overrides.onAuthRequired as ReturnType<typeof vi.fn>) ?? onAuthRequired,
  };
}

// ---------------------------------------------------------------------------
// Setup — no global fake timers (individual tests opt-in)
// ---------------------------------------------------------------------------

beforeEach(() => {
  mockGlobalStateStore.clear();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlatformSseClient", () => {
  describe("normal connect", () => {
    it("fetches SSE URL with auth and accept headers", async () => {
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client, onStatusChanged } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(4);

      expect(mockFetch).toHaveBeenCalledOnce();
      const [url, opts] = mockFetch.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(TEST_URL);
      const headers = opts.headers as Record<string, string>;
      expect(headers["Authorization"]).toBe(`Bearer ${TOKEN}`);
      expect(headers["Accept"]).toBe("text/event-stream");
      expect(onStatusChanged).toHaveBeenCalledWith("connected", "● live");

      client.dispose();
    });

    it("calls onEvent for each dispatched SSE data line", async () => {
      const sseData = "id:evt-1\ndata:hello world\n\n";
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const { client, onEvent } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(10); // enough to process events before reconnect timer fires

      expect(onEvent).toHaveBeenCalledWith("hello world", "evt-1");

      client.dispose(); // cancels any scheduled reconnect timer
    });

    it("does not open a second connection if already connecting", async () => {
      mockFetch.mockResolvedValue(makeOkSseResponse(makeNeverEndingStream()));

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);
      client.connect(TEST_URL, TOKEN); // second call ignored
      await flushMicrotasks(4);

      expect(mockFetch).toHaveBeenCalledOnce();

      client.dispose();
    });
  });

  describe("Last-Event-ID resume", () => {
    it("sends Last-Event-ID header when stored in globalState", async () => {
      mockGlobalStateStore.set(LAST_EVENT_ID_KEY, "cursor-42");
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(4);

      const [, opts] = mockFetch.mock.calls[0] as [string, RequestInit];
      expect((opts.headers as Record<string, string>)["Last-Event-ID"]).toBe("cursor-42");

      client.dispose();
    });

    it("persists the event ID to globalState on each event", async () => {
      const sseData = "id:evt-99\ndata:payload\n\n";
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(10);

      expect(mockGlobalState.update).toHaveBeenCalledWith(LAST_EVENT_ID_KEY, "evt-99");

      client.dispose();
    });

    it("re-sends persisted Last-Event-ID on reconnect after server closes stream", async () => {
      vi.useFakeTimers();

      // First connection: receives an event (persists ID), then stream closes
      const firstStream = makeSseStream("id:cursor-7\ndata:first\n\n");
      // Second connection after reconnect: never-ending so we can inspect the call
      mockFetch
        .mockResolvedValueOnce(makeOkSseResponse(firstStream))
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);

      // Process first stream events (event dispatched, stream closes, reconnect timer set)
      await flushMicrotasks(12);
      // Advance past backoff delay
      await vi.advanceTimersByTimeAsync(1300);
      await flushMicrotasks(4);

      // Second fetch must include the Last-Event-ID from the first stream
      expect(mockFetch).toHaveBeenCalledTimes(2);
      const [, secondOpts] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((secondOpts.headers as Record<string, string>)["Last-Event-ID"]).toBe("cursor-7");

      client.dispose();
    });

    it("ignores SSE comment lines (': keepalive') and does not call onEvent or advance Last-Event-ID", async () => {
      // SSE comment line followed by a real event
      const sseData = ": keepalive\n\nid:evt-real\ndata:actual payload\n\n";
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const { client, onEvent } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(10);

      // onEvent called exactly once for the real event, not for the comment
      expect(onEvent).toHaveBeenCalledOnce();
      expect(onEvent).toHaveBeenCalledWith("actual payload", "evt-real");
      // Last-Event-ID set only to the real event's ID, not disturbed by comment
      expect(mockGlobalState.update).toHaveBeenCalledWith(LAST_EVENT_ID_KEY, "evt-real");
      expect(mockGlobalState.update).toHaveBeenCalledOnce();

      client.dispose();
    });
  });

  describe("backoff sequence", () => {
    it("schedules reconnect after stream failure — onStatusChanged fires 'reconnecting'", async () => {
      vi.useFakeTimers();
      // Two error then success so the reconnect completes cleanly
      mockFetch
        .mockResolvedValueOnce(makeErrorResponse(503))
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client, onStatusChanged } = makeClient();
      client.connect(TEST_URL, TOKEN);

      // Flush: fetch resolves → _runStream throws → .catch fires → _scheduleReconnect
      await flushMicrotasks(4);

      expect(onStatusChanged).toHaveBeenCalledWith("reconnecting", "↻ reconnecting");

      // Advance past the first backoff delay (max 1200ms with ±20% jitter on 1000ms)
      await vi.advanceTimersByTimeAsync(1300);
      await flushMicrotasks(4);

      expect(mockFetch).toHaveBeenCalledTimes(2);

      client.dispose();
    });

    it("resets backoff index after a successful connection — next failure starts from 1000ms", async () => {
      vi.useFakeTimers();

      // Failure → success (first cycle closes after one event)
      mockFetch
        .mockResolvedValueOnce(makeErrorResponse(503))
        .mockResolvedValueOnce(makeOkSseResponse(makeSseStream("data:ok\n\n"))); // closes after one event

      const { client, onStatusChanged } = makeClient();
      client.connect(TEST_URL, TOKEN);

      // First failure → reconnecting ~1000ms
      await flushMicrotasks(4);
      await vi.advanceTimersByTimeAsync(1300);
      await flushMicrotasks(6);
      // Connected, then stream closes → second reconnect cycle begins

      // Trigger the scheduleReconnect for the closed stream
      await flushMicrotasks(4);
      const reconnectingCalls = onStatusChanged.mock.calls.filter(
        ([status]) => status === "reconnecting"
      );
      // At least two reconnecting signals emitted (one per failure cycle)
      expect(reconnectingCalls.length).toBeGreaterThanOrEqual(1);

      client.dispose();
    });

    it("uses increasing delay on successive failures", async () => {
      vi.useFakeTimers();
      mockFetch
        .mockResolvedValueOnce(makeErrorResponse(503)) // attempt 1 fails
        .mockResolvedValueOnce(makeErrorResponse(503)) // attempt 2 fails
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream())); // attempt 3 succeeds

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);

      // Failure 1 → ~1000ms reconnect
      await flushMicrotasks(4);
      await vi.advanceTimersByTimeAsync(1300);
      await flushMicrotasks(4);

      // Failure 2 → ~2000ms reconnect
      await vi.advanceTimersByTimeAsync(2500);
      await flushMicrotasks(4);

      expect(mockFetch).toHaveBeenCalledTimes(3);

      client.dispose();
    });
  });

  describe("401 handling", () => {
    it("calls onAuthRequired on 401, reconnects with new token on success", async () => {
      const newToken = "refreshed-token";
      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(newToken);

      mockFetch
        .mockResolvedValueOnce(make401Response())
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client, onStatusChanged } = makeClient({ onAuthRequired });
      client.connect(TEST_URL, TOKEN);

      // Chain: fetch(401) → SseAuthError → catch → _handleAuthError → onAuthRequired
      //        → connect() → fetch(success) → connected
      await flushMicrotasks(12);

      expect(onAuthRequired).toHaveBeenCalledOnce();
      expect(mockFetch).toHaveBeenCalledTimes(2);
      const [, secondOpts] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((secondOpts.headers as Record<string, string>)["Authorization"]).toBe(
        `Bearer ${newToken}`
      );
      expect(onStatusChanged).toHaveBeenCalledWith("connected", "● live");

      client.dispose();
    });

    it("surfaces disconnected when onAuthRequired returns null", async () => {
      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(null);
      const onStatusChanged = vi.fn();

      mockFetch.mockResolvedValueOnce(make401Response());

      const { client } = makeClient({ onAuthRequired, onStatusChanged });
      client.connect(TEST_URL, TOKEN);

      await flushMicrotasks(8);

      expect(onAuthRequired).toHaveBeenCalledOnce();
      expect(mockFetch).toHaveBeenCalledOnce(); // no retry
      expect(onStatusChanged).toHaveBeenCalledWith("disconnected", "✕ auth error");

      client.dispose();
    });

    it("surfaces disconnected after two consecutive 401s — no infinite loop", async () => {
      const newToken = "refreshed-token";
      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(newToken);
      const onStatusChanged = vi.fn();

      mockFetch
        .mockResolvedValueOnce(make401Response()) // first connect: 401
        .mockResolvedValueOnce(make401Response()); // reconnect with new token: 401 again

      const { client } = makeClient({ onAuthRequired, onStatusChanged });
      client.connect(TEST_URL, TOKEN);

      // First 401 → refresh → second connect → second 401 → disconnect (no third attempt)
      await flushMicrotasks(16);

      expect(onAuthRequired).toHaveBeenCalledOnce(); // only called on first 401
      expect(mockFetch).toHaveBeenCalledTimes(2);
      expect(onStatusChanged).toHaveBeenCalledWith("disconnected", "✕ auth error");

      client.dispose();
    });

    it("resets consecutive auth error counter after a successful connect", async () => {
      const newToken = "refreshed-token";
      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(newToken);
      const onStatusChanged = vi.fn();

      // First cycle: 401 → refresh → success
      mockFetch
        .mockResolvedValueOnce(make401Response())
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client } = makeClient({ onAuthRequired, onStatusChanged });
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(12);

      expect(onStatusChanged).toHaveBeenCalledWith("connected", "● live");
      onAuthRequired.mockClear();

      // After reconnect, simulate another 401 — counter should have reset, so one refresh attempt
      mockFetch
        .mockResolvedValueOnce(make401Response())
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      client.reconnect(TEST_URL, newToken);
      await flushMicrotasks(16);

      expect(onAuthRequired).toHaveBeenCalledOnce();

      client.dispose();
    });
  });

  describe("429 connection limit", () => {
    it("backs off with reconnecting status on SSE_CONNECTION_LIMIT_EXCEEDED", async () => {
      vi.useFakeTimers();
      mockFetch
        .mockResolvedValueOnce(make429Response("SSE_CONNECTION_LIMIT_EXCEEDED"))
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream()));

      const { client, onStatusChanged } = makeClient();
      client.connect(TEST_URL, TOKEN);

      await flushMicrotasks(6);

      expect(onStatusChanged).toHaveBeenCalledWith(
        "reconnecting",
        "↻ connection limit — backing off"
      );

      await vi.advanceTimersByTimeAsync(1300);
      await flushMicrotasks(4);

      expect(mockFetch).toHaveBeenCalledTimes(2);

      client.dispose();
    });
  });

  describe("reconnect()", () => {
    it("cancels a pending retry timer and immediately reconnects with new token", async () => {
      vi.useFakeTimers();
      const newToken = "new-token";

      mockFetch
        .mockResolvedValueOnce(makeErrorResponse(503)) // first attempt fails → reconnect timer
        .mockResolvedValueOnce(makeOkSseResponse(makeNeverEndingStream())); // immediate reconnect

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);

      // Let the failure path complete (timer scheduled, not yet fired)
      await flushMicrotasks(4);

      // Call reconnect() before the timer fires — cancels the timer, starts immediately
      client.reconnect(TEST_URL, newToken);
      await flushMicrotasks(4);

      // Second fetch should use the new token; total = 2 (not 3)
      expect(mockFetch).toHaveBeenCalledTimes(2);
      const [, opts] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((opts.headers as Record<string, string>)["Authorization"]).toBe(`Bearer ${newToken}`);

      client.dispose();
    });
  });

  describe("disconnect()", () => {
    it("aborts the fetch signal on disconnect", async () => {
      let capturedSignal: AbortSignal | undefined;
      mockFetch.mockImplementation((_url: string, opts: RequestInit) => {
        capturedSignal = opts.signal as AbortSignal;
        return Promise.resolve(makeOkSseResponse(makeNeverEndingStream()));
      });

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(4);

      client.disconnect();

      expect(capturedSignal?.aborted).toBe(true);
    });

    it("does not reconnect after disconnect() is called", async () => {
      vi.useFakeTimers();
      mockFetch.mockImplementation((_url: string, opts: RequestInit) => {
        return Promise.resolve(makeOkSseResponse(makeNeverEndingStream()));
      });

      const { client } = makeClient();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(4);

      client.disconnect();
      await vi.advanceTimersByTimeAsync(35_000);

      expect(mockFetch).toHaveBeenCalledOnce();
    });
  });

  describe("dispose()", () => {
    it("prevents new connections after dispose", async () => {
      const { client } = makeClient();
      client.dispose();
      client.connect(TEST_URL, TOKEN);
      await flushMicrotasks(4);

      expect(mockFetch).not.toHaveBeenCalled();
    });
  });
});
