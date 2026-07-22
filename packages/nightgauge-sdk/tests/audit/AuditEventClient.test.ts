import { describe, it, expect, beforeEach, afterEach, vi, type MockInstance } from "vitest";
import * as fsMod from "node:fs/promises";
import { EventBus, PipelineRunEmitter } from "../../src/events/EventBus.js";
import { AuditEventClient } from "../../src/audit/AuditEventClient.js";
import { AuditConfigSchema } from "../../src/audit/schemas.js";

vi.mock("node:fs/promises");

const fs = fsMod as {
  readFile: MockInstance;
  writeFile: MockInstance;
  mkdir: MockInstance;
  unlink: MockInstance;
};

const BASE_CONFIG = AuditConfigSchema.parse({
  enabled: true,
  platformUrl: "http://localhost:9999",
  apiKey: "test-key",
  flushIntervalMs: 60_000, // long interval — we flush manually in tests
  offlineQueuePath: "/tmp/test-audit-queue.json",
});

function mockFetch(status: number, body: object = {}): MockInstance {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("fetch", vi.fn());
  // Default fs mocks — most tests override individually
  fs.readFile.mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
  fs.writeFile.mockResolvedValue(undefined);
  fs.mkdir.mockResolvedValue(undefined);
  fs.unlink.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// enabled=false — all operations are no-ops
// ---------------------------------------------------------------------------

describe("when disabled", () => {
  it("enqueue is a no-op", () => {
    const client = new AuditEventClient(AuditConfigSchema.parse({ enabled: false }));
    client.enqueue({ action: "pipeline.started" });
    expect(vi.mocked(fetch)).not.toHaveBeenCalled();
  });

  it("flush is a no-op", async () => {
    const client = new AuditEventClient(AuditConfigSchema.parse({ enabled: false }));
    await client.flush();
    expect(vi.mocked(fetch)).not.toHaveBeenCalled();
  });

  it("dispose is a no-op", async () => {
    const client = new AuditEventClient(AuditConfigSchema.parse({ enabled: false }));
    await client.dispose();
    expect(vi.mocked(fetch)).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// enqueue
// ---------------------------------------------------------------------------

describe("enqueue", () => {
  it("queues a valid event", async () => {
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "pipeline.started", metadata: { issue: 42 } });
    await client.flush();
    expect(fetchMock).toHaveBeenCalledOnce();
    const body = JSON.parse(fetchMock.mock.calls[0][1].body as string) as {
      events: unknown[];
    };
    expect(body.events).toHaveLength(1);
    expect(body.events[0]).toMatchObject({ action: "pipeline.started" });
    await client.dispose();
  });

  it("discards an invalid event (bad action) and logs to stderr", async () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "not.a.real.action" });
    await client.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(stderrSpy).toHaveBeenCalledWith(
      expect.stringContaining("Invalid audit event discarded")
    );
    stderrSpy.mockRestore();
    await client.dispose();
  });

  it("auto-flushes when batchSize is reached", async () => {
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(AuditConfigSchema.parse({ ...BASE_CONFIG, batchSize: 2 }));
    client.enqueue({ action: "pipeline.started" });
    // Second enqueue should trigger auto-flush
    client.enqueue({ action: "pipeline.completed" });
    // Give the async flush a tick to execute
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledOnce();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// flush
// ---------------------------------------------------------------------------

describe("flush", () => {
  it("is a no-op when queue is empty", async () => {
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    await client.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    await client.dispose();
  });

  it("POSTs with correct Authorization header and body", async () => {
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "skill.invoked", resourceId: "feature-dev" });
    await client.flush();
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0] as [
      string,
      RequestInit & { headers: Record<string, string> },
    ];
    expect(url).toBe("http://localhost:9999/api/v1/audit/events");
    expect(init.headers["Authorization"]).toBe("Bearer test-key");
    expect(init.headers["Content-Type"]).toBe("application/json");
    await client.dispose();
  });

  it("writes to offline queue when fetch throws (network error)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("ECONNREFUSED")));
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "pipeline.started" });
    await client.flush();
    expect(fs.writeFile).toHaveBeenCalledWith(
      "/tmp/test-audit-queue.json",
      expect.stringContaining('"pipeline.started"'),
      "utf-8"
    );
    await client.dispose();
  });

  it("writes to offline queue on 5xx response", async () => {
    mockFetch(503);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "pipeline.failed" });
    await client.flush();
    expect(fs.writeFile).toHaveBeenCalledWith(
      "/tmp/test-audit-queue.json",
      expect.stringContaining('"pipeline.failed"'),
      "utf-8"
    );
    await client.dispose();
  });

  it("discards events on 4xx response (no offline queue)", async () => {
    mockFetch(422);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "pipeline.started" });
    await client.flush();
    expect(fs.writeFile).not.toHaveBeenCalled();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// flushAll + offline queue
// ---------------------------------------------------------------------------

