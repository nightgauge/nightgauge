/**
 * Integration test for AuditEventClient against a real HTTP server.
 *
 * Spins up a local http.createServer() to receive POST /api/v1/audit/events,
 * exercises end-to-end submission and offline queue retry.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as http from "node:http";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { AuditEventClient } from "../../src/audit/AuditEventClient.js";
import { AuditConfigSchema } from "../../src/audit/schemas.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

interface ReceivedRequest {
  method: string;
  url: string;
  headers: http.IncomingHttpHeaders;
  body: string;
}

function createAuditServer(): {
  server: http.Server;
  requests: ReceivedRequest[];
  responseStatus: { value: number };
  url: () => string;
} {
  const requests: ReceivedRequest[] = [];
  const responseStatus = { value: 200 };

  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", (chunk: Buffer) => {
      body += chunk.toString();
    });
    req.on("end", () => {
      requests.push({
        method: req.method ?? "",
        url: req.url ?? "",
        headers: req.headers,
        body,
      });
      res.writeHead(responseStatus.value, {
        "Content-Type": "application/json",
      });
      res.end(JSON.stringify({ ok: true }));
    });
  });

  return {
    server,
    requests,
    responseStatus,
    url: () => {
      const addr = server.address();
      if (typeof addr === "object" && addr) {
        return `http://127.0.0.1:${addr.port}`;
      }
      throw new Error("Server not listening");
    },
  };
}

function startServer(server: http.Server): Promise<void> {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
}

function stopServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve())));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AuditEventClient integration", () => {
  let tempDir: string;
  let offlineQueuePath: string;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "audit-int-"));
    offlineQueuePath = path.join(tempDir, "audit-queue.json");
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
    vi.restoreAllMocks();
  });

  it("submits events to a real HTTP server", async () => {
    const { server, requests, url } = createAuditServer();
    await startServer(server);

    const config = AuditConfigSchema.parse({
      enabled: true,
      platformUrl: url(),
      apiKey: "integration-test-key",
      offlineQueuePath,
      flushIntervalMs: 60_000,
    });

    const client = new AuditEventClient(config);
    client.enqueue({ action: "pipeline.started", metadata: { issue: 1581 } });
    client.enqueue({ action: "pipeline.completed" });
    await client.flush();
    await client.dispose();

    await stopServer(server);

    expect(requests).toHaveLength(1);
    expect(requests[0].method).toBe("POST");
    expect(requests[0].url).toBe("/api/v1/audit/events");
    expect(requests[0].headers["authorization"]).toBe("Bearer integration-test-key");
    const body = JSON.parse(requests[0].body) as {
      events: Array<{ action: string }>;
    };
    expect(body.events).toHaveLength(2);
    expect(body.events[0].action).toBe("pipeline.started");
    expect(body.events[1].action).toBe("pipeline.completed");
  });

  it("writes offline queue when server is unavailable, then flushes on reconnect", async () => {
    const config = AuditConfigSchema.parse({
      enabled: true,
      platformUrl: "http://127.0.0.1:19999", // nothing listening here
      apiKey: "test-key",
      offlineQueuePath,
      flushIntervalMs: 60_000,
      timeoutMs: 500,
    });

    // Step 1: enqueue and flush while server is down
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const client = new AuditEventClient(config);
    client.enqueue({ action: "commit.created", resourceId: "abc123" });
    await client.flush();
    await client.dispose();
    stderrSpy.mockRestore();

    // Offline queue file should exist with the event
    const queueContent = await fs.readFile(offlineQueuePath, "utf-8");
    const queued = JSON.parse(queueContent) as Array<{
      action: string;
      _queuedAt: string;
    }>;
    expect(queued).toHaveLength(1);
    expect(queued[0].action).toBe("commit.created");
    expect(queued[0]._queuedAt).toBeDefined();

    // Step 2: start a real server and retry via flushOfflineQueue
    const { server, requests, url: serverUrl } = createAuditServer();
    await startServer(server);

    const config2 = AuditConfigSchema.parse({
      enabled: true,
      platformUrl: serverUrl(),
      apiKey: "test-key",
      offlineQueuePath,
      flushIntervalMs: 60_000,
    });
    const client2 = new AuditEventClient(config2);
    await client2.flushOfflineQueue();
    await client2.dispose();

    await stopServer(server);

    // Server should have received the event; _queuedAt should be stripped
    expect(requests).toHaveLength(1);
    const body = JSON.parse(requests[0].body) as {
      events: Array<{ action: string; _queuedAt?: string }>;
    };
    expect(body.events[0].action).toBe("commit.created");
    expect(body.events[0]._queuedAt).toBeUndefined();

    // Offline queue file should be deleted after successful submission
    await expect(fs.access(offlineQueuePath)).rejects.toThrow();
  });

  it("does not delete offline queue when server returns 5xx", async () => {
    const { server, responseStatus, url: serverUrl } = createAuditServer();
    responseStatus.value = 503;
    await startServer(server);

    // Pre-populate the offline queue
    const queued = [{ action: "pr.created", _queuedAt: "2026-01-01T00:00:00Z" }];
    await fs.writeFile(offlineQueuePath, JSON.stringify(queued), "utf-8");

    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const config = AuditConfigSchema.parse({
      enabled: true,
      platformUrl: serverUrl(),
      apiKey: "test-key",
      offlineQueuePath,
      flushIntervalMs: 60_000,
    });
    const client = new AuditEventClient(config);
    await client.flushOfflineQueue();
    await client.dispose();
    stderrSpy.mockRestore();

    await stopServer(server);

    // Offline queue should still exist (not deleted)
    const remaining = await fs.readFile(offlineQueuePath, "utf-8");
    expect(JSON.parse(remaining)).toHaveLength(1);
  });
});
