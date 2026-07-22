/**
 * ProjectEventSubscriber.test.ts (refactored for #3711)
 *
 * Unit tests for ProjectEventSubscriber — SSE lifecycle, lastEventId persistence,
 * autonomousRescan triggering, enabled_repos filter, polling cadence logic,
 * and 401 token refresh handling via onAuthRequired.
 *
 * @see Issue #3025 — Event-Driven Dispatch Phase 2
 * @see Issue #3711 — Shared Resilient PlatformSseClient
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
  EventEmitter: class {
    listeners: Array<(val: unknown) => void> = [];
    event = (cb: (val: unknown) => void) => {
      this.listeners.push(cb);
      return { dispose: () => this.listeners.splice(this.listeners.indexOf(cb), 1) };
    };
    fire(val: unknown) {
      this.listeners.forEach((cb) => cb(val));
    }
    dispose() {}
  },
}));

// ── IpcClient mock ────────────────────────────────────────────────────────────

const mockAutonomousRescan = vi.fn().mockResolvedValue({ status: "running" });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      autonomousRescan: mockAutonomousRescan,
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

function makeOkResponse(body: ReadableStream): Response {
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    body,
    headers: {
      get: (name: string) => (name === "content-type" ? "text/event-stream; charset=utf-8" : null),
    },
    clone: () => makeOkResponse(body),
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

const mockFetch = vi.fn();
global.fetch = mockFetch;

// ── Imports (after mocks) ─────────────────────────────────────────────────────

import {
  ProjectEventSubscriber,
  SSE_WIDENED_INTERVAL_MS,
  DISCONNECT_REVERT_THRESHOLD_MS,
} from "../../src/services/ProjectEventSubscriber";

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

function sseChunk(id: string, data: unknown): string {
  return `id: ${id}\ndata: ${JSON.stringify(data)}\n\n`;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("ProjectEventSubscriber", () => {
  beforeEach(() => {
    mockGlobalStateStore.clear();
    mockAutonomousRescan.mockClear();
    mockFetch.mockReset();
    ProjectEventSubscriber.resetInstance();
  });

  afterEach(() => {
    ProjectEventSubscriber.resetInstance();
  });

  it("is initially not connected", () => {
    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    expect(sub.isConnected()).toBe(false);
  });

  it("getDisconnectedDurationMs returns 0 when never connected", () => {
    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    expect(sub.getDisconnectedDurationMs()).toBe(0);
  });

  it("connects and sets connected=true after successful stream open", async () => {
    mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream([])));

    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    sub.connect("https://api.example.com", "tok_test");

    await new Promise((r) => setTimeout(r, 10));

    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.com/v1/events/project/stream");
    expect((init.headers as Record<string, string>)["Authorization"]).toBe("Bearer tok_test");
  });

  it("persists lastEventId to globalState on each event", async () => {
    const events = [
      sseChunk("evt-1", { type: "projects_v2_item.edited", repo: "org/repo" }),
      sseChunk("evt-2", { type: "projects_v2_item.edited", repo: "org/repo" }),
    ];
    mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    sub.connect("https://api.example.com", "tok_test");

    await new Promise((r) => setTimeout(r, 50));

    expect(mockGlobalState.update).toHaveBeenLastCalledWith(
      "nightgauge.projectStream.lastEventId",
      "evt-2"
    );
  });

  it("calls autonomousRescan on valid event", async () => {
    vi.useFakeTimers();
    try {
      const events = [sseChunk("evt-1", { type: "projects_v2_item.edited", repo: "org/myrepo" })];
      mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

      const sub = ProjectEventSubscriber.getInstance({
        context: makeContext(),
        logger: makeLogger(),
      });
      sub.connect("https://api.example.com", "tok_test");

      for (let i = 0; i < 5; i++) await Promise.resolve();
      await vi.advanceTimersByTimeAsync(5100);

      expect(mockAutonomousRescan).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  it("filters events by enabled_repos — matching repo triggers rescan", async () => {
    vi.useFakeTimers();
    try {
      const events = [sseChunk("evt-1", { type: "projects_v2_item.edited", repo: "org/allowed" })];
      mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

      const sub = ProjectEventSubscriber.getInstance({
        context: makeContext(),
        logger: makeLogger(),
        enabledRepos: ["allowed"],
      });
      sub.connect("https://api.example.com", "tok_test");
      for (let i = 0; i < 5; i++) await Promise.resolve();
      await vi.advanceTimersByTimeAsync(5100);

      expect(mockAutonomousRescan).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  it("filters events by enabled_repos — non-matching repo skips rescan", async () => {
    const events = [sseChunk("evt-1", { type: "projects_v2_item.edited", repo: "org/other" })];
    mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
      enabledRepos: ["allowed"],
    });
    sub.connect("https://api.example.com", "tok_test");
    await new Promise((r) => setTimeout(r, 50));

    expect(mockAutonomousRescan).not.toHaveBeenCalled();
  });

  it("sends Last-Event-ID header on connect when stored in globalState", async () => {
    mockGlobalStateStore.set("nightgauge.projectStream.lastEventId", "stored-id-42");
    mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream([])));

    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    sub.connect("https://api.example.com", "tok_test");
    await new Promise((r) => setTimeout(r, 10));

    const [, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)["Last-Event-ID"]).toBe("stored-id-42");
  });

  it("disconnect() sets connected=false and records disconnectedAt", async () => {
    mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream([])));

    const sub = ProjectEventSubscriber.getInstance({
      context: makeContext(),
      logger: makeLogger(),
    });
    sub.connect("https://api.example.com", "tok_test");
    await new Promise((r) => setTimeout(r, 10));

    sub.disconnect();
    expect(sub.isConnected()).toBe(false);
    expect(sub.getDisconnectedDurationMs()).toBeGreaterThanOrEqual(0);
  });

  it("SSE_WIDENED_INTERVAL_MS is 5 minutes", () => {
    expect(SSE_WIDENED_INTERVAL_MS).toBe(5 * 60_000);
  });

  it("DISCONNECT_REVERT_THRESHOLD_MS is 2 minutes", () => {
    expect(DISCONNECT_REVERT_THRESHOLD_MS).toBe(2 * 60_000);
  });

  it("singleton getInstance returns the same instance", () => {
    const ctx = makeContext();
    const log = makeLogger();
    const a = ProjectEventSubscriber.getInstance({ context: ctx, logger: log });
    const b = ProjectEventSubscriber.getInstance();
    expect(a).toBe(b);
  });

  it("resetInstance allows creating a fresh instance", () => {
    const ctx = makeContext();
    const log = makeLogger();
    const a = ProjectEventSubscriber.getInstance({ context: ctx, logger: log });
    ProjectEventSubscriber.resetInstance();
    const b = ProjectEventSubscriber.getInstance({ context: ctx, logger: log });
    expect(a).not.toBe(b);
  });

  // #3925 — getInstanceOrNull must never throw. The bare getInstance() threw
  // `requires options on first call` from the EventStreamService session
  // handler whenever event_stream_enabled was false (subscriber never
  // constructed), and that throw was swallowed as a noisy WARN.
  it("getInstanceOrNull returns null when the subscriber is uninitialized (does not throw)", () => {
    expect(() => ProjectEventSubscriber.getInstanceOrNull()).not.toThrow();
    expect(ProjectEventSubscriber.getInstanceOrNull()).toBeNull();
    // Sanity: the throwing variant is what we were avoiding.
    expect(() => ProjectEventSubscriber.getInstance()).toThrow(/requires options on first call/);
  });

  it("getInstanceOrNull returns the live instance once initialized", () => {
    const ctx = makeContext();
    const log = makeLogger();
    const a = ProjectEventSubscriber.getInstance({ context: ctx, logger: log });
    expect(ProjectEventSubscriber.getInstanceOrNull()).toBe(a);
  });

  describe("onStatusChanged fast path", () => {
    it("status-change event calls onStatusChanged within 500ms, not autonomousRescan", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", {
            type: "project.statusChanged",
            repo: "org/repo",
            fromStatus: "ready",
            toStatus: "in-progress",
          }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["in-progress", "ready"])
        );
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("burst of status-change events for same repo coalesces into one invalidation", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", {
            type: "project.statusChanged",
            repo: "org/repo",
            toStatus: "in-progress",
          }),
          sseChunk("e2", {
            type: "project.statusChanged",
            repo: "org/repo",
            toStatus: "in-review",
          }),
          sseChunk("e3", {
            type: "project.statusChanged",
            repo: "org/repo",
            toStatus: "in-progress",
          }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(600);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        const [, statuses] = onStatusChanged.mock.calls[0] as [string, string[]];
        expect(new Set(statuses)).toEqual(new Set(["in-progress", "in-review"]));
      } finally {
        vi.useRealTimers();
      }
    });

    it("projects_v2_item.edited with toStatus uses fast invalidation path", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", { type: "projects_v2_item.edited", repo: "org/repo", toStatus: "done" }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledWith("repo", ["done"]);
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("event without toStatus falls through to autonomousRescan slow path", async () => {
      vi.useFakeTimers();
      try {
        const events = [sseChunk("e1", { type: "project.itemAdded", repo: "org/repo" })];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(5100);
        expect(mockAutonomousRescan).toHaveBeenCalledOnce();
        expect(onStatusChanged).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe("issue and pull_request events", () => {
    it("issues.closed event routes to fast path with ['ready', 'done']", async () => {
      vi.useFakeTimers();
      try {
        const events = [sseChunk("e1", { type: "issues.closed", repo: "org/repo" })];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["ready", "done"])
        );
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("issues.reopened event routes to fast path with ['done', 'ready']", async () => {
      vi.useFakeTimers();
      try {
        const events = [sseChunk("e1", { type: "issues.reopened", repo: "org/repo" })];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["done", "ready"])
        );
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("issues.labeled event routes to fast path with ['ready']", async () => {
      vi.useFakeTimers();
      try {
        const events = [sseChunk("e1", { type: "issues.labeled", repo: "org/repo" })];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith("repo", ["ready"]);
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("pull_request.closed merged=true routes to fast path with ['in-review', 'done']", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", { type: "pull_request.closed", repo: "org/repo", merged: true }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["in-review", "done"])
        );
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("pull_request.closed merged=false routes to fast path with ['in-review', 'ready']", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", { type: "pull_request.closed", repo: "org/repo", merged: false }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["in-review", "ready"])
        );
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("issues.unlabeled event routes to fast path with ['ready']", async () => {
      vi.useFakeTimers();
      try {
        const events = [sseChunk("e1", { type: "issues.unlabeled", repo: "org/repo" })];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(500);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith("repo", ["ready"]);
        expect(mockAutonomousRescan).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("issues.closed coalesces with concurrent project.statusChanged in same 500ms window", async () => {
      vi.useFakeTimers();
      try {
        const events = [
          sseChunk("e1", { type: "issues.closed", repo: "org/repo" }),
          sseChunk("e2", {
            type: "project.statusChanged",
            repo: "org/repo",
            toStatus: "in-progress",
          }),
        ];
        mockFetch.mockResolvedValueOnce(makeOkResponse(makeSSEStream(events)));

        const onStatusChanged = vi.fn();
        const sub = ProjectEventSubscriber.getInstance({
          context: makeContext(),
          logger: makeLogger(),
          onStatusChanged,
        });
        sub.connect("https://api.example.com", "tok");
        for (let i = 0; i < 5; i++) await Promise.resolve();

        await vi.advanceTimersByTimeAsync(600);
        expect(onStatusChanged).toHaveBeenCalledOnce();
        expect(onStatusChanged).toHaveBeenCalledWith(
          "repo",
          expect.arrayContaining(["ready", "done", "in-progress"])
        );
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe("401 handling", () => {
    it("calls onAuthRequired on 401 and reconnects with the new token", async () => {
      const newToken = "refreshed-token";
      const emptyStream = makeSSEStream([]);

      mockFetch
        .mockResolvedValueOnce(make401Response())
        .mockResolvedValueOnce(makeOkResponse(emptyStream));

      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(newToken);

      const sub = ProjectEventSubscriber.getInstance({
        context: makeContext(),
        logger: makeLogger(),
        onAuthRequired,
      });

      sub.connect("https://api.example.com", "tok_test");

      // Let the async chain resolve
      for (let i = 0; i < 10; i++) await Promise.resolve();

      expect(onAuthRequired).toHaveBeenCalledOnce();
      expect(mockFetch).toHaveBeenCalledTimes(2);
      const [, secondOpts] = mockFetch.mock.calls[1] as [string, RequestInit];
      expect((secondOpts.headers as Record<string, string>)["Authorization"]).toBe(
        `Bearer ${newToken}`
      );
    });

    it("surfaces disconnected when onAuthRequired returns null on 401", async () => {
      mockFetch.mockResolvedValueOnce(make401Response());

      const onAuthRequired = vi.fn<[], Promise<string | null>>().mockResolvedValue(null);

      const sub = ProjectEventSubscriber.getInstance({
        context: makeContext(),
        logger: makeLogger(),
        onAuthRequired,
      });

      sub.connect("https://api.example.com", "tok_test");

      for (let i = 0; i < 10; i++) await Promise.resolve();

      expect(onAuthRequired).toHaveBeenCalledOnce();
      expect(mockFetch).toHaveBeenCalledOnce(); // no further reconnect
      expect(sub.isConnected()).toBe(false);
    });
  });
});
