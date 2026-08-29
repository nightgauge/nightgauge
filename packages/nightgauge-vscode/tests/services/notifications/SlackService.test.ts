/**
 * SlackService — live-updating pipeline status via the Slack Web API (#1071).
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
  SECRET_KEYS: { slackBotToken: "slackBotToken" },
}));

const { SlackService, isSlackBotToken, toSlackMrkdwn, SLACK_API_BASE } =
  await import("../../../src/services/notifications/SlackService");

/** Fake bot token. Assembled from parts so no literal looks like a credential. */
const TOKEN_TAIL = "zzTESTTOKENzz";
const BOT_TOKEN = "xoxb-" + TOKEN_TAIL;
const CHANNEL = "C0123456789";

function slackConfigBridge(
  overrides: { enabled?: boolean; channel?: string; bot_token_env?: string } = {}
) {
  // No bot_token_env unless a test deliberately supplies one: the key is gone
  // (#1107) and the CI fallback is the fixed SLACK_BOT_TOKEN. The old default
  // here was injected by the harness and never by production code, so the
  // documented setup — export the var, configure nothing — went untested.
  const slack: Record<string, unknown> = {
    enabled: overrides.enabled ?? true,
    channel: "channel" in overrides ? overrides.channel : CHANNEL,
  };
  if ("bot_token_env" in overrides) slack.bot_token_env = overrides.bot_token_env;
  return { getEffectiveConfig: vi.fn(() => ({ config: { notifications: { slack } } })) };
}

function makeStateService(state: unknown) {
  return {
    getState: vi.fn(async () => state),
    onStageStart: vi.fn(() => ({ dispose: vi.fn() })),
    onStageError: vi.fn(() => ({ dispose: vi.fn() })),
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
  };
}

/** A Slack API responder: 200 with a JSON body, the way Slack actually replies. */
function slackOk(body: Record<string, unknown> = { ok: true, ts: "1700000000.000100" }) {
  return vi.fn(async () => ({ ok: true, status: 200, json: async () => body }));
}

function newService(
  fetchMock: ReturnType<typeof vi.fn>,
  logger: ReturnType<typeof makeLogger>,
  bridge = slackConfigBridge(),
  state: unknown = makeState(42)
) {
  vi.stubGlobal("fetch", fetchMock);
  return new SlackService(makeStateService(state) as never, bridge as never, logger as never);
}

/** The method name from a recorded fetch call's URL. */
function methodOf(call: unknown[]): string {
  return String(call[0]).replace(`${SLACK_API_BASE}/`, "");
}

function bodyOf(call: unknown[]): Record<string, unknown> {
  return JSON.parse((call[1] as { body: string }).body);
}

/** The Authorization header from a recorded fetch call. */
function authOf(call: unknown[]): string {
  return (call[1] as { headers: Record<string, string> }).headers.Authorization;
}

describe("isSlackBotToken", () => {
  it("accepts a bot token", () => {
    expect(isSlackBotToken(BOT_TOKEN)).toBe(true);
  });

  // These are the realistic paste-mistakes, and each would surface as an
  // opaque invalid_auth at the first pipeline run if not caught here.
  it("rejects a user token, an app-level token, and a webhook URL", () => {
    expect(isSlackBotToken("xoxp-" + TOKEN_TAIL)).toBe(false);
    expect(isSlackBotToken("xapp-" + TOKEN_TAIL)).toBe(false);
    expect(isSlackBotToken("https://hooks.slack.com/services/T0/B0/x")).toBe(false);
  });

  it("rejects junk", () => {
    expect(isSlackBotToken("")).toBe(false);
    expect(isSlackBotToken("xoxb-")).toBe(false);
  });
});

