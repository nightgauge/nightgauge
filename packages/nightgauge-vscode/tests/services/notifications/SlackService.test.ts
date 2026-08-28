/**
 * SlackService — pipeline status delivery to a Slack incoming webhook (#1071).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { makeLogger, makeState } from "./_helpers";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: { getConfiguration: vi.fn(() => ({ get: vi.fn() })) },
}));

let storedSecret: string | undefined;
vi.mock("../../../src/services/SecretStorageService", () => ({
  SecretStorageService: {
    getInstance: () => ({ getSecret: async () => storedSecret }),
  },
  SECRET_KEYS: { slackWebhookUrl: "slackWebhookUrl" },
}));

const { SlackService, isSlackWebhookUrl } =
  await import("../../../src/services/notifications/SlackService");

/** Fake webhook. The last path segment stands in for the credential. */
const HOOK_TOKEN = "zzzTESTTOKENzzz";
const WEBHOOK = `https://hooks.slack.com/services/T00000000/B00000000/${HOOK_TOKEN}`;

function slackConfigBridge(enabled = true, webhookEnv = "SLACK_WEBHOOK_URL") {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: { notifications: { slack: { enabled, webhook_env: webhookEnv } } },
    })),
  };
}

/** A PipelineStateService stub that serves one snapshot and records listeners. */
function makeStateService(state: unknown) {
  const listeners: {
    stageStart: Array<(e: { stage: string; issueNumber: number }) => void>;
    stateChanged: Array<(s: unknown) => void>;
  } = { stageStart: [], stateChanged: [] };
  return {
    listeners,
    getState: vi.fn(async () => state),
    onStageStart: vi.fn((cb: (e: { stage: string; issueNumber: number }) => void) => {
      listeners.stageStart.push(cb);
      return { dispose: vi.fn() };
    }),
    onStageError: vi.fn(() => ({ dispose: vi.fn() })),
    onStateChanged: vi.fn((cb: (s: unknown) => void) => {
      listeners.stateChanged.push(cb);
      return { dispose: vi.fn() };
    }),
  };
}

describe("isSlackWebhookUrl", () => {
  it("accepts a Slack incoming-webhook URL", () => {
    expect(isSlackWebhookUrl(WEBHOOK)).toBe(true);
  });

  it("rejects a look-alike host — a prefix check would accept this", () => {
    expect(isSlackWebhookUrl("https://hooks.slack.com.evil.test/services/T/B/X")).toBe(false);
  });

  it("rejects plaintext http", () => {
    expect(isSlackWebhookUrl("http://hooks.slack.com/services/T/B/X")).toBe(false);
  });

  it("rejects another provider's webhook pasted into the Slack field", () => {
    expect(isSlackWebhookUrl("https://discord.com/api/webhooks/123/abc")).toBe(false);
    expect(isSlackWebhookUrl("https://mm.example.com/hooks/abc")).toBe(false);
  });

  it("rejects junk", () => {
    expect(isSlackWebhookUrl("")).toBe(false);
    expect(isSlackWebhookUrl("not a url")).toBe(false);
  });
});

describe("SlackService — delivery", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    storedSecret = WEBHOOK;
    logger = makeLogger();
    fetchMock = vi.fn(async () => ({ ok: true, status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete process.env.SLACK_WEBHOOK_URL;
  });

  it("posts a Slack attachment when a run starts", async () => {
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe(WEBHOOK);
    expect(init.method).toBe("POST");
    const body = JSON.parse(init.body);
    expect(body.attachments).toHaveLength(1);
    expect(body.attachments[0].title).toContain("#42");
    svc.dispose();
  });

  it("posts the terminal summary exactly once, then forgets the run", async () => {
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const final = makeState(42, "productive");
    svc.onPipelineUpdate({ issueNumber: 42, state: final });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

    // A repeated terminal snapshot must not post again.
    svc.onPipelineUpdate({ issueNumber: 42, state: final });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    svc.dispose();
  });

  it("does not post when the notifier is disabled in config", async () => {
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge(false) as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("does not post when no webhook is configured anywhere", async () => {
    storedSecret = undefined;
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("falls back to the configured env var when SecretStorage has nothing", async () => {
    storedSecret = undefined;
    process.env.SLACK_WEBHOOK_URL = WEBHOOK;
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(fetchMock.mock.calls[0][0]).toBe(WEBHOOK);
    svc.dispose();
  });

  // Pasting a Discord webhook into the Slack field must not send pipeline
  // status to the wrong provider.
  it("refuses a configured URL that is not a Slack webhook", async () => {
    storedSecret = "https://discord.com/api/webhooks/123/abc";
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalled();
    svc.dispose();
  });

  it("a POST failure is logged and never thrown into the pipeline", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 500 });
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    await expect(
      (async () => {
        svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
        await new Promise((r) => setTimeout(r, 1500));
      })()
    ).resolves.toBeUndefined();
    expect(logger.warn).toHaveBeenCalled();
    svc.dispose();
  }, 10000);

  // The webhook URL IS the credential — no log line may carry it.
  it("never logs the webhook URL or its token on failure", async () => {
    fetchMock.mockRejectedValue(new Error(`connect ECONNREFUSED ${WEBHOOK}`));
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 1500));

    const logged = JSON.stringify([
      ...logger.warn.mock.calls,
      ...logger.info.mock.calls,
      ...logger.error.mock.calls,
    ]);
    expect(logged).not.toContain(HOOK_TOKEN);
    expect(logged).not.toContain(WEBHOOK);
    svc.dispose();
  }, 10000);

  // Intermediate stage transitions must not each become a channel message —
  // an incoming webhook cannot edit, so per-stage posts would flood.
  it("does not post on every stage transition", async () => {
    const stateService = makeStateService(makeState(42));
    const svc = new SlackService(
      stateService as never,
      slackConfigBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    for (const stage of ["feature-planning", "feature-dev", "feature-validate"]) {
      svc.onPipelineStart({ issueNumber: 42, stage });
    }
    await new Promise((r) => setTimeout(r, 30));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    svc.dispose();
  });
});
