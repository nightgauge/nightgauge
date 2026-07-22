import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Capture event listeners for direct firing
let stageStartHandler: ((e: { stage: string; issueNumber: number }) => void) | null = null;
let stageErrorHandler: ((e: { issueNumber: number }) => void) | null = null;
let stateChangedHandler: ((state: unknown) => void) | null = null;

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

const { MattermostService, parseWebhookUrl } =
  await import("../../../src/services/notifications/MattermostService");

// ─── parseWebhookUrl ───────────────────────────────────────────────────────

describe("parseWebhookUrl", () => {
  it("parses an https Mattermost webhook URL", () => {
    const result = parseWebhookUrl("https://mattermost.example.com/hooks/abc123def456");
    expect(result).toEqual({
      baseUrl: "https://mattermost.example.com",
      token: "abc123def456",
    });
  });

  it("parses an http URL (self-hosted dev)", () => {
    const result = parseWebhookUrl("http://localhost:8065/hooks/xyz789");
    expect(result).toEqual({
      baseUrl: "http://localhost:8065",
      token: "xyz789",
    });
  });

  it("tolerates a trailing slash", () => {
    const result = parseWebhookUrl("https://mm.example.com/hooks/token1/");
    expect(result).toEqual({
      baseUrl: "https://mm.example.com",
      token: "token1",
    });
  });

  it("rejects Discord webhook URLs", () => {
    expect(parseWebhookUrl("https://discord.com/api/webhooks/123/abc-def")).toBeNull();
  });

  it("rejects malformed input", () => {
    expect(parseWebhookUrl("")).toBeNull();
    expect(parseWebhookUrl("not a url")).toBeNull();
    expect(parseWebhookUrl("https://mm.example.com/")).toBeNull();
    expect(parseWebhookUrl("https://mm.example.com/hooks/")).toBeNull();
  });
});

// ─── Factory helpers ───────────────────────────────────────────────────────