describe("toSlackMrkdwn", () => {
  // The shared renderer emits Discord/Mattermost Markdown. Slack accepts the
  // payload either way and returns ok:true, so an untranslated message is a
  // SILENT rendering defect — visible only by looking at the channel.
  it("converts Markdown links to Slack link syntax", () => {
    expect(toSlackMrkdwn("[the issue](https://example.test/42)")).toBe(
      "<https://example.test/42|the issue>"
    );
  });

  it("converts double-asterisk bold to Slack's single asterisk", () => {
    expect(toSlackMrkdwn("**Feature Dev**")).toBe("*Feature Dev*");
  });

  it("keeps two bold runs on one line separate", () => {
    expect(toSlackMrkdwn("**a** and **b**")).toBe("*a* and *b*");
  });

  it("unwraps a bold label inside a link", () => {
    expect(toSlackMrkdwn("[**Title**](https://u.test)")).toBe("<https://u.test|*Title*>");
  });

  it("converts strikethrough", () => {
    expect(toSlackMrkdwn("~~gone~~")).toBe("~gone~");
  });

  // A ** or a bracket inside a command or a stack trace is literal text.
  it("leaves inline code spans untouched", () => {
    expect(toSlackMrkdwn("run `npm **x**` now")).toBe("run `npm **x**` now");
  });

  it("leaves fenced blocks untouched", () => {
    const fenced = "```\nkeep **this** [as](is)\n```";
    expect(toSlackMrkdwn(fenced)).toBe(fenced);
  });

  it("passes through text with no markup", () => {
    expect(toSlackMrkdwn("plain text")).toBe("plain text");
  });
});

