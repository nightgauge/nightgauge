/**
 * Tests for EventStreamService (Issue #3321, refactored in #3711, #3919)
 *
 * Covers SSE connection, audit + workflow node-tree event parsing, status
 * emitters, reconnect backoff, dispose/abort, and Last-Event-ID resume
 * behavior. EventStreamService delegates stream I/O to PlatformSseClient and
 * forwards the canonical WorkflowEvent node tree verbatim (#3919) — the old
 * PipelineEvent string-matching mirror (#3714) is gone.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import { EventStreamService } from "../../src/services/EventStreamService";
import type { Logger } from "../../src/utils/logger";
import type { TokenRefreshManager } from "../../src/platform/TokenRefreshManager";

// ---------------------------------------------------------------------------
// Mocks
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
  return {
    EventEmitter,
  };
});

const mockGlobalState = {
  get: vi.fn<[string], string | undefined>().mockReturnValue(undefined),
  update: vi.fn<[string, unknown], Promise<void>>().mockResolvedValue(undefined),
};

const mockContext = {
  globalState: mockGlobalState,
} as unknown as vscode.ExtensionContext;

const mockLogger: Logger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
} as unknown as Logger;

const mockTokenRefreshManager = {
  forceRefresh: vi.fn<[], Promise<string | null>>().mockResolvedValue(null),
} as unknown as TokenRefreshManager;

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const BASE_URL = "https://api.example.com";
const TOKEN = "test-token-abc";

/** Flush N microtask ticks without advancing fake timers (safe with vi.useFakeTimers). */
async function flushMicrotasks(n = 12): Promise<void> {
  for (let i = 0; i < n; i++) await Promise.resolve();
}

function makeSseStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  let i = 0;
  return new ReadableStream({
    pull(ctrl) {
      if (i < chunks.length) {
        ctrl.enqueue(encoder.encode(chunks[i++]));
      } else {
        ctrl.close();
      }
    },
  });
}

function makeOkSseResponse(stream: ReadableStream): Response {
  return {
    ok: true,
    status: 200,
    body: stream,
    headers: {
      get: (name: string) => (name === "content-type" ? "text/event-stream; charset=utf-8" : null),
    },
    clone: () => makeOkSseResponse(stream),
  } as unknown as Response;
}

function makeErrorResponse(status: number): Response {
  return {
    ok: false,
    status,
    body: null,
    headers: { get: () => null },
    statusText: "Error",
    clone: () => ({ text: () => Promise.resolve("") }),
    text: () => Promise.resolve(""),
  } as unknown as Response;
}

