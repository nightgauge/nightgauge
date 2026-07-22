/**
 * agent-lifecycle.integration.test.ts
 *
 * Integration test for the full agent lifecycle:
 *   register → heartbeat × 2 → deregister
 *
 * Uses an in-process node:http server per ADR from #2092.
 * To run against a real platform: set PLATFORM_TEST_URL env var.
 *
 * Timer note: AgentHeartbeatService.sendHeartbeat() is private. Rather than
 * fighting fake-timer/I/O interplay, the integration tests call it via type
 * cast to drive the HTTP sequence deterministically. Timer behavior is covered
 * by AgentHeartbeatService.test.ts unit tests.
 *
 * @see Issue #3548
 * @see .nightgauge/knowledge/features/2092-end-to-end-integration-tests/decisions.md
 */

import http from "node:http";
import type { AddressInfo } from "node:net";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AgentRegistrationService } from "../../src/services/AgentRegistrationService";
import type { AgentRegistrationPayload } from "../../src/services/AgentRegistrationService";
import { AgentHeartbeatService } from "../../src/services/AgentHeartbeatService";

vi.mock("vscode", () => ({}));

// ── Test helpers ──────────────────────────────────────────────────────────────

type RecordedRequest = {
  method: string;
  path: string;
  body: unknown;
  headers: Record<string, string | string[] | undefined>;
};

function makeTokenStorage(token: string | null = "test-token") {
  return {
    retrieve: vi.fn().mockResolvedValue(token),
    store: vi.fn(),
    delete: vi.fn(),
  };
}

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

function readBody(req: http.IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (chunk) => (data += chunk));
    req.on("end", () => resolve(data));
  });
}

/** Call AgentHeartbeatService.sendHeartbeat() directly (bypasses 30s interval). */
async function triggerHeartbeat(svc: AgentHeartbeatService): Promise<void> {
  await (svc as unknown as { sendHeartbeat(): Promise<void> }).sendHeartbeat();
}

const REGISTRATION_PAYLOAD: AgentRegistrationPayload = {
  agent_version: "0.1.0",
  capabilities: ["headless", "interactive"],
  repos: [{ owner: "nightgauge", repo: "nightgauge" }],
  machine_id: "machine-test-001",
  vscode_version: "1.90.0",
};

// ── Suite ─────────────────────────────────────────────────────────────────────

describe("agent lifecycle integration", () => {
  let server: http.Server;
  let platformUrl: string;
  let receivedRequests: RecordedRequest[];

  beforeEach(async () => {
    receivedRequests = [];

    server = http.createServer(async (req, res) => {
      const rawBody = await readBody(req);
      let parsedBody: unknown = null;
      if (rawBody) {
        try {
          parsedBody = JSON.parse(rawBody);
        } catch {
          parsedBody = rawBody;
        }
      }

      receivedRequests.push({
        method: req.method ?? "",
        path: req.url ?? "",
        body: parsedBody,
        headers: req.headers as Record<string, string | string[] | undefined>,
      });

      res.setHeader("Content-Type", "application/json");

      if (req.method === "POST" && req.url === "/v1/agents/register") {
        res.writeHead(200);
        res.end(JSON.stringify({ agentId: "test-agent-001" }));
        return;
      }

      if (req.method === "PUT" && req.url?.match(/^\/v1\/agents\/[^/]+\/heartbeat$/)) {
        res.writeHead(200);
        res.end(JSON.stringify({}));
        return;
      }

      if (req.method === "DELETE" && req.url?.match(/^\/v1\/agents\/[^/]+$/)) {
        res.writeHead(200);
        res.end(JSON.stringify({}));
        return;
      }

      res.writeHead(404);
      res.end(JSON.stringify({ error: "not found" }));
    });

    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const addr = server.address() as AddressInfo;
    platformUrl = `http://127.0.0.1:${addr.port}`;
  });

  afterEach(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  });

  it("completes register → heartbeat × 2 → deregister sequence", async () => {
    const tokenStorage = makeTokenStorage();
    const logger = makeLogger();

    const registrationService = new AgentRegistrationService(
      () => platformUrl,
      tokenStorage,
      logger
    );
    const heartbeatService = new AgentHeartbeatService(() => platformUrl, tokenStorage, logger);

    // Register
    const agentId = await registrationService.register(REGISTRATION_PAYLOAD);
    expect(agentId).toBe("test-agent-001");

    // Activate heartbeat service so agentId is set, then trigger two heartbeats directly
    heartbeatService.start(agentId!);
    await triggerHeartbeat(heartbeatService);
    await triggerHeartbeat(heartbeatService);

    // Deregister
    await registrationService.deregister(agentId!);
    heartbeatService.dispose();

    expect(receivedRequests).toHaveLength(4);

    const [register, hb1, hb2, deregister] = receivedRequests;

    expect(register.method).toBe("POST");
    expect(register.path).toBe("/v1/agents/register");
    expect(register.headers["authorization"]).toBe("Bearer test-token");
    expect(register.body).toMatchObject({
      agent_version: "0.1.0",
      capabilities: ["headless", "interactive"],
      repos: [{ owner: "nightgauge", repo: "nightgauge" }],
      machine_id: "machine-test-001",
      vscode_version: "1.90.0",
    });

    expect(hb1.method).toBe("PUT");
    expect(hb1.path).toBe("/v1/agents/test-agent-001/heartbeat");
    expect(hb1.headers["authorization"]).toBe("Bearer test-token");

    expect(hb2.method).toBe("PUT");
    expect(hb2.path).toBe("/v1/agents/test-agent-001/heartbeat");

    expect(deregister.method).toBe("DELETE");
    expect(deregister.path).toBe("/v1/agents/test-agent-001");
    expect(deregister.headers["authorization"]).toBe("Bearer test-token");
  });

  it("includes repos field in registration payload (workspace repo sync)", async () => {
    const registrationService = new AgentRegistrationService(
      () => platformUrl,
      makeTokenStorage(),
      makeLogger()
    );

    await registrationService.register(REGISTRATION_PAYLOAD);

    const registerReq = receivedRequests.find((r) => r.path === "/v1/agents/register");
    expect(registerReq).toBeDefined();
    expect((registerReq!.body as AgentRegistrationPayload).repos).toEqual([
      { owner: "nightgauge", repo: "nightgauge" },
    ]);
  });

  it("does not send heartbeats after dispose()", async () => {
    const tokenStorage = makeTokenStorage();
    const logger = makeLogger();

    const registrationService = new AgentRegistrationService(
      () => platformUrl,
      tokenStorage,
      logger
    );
    const heartbeatService = new AgentHeartbeatService(() => platformUrl, tokenStorage, logger);

    const agentId = await registrationService.register(REGISTRATION_PAYLOAD);
    heartbeatService.start(agentId!);
    heartbeatService.dispose();

    // After dispose, sendHeartbeat is a no-op because agentId is cleared
    await triggerHeartbeat(heartbeatService);

    const heartbeatRequests = receivedRequests.filter((r) => r.path.includes("/heartbeat"));
    expect(heartbeatRequests).toHaveLength(0);
  });
});
