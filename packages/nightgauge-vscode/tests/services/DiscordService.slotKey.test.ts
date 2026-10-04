/**
 * Per-slot state is keyed by repo + issue number, not issue number alone (#2408).
 *
 * example-org/platform#21 and example-org/app#21 run concurrently in two slots.
 * Before the fix the second subscribeToSlot tore down the first slot's
 * subscription and both runs shared one embed.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { makeLogger, makeState } from "./notifications/_helpers";

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

vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: {
    getInstance: () => ({
      getSecret: async () => "https://discord.com/api/webhooks/1234567890/" + "zzTESTHOOKzz",
    }),
  },
  SECRET_KEYS: { slackBotToken: "slackBotToken", discordWebhookUrl: "discordWebhookUrl" },
}));

const { DiscordService } = await import("../../src/services/DiscordService");

const ISSUE = 21;

type Handler = (arg: never) => void;

function makeSlot(root: string) {
  const handlers: Record<string, Handler> = {};
  const disposers: Array<ReturnType<typeof vi.fn>> = [];
  const reg = (name: string) =>
    vi.fn((h: Handler) => {
      handlers[name] = h;
      const dispose = vi.fn();
      disposers.push(dispose);
      return { dispose };
    });
  return {
    handlers,
    disposers,
    svc: {
      getState: vi.fn(async () => makeState(ISSUE)),
      getRepoRoot: vi.fn(() => root),
      onStageStart: reg("stageStart"),
      onStageError: reg("stageError"),
      onStateChanged: reg("stateChanged"),
      onRunFinalized: reg("runFinalized"),
    },
  };
}

function bridge() {
  return {
    getEffectiveConfig: vi.fn(() => ({
      config: { notifications: { discord: { enabled: true } } },
    })),
  };
}

describe("DiscordService slot keying (#2408)", () => {
  let n: number;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    n = 0;
    fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: `msg-${++n}` }),
    }));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const posts = () =>
    (fetchMock.mock.calls as unknown[][]).filter(
      (c) => (c[1] as { method?: string }).method === "POST"
    );

  it("keeps same-numbered issues from two repos in separate subscriptions and embeds", async () => {
    const svc = new DiscordService({} as never, bridge() as never, makeLogger() as never);
    const platform = makeSlot("/tmp/platform");
    const app = makeSlot("/tmp/app");

    svc.subscribeToSlot(ISSUE, platform.svc as never, "example-org/platform");
    svc.subscribeToSlot(ISSUE, app.svc as never, "example-org/app");

    // The second subscribe must not dispose the first slot's subscriptions.
    expect(platform.disposers.length).toBe(4);
    for (const d of platform.disposers) expect(d).not.toHaveBeenCalled();

    platform.handlers.stageStart({ stage: "issue-pickup", issueNumber: ISSUE } as never);
    await vi.waitFor(() => expect(posts()).toHaveLength(1));
    app.handlers.stageStart({ stage: "issue-pickup", issueNumber: ISSUE } as never);
    await vi.waitFor(() => expect(posts()).toHaveLength(2));

    const links = posts().map((c) =>
      JSON.stringify(JSON.parse((c[1] as { body: string }).body).embeds[0])
    );
    expect(links[0]).toContain("example-org/platform/issues/21");
    expect(links[1]).toContain("example-org/app/issues/21");

    // Unsubscribing one repo's slot leaves the other's subscriptions alive.
    svc.unsubscribeFromSlot(ISSUE, "example-org/app");
    for (const d of app.disposers) expect(d).toHaveBeenCalledTimes(1);
    for (const d of platform.disposers) expect(d).not.toHaveBeenCalled();

    // Each run PATCHes its own message.
    platform.handlers.stateChanged(makeState(ISSUE, "productive") as never);
    await vi.waitFor(() => {
      const patches = (fetchMock.mock.calls as unknown[][]).filter(
        (c) => (c[1] as { method?: string }).method === "PATCH"
      );
      expect(patches).toHaveLength(1);
      expect(String(patches[0][0])).toContain("msg-1");
    });

    svc.dispose();
  });

  it("re-subscribing the same repo and issue replaces only that subscription", () => {
    const svc = new DiscordService({} as never, bridge() as never, makeLogger() as never);
    const first = makeSlot("/tmp/platform");
    const other = makeSlot("/tmp/app");
    svc.subscribeToSlot(ISSUE, first.svc as never, "example-org/platform");
    svc.subscribeToSlot(ISSUE, other.svc as never, "example-org/app");
    svc.subscribeToSlot(ISSUE, makeSlot("/tmp/platform").svc as never, "example-org/platform");
    for (const d of first.disposers) expect(d).toHaveBeenCalledTimes(1);
    for (const d of other.disposers) expect(d).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("still works for the single-repository case (no repo slug)", async () => {
    const svc = new DiscordService({} as never, bridge() as never, makeLogger() as never);
    const slot = makeSlot("/tmp/only");
    svc.subscribeToSlot(ISSUE, slot.svc as never);
    svc.subscribeToSlot(ISSUE, slot.svc as never);
    expect(slot.disposers.slice(0, 4).every((d) => d.mock.calls.length === 1)).toBe(true);

    slot.handlers.stageStart({ stage: "issue-pickup", issueNumber: ISSUE } as never);
    await vi.waitFor(() => expect(posts()).toHaveLength(1));
    svc.unsubscribeFromSlot(ISSUE);
    for (const d of slot.disposers.slice(4)) expect(d).toHaveBeenCalledTimes(1);
    svc.dispose();
  });
});
