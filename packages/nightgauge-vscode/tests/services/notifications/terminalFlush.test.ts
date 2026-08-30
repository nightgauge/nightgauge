/**
 * Terminal flush — the notifiers must not race a late-written field (#1127).
 *
 * `health_score` is written by the orchestrator's post-run health evaluation
 * AFTER `outcome_type` is already on the state. Every notifier used to render
 * its terminal card on the outcome write and then release the run, so whichever
 * one's last write landed first kept a card without Pipeline Health —
 * permanently, because nothing edits the card again. The two cards for one run
 * therefore disagreed, and which one won was timing.
 *
 * These tests drive a watched run through exactly that ordering and assert
 * BOTH renderers end up carrying the field.
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
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
  },
  workspace: { getConfiguration: vi.fn(() => ({ get: vi.fn() })) },
  env: { openExternal: vi.fn() },
  Uri: { parse: vi.fn() },
}));

let storedSecret: string | undefined;
vi.mock("../../../src/services/SecretStorageService", () => ({
  SecretStorageService: {
    getInstance: () => ({ getSecret: async () => storedSecret }),
  },
  SECRET_KEYS: {
    slackBotToken: "slackBotToken",
    discordWebhookUrl: "discordWebhookUrl",
  },
}));

const { SlackService, SLACK_LIMITS, SLACK_API_BASE } =
  await import("../../../src/services/notifications/SlackService");
const { MATTERMOST_LIMITS } = await import("../../../src/services/notifications/MattermostService");
const { DiscordService, MAX_FIELDS } = await import("../../../src/services/DiscordService");

const ISSUE = 42;
const CHANNEL = "C0123456789";
/** Fake credentials, assembled from parts so no literal looks real. */
const BOT_TOKEN = "xoxb-" + "zzTESTTOKENzz";
const WEBHOOK_URL = "https://discord.com/api/webhooks/1234567890/" + "zzTESTHOOKzz";

/**
 * The run's terminal state. `health_score` is deliberately absent — at the
 * instant `outcome_type` is written, the health evaluation has not run yet.
 */
function terminalState() {
  return makeState(ISSUE, "productive", {
    stages: { "issue-pickup": { status: "complete" }, "pr-merge": { status: "complete" } },
    pipeline_meta: { complexity: "moderate" },
  });
}

/** The same run once the post-run health evaluation has written its score. */
function finalizedState() {
  return makeState(ISSUE, "productive", {
    stages: { "issue-pickup": { status: "complete" }, "pr-merge": { status: "complete" } },
    pipeline_meta: { complexity: "moderate", health_score: 89 },
  });
}

function makeStateService(state: unknown) {
  return {
    getState: vi.fn(async () => state),
    getRepoRoot: vi.fn(() => "/tmp/a-watched-repo"),
    onStageStart: vi.fn(() => ({ dispose: vi.fn() })),
    onStageError: vi.fn(() => ({ dispose: vi.fn() })),
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
    onRunFinalized: vi.fn(() => ({ dispose: vi.fn() })),
  };
}

// ─── Slack harness ──────────────────────────────────────────────────────────

function slackBridge() {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: { notifications: { slack: { enabled: true, channel: CHANNEL } } },
    })),
  };
}

function slackFetch() {
  return vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ ok: true, ts: "1700000000.000100" }),
  }));
}

// ─── Discord harness ────────────────────────────────────────────────────────

function discordBridge() {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: { notifications: { discord: { enabled: true } } },
    })),
  };
}

function discordFetch() {
  return vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ id: "9999" }),
  }));
}

/** Parse a recorded fetch call's JSON body. */
function bodyOf(call: unknown[]): Record<string, unknown> {
  return JSON.parse((call[1] as { body: string }).body);
}

/** Every field label carried by the last Discord PATCH. */
function discordFieldNames(fetchMock: ReturnType<typeof vi.fn>): string[] {
  const calls = fetchMock.mock.calls as unknown[][];
  const patches = calls.filter((c) => (c[1] as { method?: string })?.method === "PATCH");
  const last = patches[patches.length - 1];
  const embed = (bodyOf(last).embeds as Array<{ fields?: Array<{ name: string }> }>)[0];
  return (embed.fields ?? []).map((f) => f.name);
}

/** Every field label carried by the last Slack chat.update. */
function slackFieldTitles(fetchMock: ReturnType<typeof vi.fn>): string[] {
  const calls = fetchMock.mock.calls as unknown[][];
  const updates = calls.filter((c) => String(c[0]).endsWith("chat.update"));
  const last = updates[updates.length - 1];
  const att = (bodyOf(last).attachments as Array<{ fields?: Array<{ title: string }> }>)[0];
  return (att.fields ?? []).map((f) => f.title);
}

/** Drop a leading emoji so Discord's "🏥 Pipeline Health" compares to Slack's. */
function bare(label: string): string {
  return label.replace(/^[^\p{L}\p{N}]+/u, "").trim();
}

