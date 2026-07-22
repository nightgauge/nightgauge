/**
 * AgentRegistrationService.getLastFailureDetail (#360).
 *
 * register() collapses every failure mode to a `null` return, which surfaced
 * to the operator as the opaque "Workspace sync failed — Registration returned
 * no agentId" tree item that survived retries. These tests assert the service
 * now records the REAL cause (status + body) so the workspace-sync UI can name
 * it, and that a success clears the detail so a recovered sync isn't sticky.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AgentRegistrationService } from "../../src/services/AgentRegistrationService";

vi.mock("vscode", () => ({
  Disposable: { from: vi.fn() },
}));

function makeTokenStorage(token: string | null) {
  return { retrieve: vi.fn().mockResolvedValue(token) };
}
function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn() };
}
function makeService(token: string | null = "access-token") {
  return new AgentRegistrationService(
    () => "https://api.nightgauge.dev",
    makeTokenStorage(token) as never,
    makeLogger() as never
  );
}
const PAYLOAD = {
  agent_version: "0.1.0",
  capabilities: ["headless"],
  repos: [{ owner: "nightgauge", repo: "nightgauge" }],
  machine_id: "m1",
  vscode_version: "1.85.0",
};

describe("AgentRegistrationService failure detail (#360)", () => {
  let mockFetch: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mockFetch = vi.fn();
    global.fetch = mockFetch;
  });
  afterEach(() => vi.restoreAllMocks());

  it("records HTTP status and body on a 5xx", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 503,
      text: vi.fn().mockResolvedValue("service unavailable"),
    });
    const svc = makeService();
    const id = await svc.register(PAYLOAD);
    expect(id).toBeNull();
    const detail = svc.getLastFailureDetail();
    expect(detail).toContain("HTTP 503");
    expect(detail).toContain("service unavailable");
  });

  it("names an auth failure distinctly (no refresher wired)", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 401,
      text: vi.fn().mockResolvedValue("token expired"),
    });
    const svc = makeService();
    expect(await svc.register(PAYLOAD)).toBeNull();
    expect(svc.getLastFailureDetail()).toContain("authentication failed (HTTP 401)");
  });

  it("reports a network error rather than a bare null", async () => {
    mockFetch.mockRejectedValue(new Error("ECONNREFUSED"));
    const svc = makeService();
    expect(await svc.register(PAYLOAD)).toBeNull();
    expect(svc.getLastFailureDetail()).toContain("network error");
    expect(svc.getLastFailureDetail()).toContain("ECONNREFUSED");
  });

  it("reports a 200 that omits agentId", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ notAgentId: true }),
    });
    const svc = makeService();
    expect(await svc.register(PAYLOAD)).toBeNull();
    expect(svc.getLastFailureDetail()).toContain("did not include an agentId");
  });

  it("reports the no-token case", async () => {
    const svc = makeService(null);
    expect(await svc.register(PAYLOAD)).toBeNull();
    expect(svc.getLastFailureDetail()).toContain("no access token");
  });

  it("clears the detail once registration succeeds (recovered sync is not sticky)", async () => {
    // First: a failure sets the detail.
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 500,
      text: vi.fn().mockResolvedValue("boom"),
    });
    const svc = makeService();
    expect(await svc.register(PAYLOAD)).toBeNull();
    expect(svc.getLastFailureDetail()).toBeDefined();

    // Then: a success clears it.
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ agentId: "agent-123" }),
    });
    expect(await svc.register(PAYLOAD)).toBe("agent-123");
    expect(svc.getLastFailureDetail()).toBeUndefined();
  });
});