function makePipelineStateService() {
  return {
    onStageStart: vi.fn((cb: (e: { stage: string; issueNumber: number }) => void) => {
      stageStartHandler = cb;
      return { dispose: vi.fn() };
    }),
    onStageError: vi.fn((cb: (e: { issueNumber: number }) => void) => {
      stageErrorHandler = cb;
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

function makeConfigBridge(enabled = true) {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: {
        notifications: {
          mattermost: {
            enabled,
            webhook_env: "MATTERMOST_WEBHOOK_URL",
          },
        },
      },
    })),
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

const WEBHOOK_URL = "https://mm.example.com/hooks/abcdef123456";

function makeState(issueNumber: number, outcomeType?: string, extra: Record<string, unknown> = {}) {
  return {
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `fix/issue-${issueNumber}`,
    stages: { "issue-pickup": { status: "complete" } },
    tokens: { estimated_cost_usd: 0.05 },
    outcome_type: outcomeType,
    ...extra,
  };
}

// ─── buildAttachment (smoke + shape) ───────────────────────────────────────

describe("MattermostService.buildAttachment", () => {
  let service: InstanceType<typeof MattermostService>;
  let pss: ReturnType<typeof makePipelineStateService>;

  beforeEach(() => {
    pss = makePipelineStateService();
    service = new MattermostService(
      pss as never,
      makeConfigBridge() as never,
      makeLogger() as never
    );
  });

  afterEach(() => {
    service.dispose();
  });

  function makeRun(overrides: Record<string, unknown> = {}) {
    return {
      issueNumber: 42,
      issueTitle: "Test issue",
      branch: "fix/test",
      repoName: "my-repo",
      baseUrl: "https://mm.example.com",
      hookPath: "/hooks/abc",
      postId: "",
      startTime: Date.now(),
      costUsd: 0,
      stageStartTimes: new Map<string, number>(),
      isFinal: false,
      finalPatchRetries: 0,
      editMode: "edit" as const,
      fallbackWarned: false,
      ...overrides,
    };
  }

  it("emits a CSS hex color for the running state (blurple)", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42) as never);
    expect(att.color).toBe("#5865f2");
  });

  it("emits green hex for productive outcome", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "productive") as never);
    expect(att.color).toBe("#57f287");
  });

  it("emits red hex for failure outcomes", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "failure") as never);
    expect(att.color).toBe("#ed4245");
  });

  it("emits yellow hex for budget-ceiling outcome", () => {
    const att = service.buildAttachment(
      makeRun() as never,
      makeState(42, "budget-ceiling") as never
    );
    expect(att.color).toBe("#fee75c");
  });

  it("redacts secrets from error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "boom: GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234 leaked",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz1234");
    expect(errorField!.value).toContain("[REDACTED");
  });

  it("clamps long error fields to 4000 chars", () => {
    const longError = "x".repeat(8000);
    const state = makeState(42, "failure", {
      stages: { "feature-dev": { status: "failed", error: longError } },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value.length).toBeLessThanOrEqual(4000);
  });

  it("clamps the description text to 4000 chars", () => {
    // Long title forces a long description body
    const state = makeState(42, undefined, {
      title: "x".repeat(10_000),
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    expect(att.text).toBeDefined();
    expect(att.text!.length).toBeLessThanOrEqual(4000);
  });

  it("uses Slack-compatible attachment shape", () => {
    const att = service.buildAttachment(makeRun() as never, makeState(42, "productive") as never);
    expect(att).toHaveProperty("color");
    expect(att).toHaveProperty("title");
    expect(att).toHaveProperty("text");
    expect(att).toHaveProperty("fields");
    expect(att).toHaveProperty("footer");
    expect(att).toHaveProperty("ts");
    expect(Array.isArray(att.fields)).toBe(true);
  });
});

// ─── Lifecycle: POST then in-place edit ────────────────────────────────────

describe("MattermostService lifecycle", () => {
  let service: InstanceType<typeof MattermostService>;
  let pss: ReturnType<typeof makePipelineStateService>;
  let logger: ReturnType<typeof makeLogger>;
  let fetchMock: ReturnType<typeof vi.fn>;
  const originalEnv = process.env.MATTERMOST_WEBHOOK_URL;

  beforeEach(() => {
    vi.useFakeTimers();
    stageStartHandler = null;
    stageErrorHandler = null;
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
    if (originalEnv !== undefined) {
      process.env.MATTERMOST_WEBHOOK_URL = originalEnv;
    } else {
      delete process.env.MATTERMOST_WEBHOOK_URL;
    }
  });

  // jsonResponse with content-type: application/json
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

  function emptyOk(): Response {
    return {
      ok: true,
      status: 200,
      headers: { get: () => null },
      json: async () => null,
    } as unknown as Response;
  }

  async function simulateIssuePickup(
    issueNumber: number,
    initialResp: Response = jsonResponse({ id: `post-${issueNumber}` })
  ): Promise<void> {
    pss.getState.mockResolvedValue(makeState(issueNumber));
    fetchMock.mockResolvedValueOnce(initialResp);
    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber });
    await vi.advanceTimersByTimeAsync(0);
  }

  async function markFinal(issueNumber: number, outcomeType = "productive"): Promise<void> {
    stateChangedHandler!(makeState(issueNumber, outcomeType));
    await vi.advanceTimersByTimeAsync(0);
  }

  it("captures the post id from a json response and edits in place", async () => {
    await simulateIssuePickup(42);

    // Final PATCH (PUT to /api/v4/posts/post-42) succeeds
    fetchMock.mockResolvedValueOnce({ ok: true, status: 200 } as Response);
    await markFinal(42);

    // 2 calls: initial POST, then immediate final PUT
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const editCall = fetchMock.mock.calls[1] as unknown[];
    expect(editCall[0]).toBe("https://mm.example.com/api/v4/posts/post-42");
    const editInit = editCall[1] as { method: string; body: string };
    expect(editInit.method).toBe("PUT");
    const editBody = JSON.parse(editInit.body);
    expect(editBody.id).toBe("post-42");
    expect(Array.isArray(editBody.props.attachments)).toBe(true);
  });

  it("extracts post id from data.post.id when top-level id missing", async () => {
    await simulateIssuePickup(42, jsonResponse({ post: { id: "nested-post-7" } }));

    fetchMock.mockResolvedValueOnce({ ok: true, status: 200 } as Response);
    await markFinal(42);

    const editCall = fetchMock.mock.calls[1] as unknown[];
    expect(editCall[0]).toBe("https://mm.example.com/api/v4/posts/nested-post-7");
  });

  it("falls back to post-only mode when the response carries no id", async () => {
    await simulateIssuePickup(42, jsonResponse({ text: "ok" }));

    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("missing post id"),
      expect.any(Object)
    );

    // In post-only mode, intermediate state changes do NOT trigger PUT
    stateChangedHandler!(makeState(42)); // no outcome
    await vi.advanceTimersByTimeAsync(2000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // Terminal state posts a fresh attachment via POST
    fetchMock.mockResolvedValueOnce(emptyOk());
    await markFinal(42);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const finalCall = fetchMock.mock.calls[1] as unknown[];
    expect(finalCall[0]).toBe(`${"https://mm.example.com"}/hooks/abcdef123456`);
    const finalInit = finalCall[1] as { method: string };
    expect(finalInit.method).toBe("POST");
  });

  it("falls back to post-only mode on empty (non-json) response", async () => {
    await simulateIssuePickup(42, emptyOk());
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("missing post id"),
      expect.any(Object)
    );
  });

  it("downgrades to post-only on 401 from edit endpoint", async () => {
    await simulateIssuePickup(42);

    // First PUT — 401 — triggers fallback. Then a fresh POST in post-only mode succeeds.
    fetchMock.mockResolvedValueOnce({ ok: false, status: 401 } as Response);
    fetchMock.mockResolvedValueOnce(emptyOk());

    await markFinal(42);
    await vi.advanceTimersByTimeAsync(0);

    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("rejected webhook auth"),
      expect.any(Object)
    );
    expect(fetchMock).toHaveBeenCalledTimes(3); // POST + PUT(401) + POST(fallback)
    const fallbackCall = fetchMock.mock.calls[2] as unknown[];
    expect(fallbackCall[0]).toBe("https://mm.example.com/hooks/abcdef123456");
  });

  it("retries final PATCH after 3s on 5xx failure", async () => {
    await simulateIssuePickup(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await markFinal(42);

    expect(logger.warn).toHaveBeenCalledWith(
      "MattermostService: failed to patch post",
      expect.any(Object)
    );
    expect(logger.info).toHaveBeenCalledWith(
      "MattermostService: scheduling final patch retry",
      expect.objectContaining({ attempt: 1, delayMs: 3000 })
    );

    fetchMock.mockResolvedValueOnce({ ok: true, status: 200 } as Response);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("gives up after the final PATCH retry budget is exhausted", async () => {
    await simulateIssuePickup(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await markFinal(42);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await vi.advanceTimersByTimeAsync(3000);

    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);
    await vi.advanceTimersByTimeAsync(6000);

    expect(logger.error).toHaveBeenCalledWith(
      "MattermostService: final patch failed after all retries — post may be stuck",
      expect.objectContaining({ issueNumber: 42, retries: 2 })
    );
  });

  it("setEphemeral is a logged no-op against incoming webhooks", () => {
    service.setEphemeral(42, true);
    expect(logger.debug).toHaveBeenCalledWith(
      expect.stringContaining("ephemeral flag set"),
      expect.objectContaining({ issueNumber: 42, ephemeral: true })
    );
  });

  it("does nothing when notifications.mattermost.enabled is false", async () => {
    service.dispose();
    service = new MattermostService(
      pss as never,
      makeConfigBridge(false) as never,
      logger as never
    );
    pss.getState.mockResolvedValue(makeState(42));
    await service.initialize();
    stageStartHandler!({ stage: "issue-pickup", issueNumber: 42 });
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

// ─── Dispatcher integration smoke test ─────────────────────────────────────

describe("MattermostService dispatcher integration", () => {
  it("can be wrapped by NotificationDispatcher alongside DiscordService", async () => {
    const { NotificationDispatcher } =
      await import("../../../src/services/notifications/NotificationDispatcher");
    const pss = makePipelineStateService();
    const logger = makeLogger();
    const mm = new MattermostService(
      pss as never,
      makeConfigBridge(false) as never,
      logger as never
    );

    const dispatcher = new NotificationDispatcher([mm], logger as never);
    await dispatcher.initialize();
    dispatcher.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    dispatcher.onPipelineUpdate({ issueNumber: 42 });
    dispatcher.dispose();

    // No exceptions thrown — dispatcher invoked the lifecycle methods cleanly.
    expect(logger.error).not.toHaveBeenCalled();
  });
});