describe("terminal flush — a late-written field reaches both cards (#1127)", () => {
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    logger = makeLogger();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("Discord renders Pipeline Health written after the terminal state", async () => {
    storedSecret = WEBHOOK_URL;
    const fetchMock = discordFetch();
    vi.stubGlobal("fetch", fetchMock);
    const svc = new DiscordService(
      makeStateService(makeState(ISSUE)) as never,
      discordBridge() as never,
      logger as never
    );

    svc.onPipelineStart({ issueNumber: ISSUE, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    // The outcome write: terminal, but health has not been evaluated yet.
    svc.onPipelineUpdate({ issueNumber: ISSUE, state: terminalState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(discordFieldNames(fetchMock).map(bare)).not.toContain("Pipeline Health");

    // The health score lands, and the run is finalized.
    svc.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(discordFieldNames(fetchMock).map(bare)).toContain("Pipeline Health");

    svc.dispose();
  });

  it("Slack renders Pipeline Health written after the terminal state", async () => {
    storedSecret = BOT_TOKEN;
    const fetchMock = slackFetch();
    vi.stubGlobal("fetch", fetchMock);
    const svc = new SlackService(
      makeStateService(makeState(ISSUE)) as never,
      slackBridge() as never,
      logger as never
    );

    svc.onPipelineStart({ issueNumber: ISSUE, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    svc.onPipelineUpdate({ issueNumber: ISSUE, state: terminalState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(slackFieldTitles(fetchMock)).not.toContain("Pipeline Health");

    svc.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(slackFieldTitles(fetchMock)).toContain("Pipeline Health");

    const flushCall = (fetchMock.mock.calls as unknown[][])[2];
    expect(String(flushCall[0])).toBe(`${SLACK_API_BASE}/chat.update`);
    svc.dispose();
  });

  // The report that opened #1127: one run, two cards, one of them missing a
  // field. Both notifiers are driven through the identical event sequence and
  // their final field sets must agree.
  it("both cards carry the same field set for the same completed run", async () => {
    storedSecret = WEBHOOK_URL;
    const discordCalls = discordFetch();
    vi.stubGlobal("fetch", discordCalls);
    const discord = new DiscordService(
      makeStateService(makeState(ISSUE)) as never,
      discordBridge() as never,
      logger as never
    );
    discord.onPipelineStart({ issueNumber: ISSUE, stage: "issue-pickup" });
    await vi.waitFor(() => expect(discordCalls).toHaveBeenCalledTimes(1));
    discord.onPipelineUpdate({ issueNumber: ISSUE, state: terminalState() });
    await vi.waitFor(() => expect(discordCalls).toHaveBeenCalledTimes(2));
    discord.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await vi.waitFor(() => expect(discordCalls).toHaveBeenCalledTimes(3));
    const discordFields = discordFieldNames(discordCalls).map(bare);
    discord.dispose();
    vi.unstubAllGlobals();

    storedSecret = BOT_TOKEN;
    const slackCalls = slackFetch();
    vi.stubGlobal("fetch", slackCalls);
    const slack = new SlackService(
      makeStateService(makeState(ISSUE)) as never,
      slackBridge() as never,
      logger as never
    );
    slack.onPipelineStart({ issueNumber: ISSUE, stage: "issue-pickup" });
    await vi.waitFor(() => expect(slackCalls).toHaveBeenCalledTimes(1));
    slack.onPipelineUpdate({ issueNumber: ISSUE, state: terminalState() });
    await vi.waitFor(() => expect(slackCalls).toHaveBeenCalledTimes(2));
    slack.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await vi.waitFor(() => expect(slackCalls).toHaveBeenCalledTimes(3));
    const slackFields = slackFieldTitles(slackCalls).map(bare);
    slack.dispose();

    expect(slackFields).toEqual(discordFields);
  });

  // A second finalize must not re-edit the card: the flush is a one-shot.
  it("is idempotent — a repeated finalize does not re-edit", async () => {
    storedSecret = BOT_TOKEN;
    const fetchMock = slackFetch();
    vi.stubGlobal("fetch", fetchMock);
    const svc = new SlackService(
      makeStateService(makeState(ISSUE)) as never,
      slackBridge() as never,
      logger as never
    );
    svc.onPipelineStart({ issueNumber: ISSUE, stage: "issue-pickup" });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    svc.onPipelineUpdate({ issueNumber: ISSUE, state: terminalState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    svc.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));

    svc.onPipelineFinal({ issueNumber: ISSUE, state: finalizedState() });
    await new Promise((r) => setTimeout(r, 30));
    expect(fetchMock).toHaveBeenCalledTimes(3);
    svc.dispose();
  });
});

describe("field caps agree across providers (#1127)", () => {
  // Discord documents "fields — Up to 25 field objects"; Slack and Mattermost
  // attachments document no field-count limit at all. A lower invented cap on
  // one provider silently drops a field the others keep — the same divergence
  // by a different mechanism.
  it("every provider renders to the one documented cap", () => {
    expect(SLACK_LIMITS.maxFields).toBe(MAX_FIELDS);
    expect(MATTERMOST_LIMITS.maxFields).toBe(MAX_FIELDS);
  });
});
