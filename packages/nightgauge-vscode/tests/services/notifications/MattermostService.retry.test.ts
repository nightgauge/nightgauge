import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { vi } from "vitest";
import { makeConfigBridge, makeLogger, makeState } from "./_helpers";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

vi.mock("../../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: () => null },
  SECRET_KEYS: { mattermostWebhookUrl: "mattermostWebhookUrl" },
}));

const { MattermostService } = await import("../../../src/services/notifications/MattermostService");

const WEBHOOK_URL = "https://mm.example.com/hooks/abcdef123456";

// Per-test handler capture — each test that calls service.initialize() populates these.
let stageStartHandler: ((e: { stage: string; issueNumber: number }) => void) | null = null;
let stateChangedHandler: ((state: unknown) => void) | null = null;

function makePipelineStateService() {
  return {
    onStageStart: vi.fn((cb: (e: { stage: string; issueNumber: number }) => void) => {
      stageStartHandler = cb;
      return { dispose: vi.fn() };
    }),
    onStageError: vi.fn((_cb: (e: { issueNumber: number }) => void) => {
      return { dispose: vi.fn() };
    }),
    onStateChanged: vi.fn((cb: (state: unknown) => void) => {
      stateChangedHandler = cb;
      return { dispose: vi.fn() };
    }),
    getState: vi.fn().mockResolvedValue(null),
    getStatePath: vi.fn(() => "/repos/my-repo/.nightgauge/pipeline/state.json"),
  };
}

function jsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    headers: {
      get: (k: string) => (k.toLowerCase() === "content-type" ? "application/json" : null),
    },
    json: async () => body,
  } as unknown as Response;
}

// ─── In-flight POST retries (200ms / 800ms) ────────────────────────────────

describe("MattermostService.retry — in-flight POST retries (FETCH_RETRY_DELAYS)", () => {
  let service: InstanceType<typeof MattermostService>;
  let pss: ReturnType<typeof makePipelineStateService>;
  let logger: ReturnType<typeof makeLogger>;
  let fetchMock: ReturnType<typeof vi.fn>;
  const origEnv = process.env.MATTERMOST_WEBHOOK_URL;

  beforeEach(() => {
    vi.useFakeTimers();
    stageStartHandler = null;
    stateChangedHandler = null;
    pss = makePipelineStateService();
    logger = makeLogger();
    service = new MattermostService(pss as never, makeConfigBridge() as never, logger as never);
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    process.env.MATTERMOST_WEBHOOK_URL = WEBHOOK_URL;
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    if (origEnv !== undefined) {
      process.env.MATTERMOST_WEBHOOK_URL = origEnv;
    } else {
      delete process.env.MATTERMOST_WEBHOOK_URL;
    }
  });

  it("succeeds on first attempt — no retry, no delay", async () => {
    pss.getState.mockResolvedValue(makeState(42));
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: "post-42" }));

    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber: 42 });
    await vi.advanceTimersByTimeAsync(0);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(logger.info).not.toHaveBeenCalledWith(
      expect.stringContaining("retrying"),
      expect.any(Object)
    );
  });

  it("retries at 200ms after first non-ok response", async () => {
    pss.getState.mockResolvedValue(makeState(42));
    fetchMock
      .mockResolvedValueOnce({ ok: false, status: 500 } as Response)
      .mockResolvedValueOnce(jsonResponse({ id: "post-42" }));

    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber: 42 });
    await vi.advanceTimersByTimeAsync(0);

    // After 0ms advance: first attempt fired, retry pending
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(200);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(logger.info).toHaveBeenCalledWith(
      "MattermostService: fetch failed, retrying",
      expect.objectContaining({ attempt: 1, delayMs: 200 })
    );
  });

  it("retries at 800ms after second consecutive failure", async () => {
    pss.getState.mockResolvedValue(makeState(42));
    fetchMock
      .mockResolvedValueOnce({ ok: false, status: 503 } as Response)
      .mockResolvedValueOnce({ ok: false, status: 503 } as Response)
      .mockResolvedValueOnce(jsonResponse({ id: "post-42" }));

    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber: 42 });

    await vi.advanceTimersByTimeAsync(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(800);
    expect(fetchMock).toHaveBeenCalledTimes(3);

    expect(logger.info).toHaveBeenCalledWith(
      "MattermostService: fetch failed, retrying",
      expect.objectContaining({ attempt: 2, delayMs: 800 })
    );
  });

  it("logs error after exhausting all delays (3 total attempts fail)", async () => {
    pss.getState.mockResolvedValue(makeState(42));
    fetchMock.mockResolvedValue({ ok: false, status: 503 } as Response);

    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber: 42 });

    await vi.advanceTimersByTimeAsync(0 + 200 + 800);

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(logger.error).toHaveBeenCalledWith(
      "MattermostService: failed to create post after retries",
      expect.any(Object)
    );
  });
});

