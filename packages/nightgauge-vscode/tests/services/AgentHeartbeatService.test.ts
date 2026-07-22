import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AgentHeartbeatService } from "../../src/services/AgentHeartbeatService";

vi.mock("vscode", () => ({}));

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

function makeResponse(status: number): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: vi.fn().mockResolvedValue({}),
  } as unknown as Response;
}

const PLATFORM_URL = "https://api.nightgauge.dev";
const AGENT_ID = "agent-heartbeat-001";

describe("AgentHeartbeatService", () => {
  let tokenStorage: ReturnType<typeof makeTokenStorage>;
  let logger: ReturnType<typeof makeLogger>;
  let service: AgentHeartbeatService;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn());
    tokenStorage = makeTokenStorage();
    logger = makeLogger();
    service = new AgentHeartbeatService(() => PLATFORM_URL, tokenStorage, logger);
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  describe("start()", () => {
    it("starts the interval timer on start(agentId)", async () => {
      vi.mocked(fetch).mockResolvedValue(makeResponse(200));

      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).toHaveBeenCalledWith(
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}/heartbeat`,
        expect.objectContaining({ method: "PUT" })
      );
    });

    it("is a no-op if already started (idempotent)", async () => {
      vi.mocked(fetch).mockResolvedValue(makeResponse(200));

      service.start(AGENT_ID);
      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).toHaveBeenCalledTimes(1);
    });

    it("is a no-op if agentId is empty string", async () => {
      service.start("");
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).not.toHaveBeenCalled();
    });
  });

  describe("dispose()", () => {
    it("clears the interval timer on dispose()", async () => {
      vi.mocked(fetch).mockResolvedValue(makeResponse(200));

      service.start(AGENT_ID);
      service.dispose();
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).not.toHaveBeenCalled();
    });

    it("is safe to call before start()", () => {
      expect(() => service.dispose()).not.toThrow();
    });
  });

  describe("heartbeat behavior", () => {
    it("calls PUT /v1/agents/{agentId}/heartbeat with Bearer token on interval tick", async () => {
      vi.mocked(fetch).mockResolvedValue(makeResponse(200));

      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).toHaveBeenCalledWith(
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}/heartbeat`,
        expect.objectContaining({
          method: "PUT",
          headers: expect.objectContaining({
            Authorization: "Bearer test-token",
            "Content-Type": "application/json",
          }),
        })
      );
    });

    it("resets consecutiveFailures to 0 on success after failures", async () => {
      // Fail twice (2 full heartbeat cycles, each requiring retry), then succeed
      vi.mocked(fetch)
        // First heartbeat attempt + retry → both fail
        .mockResolvedValueOnce(makeResponse(500))
        .mockResolvedValueOnce(makeResponse(500))
        // Second heartbeat attempt + retry → both fail
        .mockResolvedValueOnce(makeResponse(500))
        .mockResolvedValueOnce(makeResponse(500))
        // Third heartbeat → succeeds on first attempt
        .mockResolvedValue(makeResponse(200));

      service.start(AGENT_ID);

      // First interval tick → attempt + retry_delay(5s) + retry → both fail
      await vi.advanceTimersByTimeAsync(30_000);
      await vi.advanceTimersByTimeAsync(5_000);

      // Second tick
      await vi.advanceTimersByTimeAsync(30_000);
      await vi.advanceTimersByTimeAsync(5_000);

      // Third tick → succeeds, resets failures → no more warnings after this
      await vi.advanceTimersByTimeAsync(30_000);

      // Warn was called for the 2 failure cycles, but the service recovers
      expect(fetch).toHaveBeenCalledWith(
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}/heartbeat`,
        expect.objectContaining({ method: "PUT" })
      );
    });

    it("retries once after a failed heartbeat before recording failure", async () => {
      // First attempt fails, retry attempt also fails → 1 failure recorded
      vi.mocked(fetch)
        .mockResolvedValueOnce(makeResponse(500))
        .mockResolvedValueOnce(makeResponse(500));

      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);
      await vi.advanceTimersByTimeAsync(5_000);

      expect(fetch).toHaveBeenCalledTimes(2);
      // Only 1 consecutive failure recorded after 1 full failed cycle
      expect(logger.warn).not.toHaveBeenCalled();
    });

    it("logs warning after MAX_CONSECUTIVE_FAILURES (3) consecutive failures", async () => {
      // Each heartbeat cycle: attempt + retry → both fail
      vi.mocked(fetch).mockResolvedValue(makeResponse(500));

      service.start(AGENT_ID);

      // Drive 3 full failure cycles (each: tick + retry delay)
      for (let i = 0; i < 3; i++) {
        await vi.advanceTimersByTimeAsync(30_000);
        await vi.advanceTimersByTimeAsync(5_000);
      }

      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("consecutive heartbeat failures")
      );
    });

    it("does not send heartbeat if no accessToken", async () => {
      tokenStorage = makeTokenStorage(null);
      service = new AgentHeartbeatService(() => PLATFORM_URL, tokenStorage, logger);

      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);

      expect(fetch).not.toHaveBeenCalled();
    });
  });

  // 401 recovery delegates to the centralized IOnDemandTokenRefresher
  // (TokenRefreshManager.forceRefresh) so the single-use refresh token is spent
  // once across all refresh paths (#3751).
  describe("401 refresh-and-retry behavior", () => {
    const makeTokenRefresher = (token: string | null = "new-token") => ({
      forceRefresh: vi.fn().mockResolvedValue(token),
    });

    it("refreshes token and retries on 401 — succeeds on retry", async () => {
      const tokenRefresher = makeTokenRefresher("new-token");
      tokenStorage = makeTokenStorage("old-token");
      service = new AgentHeartbeatService(() => PLATFORM_URL, tokenStorage, logger, tokenRefresher);

      vi.mocked(fetch)
        .mockResolvedValueOnce(makeResponse(401)) // first attempt → 401
        .mockResolvedValueOnce(makeResponse(200)); // retry with new token → 200

      service.start(AGENT_ID);
      await vi.advanceTimersByTimeAsync(30_000);

      expect(tokenRefresher.forceRefresh).toHaveBeenCalledTimes(1);
      expect(fetch).toHaveBeenCalledTimes(2);
      expect(fetch).toHaveBeenNthCalledWith(
        1,
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}/heartbeat`,
        expect.objectContaining({
          headers: expect.objectContaining({ Authorization: "Bearer old-token" }),
        })
      );
      expect(fetch).toHaveBeenNthCalledWith(
        2,
        `${PLATFORM_URL}/v1/agents/${AGENT_ID}/heartbeat`,
        expect.objectContaining({
          headers: expect.objectContaining({ Authorization: "Bearer new-token" }),
        })
      );
      // Success → no consecutive failure recorded, no warn
      expect(logger.warn).not.toHaveBeenCalled();
    });

    it("counts failure when refresh yields no token on 401", async () => {
      const tokenRefresher = makeTokenRefresher(null);
      tokenStorage = makeTokenStorage("old-token");
      service = new AgentHeartbeatService(() => PLATFORM_URL, tokenStorage, logger, tokenRefresher);

      // Every attempt returns 401 and refresh yields no usable token.
      vi.mocked(fetch).mockResolvedValue(makeResponse(401));

      service.start(AGENT_ID);
      // Drive 3 full cycles so warn threshold is reached
      for (let i = 0; i < 3; i++) {
        await vi.advanceTimersByTimeAsync(30_000);
        await vi.advanceTimersByTimeAsync(5_000);
      }

      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("consecutive heartbeat failures")
      );
    });

    it("counts failure when no tokenRefresher provided on 401", async () => {
      // service without tokenRefresher (default constructor from beforeEach)
      vi.mocked(fetch).mockResolvedValue(makeResponse(401));

      service.start(AGENT_ID);
      for (let i = 0; i < 3; i++) {
        await vi.advanceTimersByTimeAsync(30_000);
        await vi.advanceTimersByTimeAsync(5_000);
      }

      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("consecutive heartbeat failures")
      );
      // No refresh attempted
      expect(tokenStorage.store).not.toHaveBeenCalled();
    });
  });
});