describe("flushAll", () => {
  it("reads offline queue and POSTs remaining events", async () => {
    const offlineEvents = [{ action: "pipeline.started", _queuedAt: "2026-01-01T00:00:00Z" }];
    fs.readFile.mockResolvedValue(JSON.stringify(offlineEvents));
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    await client.flushAll();
    expect(fetchMock).toHaveBeenCalledOnce();
    const body = JSON.parse(fetchMock.mock.calls[0][1].body as string) as {
      events: unknown[];
    };
    // _queuedAt should be stripped
    expect(body.events[0]).not.toHaveProperty("_queuedAt");
    expect(body.events[0]).toMatchObject({ action: "pipeline.started" });
    expect(fs.unlink).toHaveBeenCalledWith("/tmp/test-audit-queue.json");
    await client.dispose();
  });

  it("leaves offline queue intact when submission fails", async () => {
    const offlineEvents = [{ action: "pr.created", _queuedAt: "2026-01-01T00:00:00Z" }];
    fs.readFile.mockResolvedValue(JSON.stringify(offlineEvents));
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("timeout")));
    const client = new AuditEventClient(BASE_CONFIG);
    await client.flushAll();
    expect(fs.unlink).not.toHaveBeenCalled();
    await client.dispose();
  });

  it("handles corrupt offline queue JSON gracefully", async () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    fs.readFile.mockResolvedValue("not-valid-json{");
    const client = new AuditEventClient(BASE_CONFIG);
    await client.flushOfflineQueue();
    expect(stderrSpy).toHaveBeenCalledWith(expect.stringContaining("corrupt JSON"));
    expect(fs.unlink).toHaveBeenCalledWith("/tmp/test-audit-queue.json");
    stderrSpy.mockRestore();
    await client.dispose();
  });

  it("handles offline queue with unexpected format (non-array)", async () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    fs.readFile.mockResolvedValue(JSON.stringify({ not: "an array" }));
    const client = new AuditEventClient(BASE_CONFIG);
    await client.flushOfflineQueue();
    expect(stderrSpy).toHaveBeenCalledWith(expect.stringContaining("unexpected format"));
    expect(fs.unlink).toHaveBeenCalledWith("/tmp/test-audit-queue.json");
    stderrSpy.mockRestore();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// offline queue cap
// ---------------------------------------------------------------------------

describe("offline queue cap", () => {
  it("discards oldest events when offlineQueueMaxSize is exceeded", async () => {
    const config = AuditConfigSchema.parse({
      ...BASE_CONFIG,
      offlineQueueMaxSize: 2,
    });
    // Pre-existing queue with 2 events (already at cap)
    const existing = [
      { action: "auth.login", _queuedAt: "2026-01-01T00:00:00Z" },
      { action: "auth.logout", _queuedAt: "2026-01-01T00:00:01Z" },
    ];
    fs.readFile.mockResolvedValue(JSON.stringify(existing));
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network")));
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    const client = new AuditEventClient(config);
    client.enqueue({ action: "pr.created" });
    await client.flush();

    // writeFile should have been called; the written queue should have at most 2 items
    expect(fs.writeFile).toHaveBeenCalled();
    const written = JSON.parse(
      (fs.writeFile.mock.calls[0] as [string, string, string])[1]
    ) as unknown[];
    expect(written.length).toBeLessThanOrEqual(2);
    expect(stderrSpy).toHaveBeenCalledWith(expect.stringContaining("cap reached"));
    stderrSpy.mockRestore();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// dispose
// ---------------------------------------------------------------------------

describe("dispose", () => {
  it("clears the timer and performs a final flush", async () => {
    const fetchMock = mockFetch(200);
    const client = new AuditEventClient(BASE_CONFIG);
    client.enqueue({ action: "pipeline.completed" });
    await client.dispose();
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});

// ---------------------------------------------------------------------------
// EventBus integration
// ---------------------------------------------------------------------------

describe("EventBus integration", () => {
  it("flushes when a phase node reaches a terminal state", async () => {
    const fetchMock = mockFetch(200);
    const bus = new EventBus();
    const emitter = new PipelineRunEmitter(bus, 1581);
    const client = new AuditEventClient(BASE_CONFIG, bus);
    client.enqueue({ action: "pipeline.started" });

    // A stage completing is a phase node reaching `succeeded`.
    emitter.stageStarted("feature-dev");
    emitter.stageCompleted("feature-dev");

    // Flush is async — give it a tick
    await Promise.resolve();
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledOnce();
    await client.dispose();
  });

  it("calls flushAll when the root run node reaches a terminal state", async () => {
    const fetchMock = mockFetch(200);
    fs.readFile.mockRejectedValue(new Error("ENOENT"));
    const bus = new EventBus();
    const emitter = new PipelineRunEmitter(bus, 1581);
    const client = new AuditEventClient(BASE_CONFIG, bus);
    client.enqueue({ action: "pr.merged" });

    // The whole pipeline finishing is the root run node reaching `succeeded`.
    emitter.runStarted();
    emitter.runFinished("succeeded");

    await Promise.resolve();
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalledOnce();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// Config edge cases
// ---------------------------------------------------------------------------

describe("config edge cases", () => {
  it("logs stderr and writes offline queue when platformUrl is not configured", async () => {
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const config = AuditConfigSchema.parse({
      enabled: true,
      // no platformUrl or apiKey
      offlineQueuePath: "/tmp/test-audit-queue.json",
    });
    const client = new AuditEventClient(config);
    client.enqueue({ action: "pipeline.started" });
    await client.flush();
    expect(stderrSpy).toHaveBeenCalledWith(
      expect.stringContaining("platformUrl or apiKey not configured")
    );
    expect(fs.writeFile).toHaveBeenCalled();
    stderrSpy.mockRestore();
    await client.dispose();
  });
});