// ─── Final PATCH retries (3s / 6s) ────────────────────────────────────────

describe("MattermostService.retry — final PATCH retries (FINAL_PATCH_RETRY_DELAYS)", () => {
  let service: InstanceType<typeof MattermostService>;
  let pss: ReturnType<typeof makePipelineStateService>;
  let logger: ReturnType<typeof makeLogger>;
  let fetchMock: ReturnType<typeof vi.fn>;
  const origEnv = process.env.MATTERMOST_WEBHOOK_URL;

  async function setupWithPost(issueNumber: number): Promise<void> {
    pss.getState.mockResolvedValue(makeState(issueNumber));
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: `post-${issueNumber}` }));
    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber });
    await vi.advanceTimersByTimeAsync(0);
  }

  beforeEach(() => {
    vi.useFakeTimers();
    stageStartHandler = null;
    stateChangedHandler = null;
    pss = makePipelineStateService();
    logger = makeLogger();
    service = new MattermostService(pss as never, makeConfigBridge() as never, logger as never);
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    process.env.MATTERMOST_WEBHOOK_URL = WEBHOOK_URL;
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    if (origEnv !== undefined) {
      process.env.MATTERMOST_WEBHOOK_URL = origEnv;
    } else {
      delete process.env.MATTERMOST_WEBHOOK_URL;
    }
  });

  it("retries final PATCH at 3s after first 5xx failure", async () => {
    await setupWithPost(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    stateChangedHandler!(makeState(42, "productive"));
    await vi.advanceTimersByTimeAsync(0);

    expect(logger.info).toHaveBeenCalledWith(
      "MattermostService: scheduling final patch retry",
      expect.objectContaining({ attempt: 1, delayMs: 3000 })
    );

    fetchMock.mockResolvedValueOnce({ ok: true, status: 200 } as Response);
    await vi.advanceTimersByTimeAsync(3000);

    // Initial POST + failed PUT + retry PUT
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("retries final PATCH at 6s after second consecutive 5xx failure", async () => {
    await setupWithPost(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    stateChangedHandler!(makeState(42, "productive"));
    await vi.advanceTimersByTimeAsync(0);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await vi.advanceTimersByTimeAsync(3000);

    expect(logger.info).toHaveBeenCalledWith(
      "MattermostService: scheduling final patch retry",
      expect.objectContaining({ attempt: 2, delayMs: 6000 })
    );

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await vi.advanceTimersByTimeAsync(6000);

    expect(logger.error).toHaveBeenCalledWith(
      "MattermostService: final patch failed after all retries — post may be stuck",
      expect.objectContaining({ issueNumber: 42, retries: 2 })
    );
  });

  it("succeeds on first retry within the 3s budget", async () => {
    await setupWithPost(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    stateChangedHandler!(makeState(42, "productive"));
    await vi.advanceTimersByTimeAsync(0);

    fetchMock.mockResolvedValueOnce({ ok: true, status: 200 } as Response);
    await vi.advanceTimersByTimeAsync(3000);

    expect(logger.error).not.toHaveBeenCalled();
    // Verify the final successful PUT call went to the correct endpoint
    const calls = fetchMock.mock.calls as unknown[][];
    const retryCall = calls[2] as [string, RequestInit];
    expect(retryCall[0]).toBe("https://mm.example.com/api/v4/posts/post-42");
    expect(retryCall[1].method).toBe("PUT");
  });
});
