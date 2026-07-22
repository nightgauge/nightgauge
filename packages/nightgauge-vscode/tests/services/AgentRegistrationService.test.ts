import { describe, it, expect, vi, beforeEach } from "vitest";
import { AgentRegistrationService } from "../../src/services/AgentRegistrationService";
import type { AgentRegistrationPayload } from "../../src/services/AgentRegistrationService";

vi.mock("vscode", () => ({}));

vi.stubGlobal("fetch", vi.fn());

const makeTokenStorage = (token: string | null = "test-token") => ({
  retrieve: vi.fn().mockResolvedValue(token),
  store: vi.fn(),
  delete: vi.fn(),
});

const makeLogger = () => ({
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
});

const PLATFORM_URL = "https://api.nightgauge.dev";

const PAYLOAD: AgentRegistrationPayload = {
  agent_version: "0.1.0",
  capabilities: ["headless", "interactive"],
  repos: [{ owner: "nightgauge", repo: "nightgauge" }],
  machine_id: "machine-abc",
  vscode_version: "1.90.0",
};

function makeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe("AgentRegistrationService", () => {
  let tokenStorage: ReturnType<typeof makeTokenStorage>;
  let logger: ReturnType<typeof makeLogger>;
  let service: AgentRegistrationService;

  beforeEach(() => {
    tokenStorage = makeTokenStorage();
    logger = makeLogger();
    service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger);
    vi.mocked(fetch).mockReset();
  });

  it("returns agentId on successful registration", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(200, { agentId: "agent-xyz" }));

    const result = await service.register(PAYLOAD);

    expect(result).toBe("agent-xyz");
    expect(fetch).toHaveBeenCalledWith(
      `${PLATFORM_URL}/v1/agents/register`,
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ Authorization: "Bearer test-token" }),
      })
    );
    expect(logger.info).toHaveBeenCalledWith(
      "AgentRegistrationService: registered",
      expect.objectContaining({ agentId: "agent-xyz" })
    );
  });

  it("returns null and logs warning on 401", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(401, {}));

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    // #360 — failures now name the concrete cause (status + optional body).
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("authentication failed (HTTP 401)")
    );
  });

  it("returns null and logs warning on 403", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(403, {}));

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("authentication failed (HTTP 403)")
    );
  });

  it("returns null and logs warning on 500", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(500, {}));

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("HTTP 500"));
  });

  it("returns null and logs warning on network error", async () => {
    vi.mocked(fetch).mockRejectedValueOnce(new Error("Network failure"));

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("network error: Network failure")
    );
  });

  it("returns null and never calls fetch when no accessToken", async () => {
    tokenStorage = makeTokenStorage(null);
    service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger);

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("no access token"));
  });

  it("returns null and logs warning when response missing agentId", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(200, { id: "wrong-key" }));

    const result = await service.register(PAYLOAD);

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("did not include an agentId"));
  });

  it("sends correct payload fields (repos, capabilities, machine_id, agent_version) in request body", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(makeResponse(200, { agentId: "agent-xyz" }));

    await service.register(PAYLOAD);

    const [, init] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as typeof PAYLOAD;
    expect(body.repos).toEqual([{ owner: "nightgauge", repo: "nightgauge" }]);
    expect(body.capabilities).toEqual(["headless", "interactive"]);
    expect(body.machine_id).toBe("machine-abc");
    expect(body.agent_version).toBe("0.1.0");
    expect(body.vscode_version).toBe("1.90.0");
  });

  it("dispose() completes without error", () => {
    expect(() => service.dispose()).not.toThrow();
  });

  // Registration's 401 recovery delegates to the centralized
  // IOnDemandTokenRefresher (TokenRefreshManager.forceRefresh) so the single-use
  // refresh token is spent once across all refresh paths (#3751). Token rotation
  // and persistence are the manager's responsibility, verified in
  // TokenRefreshManager.test.ts; here we only assert the retry handshake.
  describe("refresh-on-auth-failure", () => {
    /** A refresher that yields a fresh access token on demand. */
    const makeRefresher = (token: string | null = "fresh-token") => ({
      forceRefresh: vi.fn().mockResolvedValue(token),
    });

    it("refreshes the token and retries once on 401, then succeeds", async () => {
      const refresher = makeRefresher();
      service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger, refresher);
      vi.mocked(fetch)
        .mockResolvedValueOnce(makeResponse(401, {}))
        .mockResolvedValueOnce(makeResponse(201, { agentId: "agent-after-refresh" }));

      const result = await service.register(PAYLOAD);

      expect(result).toBe("agent-after-refresh");
      expect(refresher.forceRefresh).toHaveBeenCalledTimes(1);
      expect(fetch).toHaveBeenCalledTimes(2);
      // retry used the refreshed token
      const [, retryInit] = vi.mocked(fetch).mock.calls[1] as [string, RequestInit];
      expect((retryInit.headers as Record<string, string>).Authorization).toBe(
        "Bearer fresh-token"
      );
    });

    it("returns null when refresh yields no token (refresh token dead)", async () => {
      const refresher = makeRefresher(null);
      service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger, refresher);
      vi.mocked(fetch).mockResolvedValueOnce(makeResponse(401, {}));

      const result = await service.register(PAYLOAD);

      expect(result).toBeNull();
      // forceRefresh attempted, but with no new token there is no retry fetch
      expect(refresher.forceRefresh).toHaveBeenCalledTimes(1);
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("authentication failed (HTTP 401)")
      );
    });

    it("does not refresh when no refresher is wired", async () => {
      // Default service (no refresher) — a 401 surfaces directly with no retry.
      vi.mocked(fetch).mockResolvedValueOnce(makeResponse(401, {}));

      const result = await service.register(PAYLOAD);

      expect(result).toBeNull();
      expect(fetch).toHaveBeenCalledTimes(1);
    });

    it("does not refresh when first attempt already succeeds", async () => {
      const refresher = makeRefresher();
      service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger, refresher);
      vi.mocked(fetch).mockResolvedValueOnce(makeResponse(201, { agentId: "agent-1" }));

      const result = await service.register(PAYLOAD);

      expect(result).toBe("agent-1");
      expect(refresher.forceRefresh).not.toHaveBeenCalled();
      expect(fetch).toHaveBeenCalledTimes(1);
    });
  });

  describe("deregister()", () => {
    const AGENT_ID = "agent-xyz";

    it("calls DELETE /v1/agents/{agentId} with correct Bearer token", async () => {
      vi.mocked(fetch).mockResolvedValueOnce(makeResponse(200, {}));

      await service.deregister(AGENT_ID);

      expect(fetch).toHaveBeenCalledWith(
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}`,
        expect.objectContaining({
          method: "DELETE",
          headers: expect.objectContaining({ Authorization: "Bearer test-token" }),
        })
      );
      expect(logger.info).toHaveBeenCalledWith(
        "AgentRegistrationService: deregistered",
        expect.objectContaining({ agentId: AGENT_ID })
      );
    });

    it("skips fetch and logs warning when no accessToken", async () => {
      tokenStorage = makeTokenStorage(null);
      service = new AgentRegistrationService(() => PLATFORM_URL, tokenStorage, logger);

      await service.deregister(AGENT_ID);

      expect(fetch).not.toHaveBeenCalled();
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("no accessToken, skipping deregister")
      );
    });

    it("logs warning on non-2xx response and does not throw", async () => {
      vi.mocked(fetch).mockResolvedValueOnce(makeResponse(500, {}));

      await expect(service.deregister(AGENT_ID)).resolves.toBeUndefined();
      expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining("deregister failed 500"));
    });

    it("logs warning on network error and does not throw", async () => {
      vi.mocked(fetch).mockRejectedValueOnce(new Error("Network down"));

      await expect(service.deregister(AGENT_ID)).resolves.toBeUndefined();
      expect(logger.warn).toHaveBeenCalledWith(
        "AgentRegistrationService: deregister network error",
        expect.objectContaining({ error: "Network down" })
      );
    });
  });
});