describe("SlackService — delivery", () => {
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    storedSecret = BOT_TOKEN;
    logger = makeLogger();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    delete process.env.SLACK_BOT_TOKEN;
  });

  it("posts via chat.postMessage with the bot token when a run starts", async () => {
    const fetchMock = slackOk();
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());

    const call = fetchMock.mock.calls[0];
    expect(methodOf(call)).toBe("chat.postMessage");
    expect(authOf(call)).toBe(`Bearer ${BOT_TOKEN}`);
    const body = bodyOf(call);
    expect(body.channel).toBe(CHANNEL);
    expect((body.attachments as Array<{ title: string }>)[0].title).toContain("#42");

    // The posted attachment must carry Slack mrkdwn, not the renderer's
    // Markdown, and must declare mrkdwn_in or Slack renders it as plain text.
    const att = (body.attachments as Array<{ text: string; mrkdwn_in: string[] }>)[0];
    expect(att.mrkdwn_in).toEqual(["text", "fields"]);
    expect(att.text).not.toMatch(/\*\*/);
    expect(att.text).not.toMatch(/\]\(/);
    svc.dispose();
  });

  // The whole reason for a bot token: the terminal summary must EDIT the
  // original message, not append a second one.
  it("edits the original message in place at terminal state", async () => {
    const fetchMock = slackOk();
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    svc.onPipelineUpdate({ issueNumber: 42, state: makeState(42, "productive") });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

    const second = fetchMock.mock.calls[1];
    expect(methodOf(second)).toBe("chat.update");
    const body = bodyOf(second);
    expect(body.ts).toBe("1700000000.000100");
    expect(body.channel).toBe(CHANNEL);
    svc.dispose();
  });

  // Slack reports failures in a 200 body. Trusting res.ok would call this a
  // success and then try to edit a message that was never posted.
  it("treats {ok:false} in a 200 body as a failure", async () => {
    const fetchMock = slackOk({ ok: false, error: "channel_not_found" });
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalled());

    const warned = JSON.stringify(logger.warn.mock.calls);
    expect(warned).toContain("channel_not_found");

    // The run must not be retained: a later terminal event has nothing to edit.
    svc.onPipelineUpdate({ issueNumber: 42, state: makeState(42, "productive") });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    svc.dispose();
  });

  // A permanent error should carry the action that fixes it, not just a code.
  it("logs an actionable hint for a permanent Slack error", async () => {
    const fetchMock = slackOk({ ok: false, error: "missing_scope" });
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalled());

    const warned = JSON.stringify(logger.warn.mock.calls);
    expect(warned).toContain("chat:write");
    svc.dispose();
  });

  // No ts means nothing to edit; appending per stage would flood the channel.
  it("degrades to post-only when chat.postMessage returns no ts", async () => {
    const fetchMock = slackOk({ ok: true });
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    // An intermediate update must not post in post-only mode.
    svc.onPipelineUpdate({ issueNumber: 42, state: makeState(42) });
    await new Promise((r) => setTimeout(r, 50));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // The terminal state posts once, as a fresh message.
    svc.onPipelineUpdate({ issueNumber: 42, state: makeState(42, "productive") });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(methodOf(fetchMock.mock.calls[1])).toBe("chat.postMessage");
    svc.dispose();
  });

  it("does not post when disabled, when no token is configured, or with no channel", async () => {
    for (const [label, bridge, secret] of [
      ["disabled", slackConfigBridge({ enabled: false }), BOT_TOKEN],
      ["no channel", slackConfigBridge({ channel: undefined }), BOT_TOKEN],
      ["no token", slackConfigBridge(), undefined],
    ] as const) {
      storedSecret = secret;
      const fetchMock = slackOk();
      const svc = newService(fetchMock, logger, bridge);
      svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
      await new Promise((r) => setTimeout(r, 20));
      expect(fetchMock, label).not.toHaveBeenCalled();
      svc.dispose();
      vi.unstubAllGlobals();
    }
  });

  it("resolves SLACK_BOT_TOKEN with no notifications config beyond enabled+channel (#1107)", async () => {
    // The documented headless setup: export the variable, configure nothing.
    // Before #1107 the code guarded on `if (config.bot_token_env)` with no
    // default, so this resolved nothing — silently.
    storedSecret = undefined;
    process.env.SLACK_BOT_TOKEN = BOT_TOKEN;
    const fetchMock = slackOk();
    const svc = newService(fetchMock, logger, slackConfigBridge());
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(authOf(fetchMock.mock.calls[0])).toBe(`Bearer ${BOT_TOKEN}`);
    svc.dispose();
  });

  it("refuses a token pasted into bot_token_env and says to rotate it (#1106)", async () => {
    // The observed failure: a live token in the env-var-NAME field made the
    // lookup process.env["xoxb-…"] -> undefined, and nothing was ever logged.
    storedSecret = undefined;
    delete process.env.SLACK_BOT_TOKEN;
    const fetchMock = slackOk();
    const svc = newService(
      fetchMock,
      logger,
      slackConfigBridge({ bot_token_env: "xoxb-" + TOKEN_TAIL })
    );
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 20));

    expect(fetchMock).not.toHaveBeenCalled();
    const warned = logger.warn.mock.calls.map((c: unknown[]) => String(c[0])).join("\n");
    expect(warned).toContain("Rotate");
    expect(warned).not.toContain(TOKEN_TAIL);
    svc.dispose();
  });

  it("explains every inert reason instead of failing silently (#1106)", async () => {
    // Silence was the whole defect: Slack logged "subscribed to worktree slot"
    // and then said nothing at all for a 13-minute run.
    for (const [label, bridge, secret] of [
      ["no channel", slackConfigBridge({ channel: undefined }), BOT_TOKEN],
      ["no token", slackConfigBridge(), undefined],
    ] as const) {
      storedSecret = secret;
      delete process.env.SLACK_BOT_TOKEN;
      logger.warn.mockClear();
      const fetchMock = slackOk();
      const svc = newService(fetchMock, logger, bridge);
      svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
      await new Promise((r) => setTimeout(r, 20));

      expect(fetchMock, label).not.toHaveBeenCalled();
      expect(logger.warn, `${label} must be explained, not silent`).toHaveBeenCalled();
      svc.dispose();
      vi.unstubAllGlobals();
    }
  });

  it("falls back to the fixed env var when SecretStorage has nothing", async () => {
    storedSecret = undefined;
    process.env.SLACK_BOT_TOKEN = BOT_TOKEN;
    const fetchMock = slackOk();
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(authOf(fetchMock.mock.calls[0])).toBe(`Bearer ${BOT_TOKEN}`);
    svc.dispose();
  });

  it("refuses a credential that is not a bot token", async () => {
    storedSecret = "https://hooks.slack.com/services/T0/B0/x";
    const fetchMock = slackOk();
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalled();
    svc.dispose();
  });

  it("a transport failure is logged and never thrown into the pipeline", async () => {
    const fetchMock = vi.fn(async () => {
      throw new Error("ECONNREFUSED");
    });
    const svc = newService(fetchMock, logger);
    await expect(
      (async () => {
        svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
        await new Promise((r) => setTimeout(r, 1500));
      })()
    ).resolves.toBeUndefined();
    expect(logger.warn).toHaveBeenCalled();
    svc.dispose();
  }, 10000);

  // The bot token IS the credential — no log line may carry it.
  it("never logs the bot token", async () => {
    const fetchMock = vi.fn(async () => {
      throw new Error(`request failed with Bearer ${BOT_TOKEN}`);
    });
    const svc = newService(fetchMock, logger);
    svc.onPipelineStart({ issueNumber: 42, stage: "issue-pickup" });
    await new Promise((r) => setTimeout(r, 1500));

    const logged = JSON.stringify([
      ...logger.warn.mock.calls,
      ...logger.info.mock.calls,
      ...logger.error.mock.calls,
    ]);
    expect(logged).not.toContain(TOKEN_TAIL);
    svc.dispose();
  }, 10000);
});