function makeServiceOpts() {
  return {
    context: mockContext,
    logger: mockLogger,
    tokenRefreshManager: mockTokenRefreshManager,
  };
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

beforeEach(() => {
  EventStreamService.resetInstance();
  vi.clearAllMocks();
  vi.useFakeTimers();
});

afterEach(() => {
  EventStreamService.resetInstance();
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("EventStreamService", () => {
  // #3925 — non-throwing accessor for the reconnect command path.
  describe("getInstanceOrNull()", () => {
    it("returns null when uninitialized and does not throw", () => {
      expect(() => EventStreamService.getInstanceOrNull()).not.toThrow();
      expect(EventStreamService.getInstanceOrNull()).toBeNull();
    });

    it("returns the live instance once initialized", () => {
      const svc = EventStreamService.getInstance(makeServiceOpts());
      expect(EventStreamService.getInstanceOrNull()).toBe(svc);
    });
  });

  describe("connect()", () => {
    it("fetches the correct SSE URL with auth header", async () => {
      const stream = makeSseStream(""); // empty stream, closes immediately
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);

      await flushMicrotasks(16);

      expect(mockFetch).toHaveBeenCalledOnce();
      const [url, opts] = mockFetch.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(`${BASE_URL}/v1/events/stream`);
      expect((opts.headers as Record<string, string>)["Authorization"]).toBe(`Bearer ${TOKEN}`);
      expect((opts.headers as Record<string, string>)["Accept"]).toBe("text/event-stream");
    });

    it("sends Last-Event-ID header when globalState has a saved cursor", async () => {
      mockGlobalState.get.mockReturnValueOnce("evt-cursor-42");
      const stream = makeSseStream("");
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      const [, opts] = mockFetch.mock.calls[0] as [string, RequestInit];
      expect((opts.headers as Record<string, string>)["Last-Event-ID"]).toBe("evt-cursor-42");
    });

    it("does not open a second connection if already connecting", async () => {
      const stream = makeSseStream("");
      mockFetch.mockResolvedValue(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);
      svc.connect(BASE_URL, TOKEN); // second call — should be ignored

      await flushMicrotasks(16);

      expect(mockFetch).toHaveBeenCalledOnce();
    });
  });

  describe("onStreamStatusChanged", () => {
    it("fires 'connected' after a successful stream open", async () => {
      const stream = makeSseStream("");
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const statusEvents: string[] = [];
      svc.onStreamStatusChanged((e) => statusEvents.push(e.status));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(statusEvents).toContain("connected");
    });

    it("fires 'reconnecting' after a fetch error", async () => {
      mockFetch.mockResolvedValueOnce(makeErrorResponse(503));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const statusEvents: string[] = [];
      svc.onStreamStatusChanged((e) => statusEvents.push(e.status));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(statusEvents).toContain("reconnecting");
    });
  });

  describe("onAuditLiveEvent", () => {
    it("fires with parsed AuditLogEntry when receiving an audit_* SSE event", async () => {
      const entry = {
        id: "entry-1",
        timestamp: "2026-05-13T12:00:00Z",
        userId: "user-xyz",
        action: "audit_issue_created",
        status: "success",
      };
      const sseData = `id:evt-1\ndata:${JSON.stringify({ type: "audit_issue_created", id: entry.id, timestamp: entry.timestamp, userId: entry.userId, action: entry.action, status: entry.status })}\n\n`;
      const stream = makeSseStream(sseData);
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const receivedEntries: unknown[] = [];
      svc.onAuditLiveEvent((e) => receivedEntries.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(receivedEntries).toHaveLength(1);
      expect((receivedEntries[0] as { id: string }).id).toBe("entry-1");
      expect((receivedEntries[0] as { action: string }).action).toBe("audit_issue_created");
    });

    it("persists the event ID to globalState for cursor-based resume", async () => {
      const sseData = `id:evt-cursor-99\ndata:${JSON.stringify({ type: "audit_test", id: "e1", timestamp: new Date().toISOString(), userId: "u", action: "audit_test", status: "success" })}\n\n`;
      const stream = makeSseStream(sseData);
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(mockGlobalState.update).toHaveBeenCalledWith(
        "nightgauge.eventStream.lastEventId",
        "evt-cursor-99"
      );
    });

    it("does not fire for non-audit event types", async () => {
      const sseData = `data:${JSON.stringify({ type: "pipeline_update", id: "x" })}\n\n`;
      const stream = makeSseStream(sseData);
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const auditEvents: unknown[] = [];
      svc.onAuditLiveEvent((e) => auditEvents.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(auditEvents).toHaveLength(0);
    });

    it("ignores keepalive SSE comment lines (starting with ':')", async () => {
      const sseData = ":ok\n\n";
      const stream = makeSseStream(sseData);
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(stream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const auditEvents: unknown[] = [];
      svc.onAuditLiveEvent((e) => auditEvents.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(auditEvents).toHaveLength(0);
    });
  });

  describe("onWorkflowEvent (#3919 — node tree, reverses #3714 mirror)", () => {
    const usage = {
      inputTokens: 100,
      outputTokens: 50,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.0123,
      estimated: false,
    };

    const runNode = {
      schemaVersion: 4,
      kind: "run",
      nodeId: "run:42",
      parentId: null,
      seq: 0,
      ts: "2026-05-23T10:00:00Z",
      status: "running",
      runId: "run:42",
      issueNumber: 42,
      backend: "sdk-fanout",
      startedAt: "2026-05-23T10:00:00Z",
    };

    const agentNode = {
      schemaVersion: 4,
      kind: "agent",
      nodeId: "agent:42:finder",
      parentId: "phase:42:feature-dev",
      seq: 5,
      ts: "2026-05-23T10:01:00Z",
      status: "succeeded",
      agentId: "finder",
      provider: "codex",
      usage,
      terminalKind: "success",
    };

    it("forwards a run node verbatim (nodeId/parentId/seq intact)", async () => {
      const sseData = `data:${JSON.stringify(runNode)}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const events: unknown[] = [];
      svc.onWorkflowEvent((e) => events.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(events).toHaveLength(1);
      expect(events[0]).toMatchObject({
        kind: "run",
        nodeId: "run:42",
        parentId: null,
        seq: 0,
        backend: "sdk-fanout",
      });
    });

    it("forwards an agent node with its seq and usage preserved", async () => {
      const sseData = `data:${JSON.stringify(agentNode)}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const events: unknown[] = [];
      svc.onWorkflowEvent((e) => events.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(events).toHaveLength(1);
      expect((events[0] as { seq: number }).seq).toBe(5);
      expect((events[0] as { usage: { costUsd: number } }).usage.costUsd).toBeCloseTo(0.0123);
    });

    it("unwraps a node nested under an SSE `data` envelope", async () => {
      const sseData = `data:${JSON.stringify({ type: "workflow.node", data: runNode })}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const events: unknown[] = [];
      svc.onWorkflowEvent((e) => events.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(events).toHaveLength(1);
      expect((events[0] as { nodeId: string }).nodeId).toBe("run:42");
    });

    it("drops payloads that fail the v4 Zod parse (no string matching)", async () => {
      // Old flat pipeline.* event — no longer a valid node emission.
      const legacy = {
        type: "pipeline.stage.started",
        runId: "run-uuid-1",
        stage: "feature-dev",
        timestamp: "2026-05-23T10:00:00Z",
      };
      const sseData = `data:${JSON.stringify(legacy)}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const events: unknown[] = [];
      svc.onWorkflowEvent((e) => events.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(events).toHaveLength(0);
    });

    it("does not fire onWorkflowEvent for audit_* events", async () => {
      const sseData = `data:${JSON.stringify({ type: "audit_issue_created", id: "e1", timestamp: new Date().toISOString(), userId: "u", action: "audit_issue_created", status: "success" })}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const events: unknown[] = [];
      svc.onWorkflowEvent((e) => events.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(events).toHaveLength(0);
    });

    it("does not fire onAuditLiveEvent for workflow node events", async () => {
      const sseData = `data:${JSON.stringify(runNode)}\n\n`;
      mockFetch.mockResolvedValueOnce(makeOkSseResponse(makeSseStream(sseData)));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      const auditEvents: unknown[] = [];
      svc.onAuditLiveEvent((e) => auditEvents.push(e));

      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(16);

      expect(auditEvents).toHaveLength(0);
    });
  });

  describe("reconnect backoff", () => {
    it("schedules reconnect with exponential backoff on stream failure", async () => {
      // First call fails; second call succeeds (empty stream) to stop the loop
      const emptyStream = makeSseStream("");
      mockFetch
        .mockResolvedValueOnce(makeErrorResponse(500))
        .mockResolvedValueOnce(makeOkSseResponse(emptyStream));

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);

      // Let the first fetch failure resolve and schedule the reconnect timer
      await Promise.resolve();
      await Promise.resolve();

      expect(mockFetch).toHaveBeenCalledOnce();

      // Advance 1.5s — first reconnect timer fires (1000ms + jitter)
      await vi.advanceTimersByTimeAsync(1500);
      await Promise.resolve();
      await Promise.resolve();

      expect(mockFetch).toHaveBeenCalledTimes(2);
    });
  });

  describe("401 handling", () => {
    it("calls tokenRefreshManager.forceRefresh() on 401 and reconnects with new token", async () => {
      const newToken = "refreshed-token";
      const emptyStream = makeSseStream("");
      const refreshManager = {
        forceRefresh: vi.fn<[], Promise<string | null>>().mockResolvedValue(newToken),
      } as unknown as TokenRefreshManager;

      mockFetch
        .mockResolvedValueOnce({
          ok: false,
          status: 401,
          statusText: "Unauthorized",
          body: null,
          headers: { get: () => null },
          clone: () => ({ text: () => Promise.resolve("") }),
          text: () => Promise.resolve(""),
        } as unknown as Response)
        .mockResolvedValueOnce(makeOkSseResponse(emptyStream));

      const svc = EventStreamService.getInstance({
        context: mockContext,
        logger: mockLogger,
        tokenRefreshManager: refreshManager,
      });
      svc.connect(BASE_URL, TOKEN);
      await flushMicrotasks(20);

      expect(refreshManager.forceRefresh).toHaveBeenCalledOnce();
      expect(mockFetch).toHaveBeenCalledTimes(2);
      const [, opts] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((opts.headers as Record<string, string>)["Authorization"]).toBe(`Bearer ${newToken}`);
    });
  });

  describe("dispose()", () => {
    it("aborts the fetch via AbortController on dispose", async () => {
      let capturedSignal: AbortSignal | undefined;
      const neverEndingStream = new ReadableStream({
        start() {
          // never pushes — simulates a long-lived stream
        },
      });
      mockFetch.mockImplementation((_url: string, opts: RequestInit) => {
        capturedSignal = opts.signal as AbortSignal;
        return Promise.resolve(makeOkSseResponse(neverEndingStream));
      });

      const svc = EventStreamService.getInstance(makeServiceOpts());
      svc.connect(BASE_URL, TOKEN);
      // Let connect() run far enough to reach fetch and capture the signal
      await Promise.resolve();
      await Promise.resolve();

      svc.dispose();

      expect(capturedSignal?.aborted).toBe(true);
    });
  });

  describe("singleton", () => {
    it("returns the same instance on subsequent getInstance() calls", () => {
      const a = EventStreamService.getInstance(makeServiceOpts());
      const b = EventStreamService.getInstance();
      expect(a).toBe(b);
    });

    it("throws if getInstance() called with no opts before first init", () => {
      expect(() => EventStreamService.getInstance()).toThrow(
        "EventStreamService requires options on first call"
      );
    });

    it("resets the singleton on resetInstance()", () => {
      const a = EventStreamService.getInstance(makeServiceOpts());
      EventStreamService.resetInstance();
      const b = EventStreamService.getInstance(makeServiceOpts());
      expect(a).not.toBe(b);
    });
  });
});
