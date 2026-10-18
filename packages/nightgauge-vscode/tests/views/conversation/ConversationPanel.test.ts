/**
 * The chat panel on the conversation contract (#1571).
 *
 * arrival:src/views/conversation/ConversationPanel.ts — the panel's real
 * transport is the ConversationApi and the stream opener; these tests stub
 * both and assert the panel reaches a populated state from them.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  ConversationApi,
  ConversationFailure,
  ConversationResult,
} from "../../../src/services/conversation/ConversationClient";
import type {
  ConversationStreamHandlers,
  OpenConversationStream,
} from "../../../src/services/conversation/ConversationStream";
import type {
  Conversation,
  ConversationFrame,
  ConversationPicker,
  ConversationThread,
  WireExecutionProfile,
} from "../../../src/services/conversation/conversationWire";
import type { ChatViewModel } from "../../../src/views/conversation/conversationViewModel";

let posted: Record<string, unknown>[] = [];
let messageHandler: ((msg: unknown) => Promise<void> | void) | null = null;
const showQuickPick = vi.fn();
const showInformationMessage = vi.fn();
const executeCommand = vi.fn();

vi.mock("vscode", () => ({
  ViewColumn: { One: 1, Beside: -2 },
  window: {
    createWebviewPanel: vi.fn(() => ({
      webview: {
        html: "",
        cspSource: "vscode-resource:",
        onDidReceiveMessage: (handler: (msg: unknown) => void) => {
          messageHandler = handler;
          return { dispose: () => undefined };
        },
        postMessage: (msg: Record<string, unknown>) => {
          posted.push(msg);
          return Promise.resolve(true);
        },
      },
      reveal: vi.fn(),
      onDidDispose: () => ({ dispose: () => undefined }),
      dispose: vi.fn(),
    })),
    showQuickPick: (...args: unknown[]) => showQuickPick(...args),
    showInformationMessage: (...args: unknown[]) => showInformationMessage(...args),
    showWarningMessage: vi.fn(),
  },
  commands: { executeCommand: (...args: unknown[]) => executeCommand(...args) },
}));

vi.mock("../../../src/services/IpcClient", () => ({ IpcClient: { getInstance: vi.fn() } }));

const { ConversationPanel, parseChatMessage, plainLabel } =
  await import("../../../src/views/conversation/ConversationPanel");

const TEAM_A = "team-a";
const TEAM_B = "team-b";
const WS_PLATFORM = "ws-platform";
const WS_MOBILE = "ws-mobile";
const CONV = "conv-1";
const ALICE = "acct-alice";
const BOB = "acct-bob";
const OPERATOR = "acct-operator";

function ok<T>(value: T): ConversationResult<T> {
  return { ok: true, value };
}
function refused(
  kind: ConversationFailure["kind"],
  status: number,
  code: string,
  extra: Partial<ConversationFailure> = {}
): ConversationFailure {
  return { ok: false, kind, status, code, message: `${code} message`, ...extra };
}

function conversation(over: Partial<Conversation> = {}): Conversation {
  return {
    id: CONV,
    team_id: TEAM_B,
    created_by: ALICE,
    title: "Release prep",
    visibility: "team",
    last_activity_at: "2026-10-17T10:00:00Z",
    ...over,
  };
}

const CODEX_FRONTIER: WireExecutionProfile = {
  adapter: "codex",
  performance_mode: "frontier",
  effort: "high",
};
const CLAUDE_ELEVATED: WireExecutionProfile = {
  adapter: "claude-headless",
  performance_mode: "elevated",
  effort: "medium",
};

function picker(profile: WireExecutionProfile | null, over: Partial<ConversationPicker> = {}) {
  return {
    workspaces: [
      {
        id: WS_PLATFORM,
        slug: "platform",
        display_name: "Platform",
        eligible: profile !== null,
        reason: profile ? null : "offline",
        host_operator_account_id: OPERATOR,
        execution_profile: profile,
      },
      {
        id: WS_MOBILE,
        slug: "mobile",
        display_name: "Mobile",
        eligible: true,
        reason: null,
        host_operator_account_id: BOB,
        execution_profile: CLAUDE_ELEVATED,
      },
    ],
    previous_target_workspace_id: WS_PLATFORM,
    resolved_target_workspace_id: WS_PLATFORM,
    ...over,
  } satisfies ConversationPicker;
}

function emptyThread(over: Partial<ConversationThread> = {}): ConversationThread {
  return { conversation: conversation(), messages: [], turns: [], ...over };
}

type Api = { [K in keyof ConversationApi]: ReturnType<typeof vi.fn> };

function makeApi(role: "owner" | "admin" | "developer" | "viewer" = "developer"): Api {
  return {
    listTeams: vi.fn(async () =>
      ok([
        { id: TEAM_A, name: "Personal", role: "owner", is_default: true },
        { id: TEAM_B, name: "Acme", role, is_default: false },
      ])
    ),
    listWorkspaces: vi.fn(async (teamId: string) =>
      ok(
        teamId === TEAM_B
          ? [
              { id: WS_PLATFORM, slug: "platform", display_name: "Platform" },
              { id: WS_MOBILE, slug: "mobile", display_name: "Mobile" },
            ]
          : [{ id: "ws-personal", slug: "default", display_name: "Default" }]
      )
    ),
    listMembers: vi.fn(async () =>
      ok([
        { account_id: ALICE, login: "alice" },
        { account_id: BOB, login: "bob" },
        { account_id: OPERATOR, login: "olivia" },
      ])
    ),
    listConversations: vi.fn(async () =>
      ok([conversation(), conversation({ id: "conv-2", created_by: BOB, title: "Other" })])
    ),
    createConversation: vi.fn(),
    getConversation: vi.fn(async () => ok(emptyThread())),
    postMessage: vi.fn(),
    cancelTurn: vi.fn(async () => ok({ ended: false })),
    picker: vi.fn(async () => ok(picker(CODEX_FRONTIER))),
    getAttentionCard: vi.fn(),
    resolveAttentionCard: vi.fn(),
  };
}

let streamHandlers: ConversationStreamHandlers | null;
const openStream: OpenConversationStream = vi.fn(async (_id, handlers) => {
  streamHandlers = handlers;
  return { close: vi.fn() };
});

async function flush(): Promise<void> {
  for (let i = 0; i < 10; i++) await Promise.resolve();
  await new Promise((r) => setImmediate(r));
}

function lastState(): ChatViewModel {
  const states = posted.filter((m) => m.type === "state");
  return states[states.length - 1]!.model as ChatViewModel;
}

async function openPanel(api: Api, pollMs = 15_000) {
  const panel = ConversationPanel.show({
    api: api as unknown as ConversationApi,
    openStream,
    signedIn: async () => true,
    getWorkspaceSlug: () => "platform",
    pickerPollMs: pollMs,
  });
  await flush();
  return panel;
}

async function openThread(api: Api) {
  const panel = await openPanel(api);
  await messageHandler!({ type: "selectConversation", conversationId: CONV });
  await flush();
  return panel;
}

function emit(frame: ConversationFrame): void {
  streamHandlers!.onFrame(frame);
}

let frameId = 100;
function msgFrame(
  id: string,
  turnId: string,
  position: number,
  actor: string | null,
  content: string,
  role: "user" | "assistant" = "user",
  target: string | null = WS_PLATFORM
): ConversationFrame {
  return {
    id: String(++frameId),
    type: "message",
    payload: {
      message_id: id,
      turn_id: turnId,
      position,
      role,
      actor_account_id: actor,
      content,
      redacted: false,
      target_workspace_id: target,
      client_message_id: null,
    },
  };
}
function stateFrame(
  turnId: string,
  state: string,
  over: Partial<Extract<ConversationFrame, { type: "turn.state" }>["payload"]> = {}
): ConversationFrame {
  return {
    id: String(++frameId),
    type: "turn.state",
    payload: {
      turn_id: turnId,
      state,
      failure_reason: null,
      failure_detail: null,
      target_workspace_id: WS_PLATFORM,
      operator_account_id: OPERATOR,
      execution_profile: null,
      ...over,
    },
  };
}
function deltaFrame(turnId: string, seq: number, text: string): ConversationFrame {
  return { id: String(++frameId), type: "turn.delta", payload: { turn_id: turnId, seq, text } };
}

beforeEach(() => {
  posted = [];
  messageHandler = null;
  streamHandlers = null;
  showQuickPick.mockReset();
  showInformationMessage.mockReset();
  executeCommand.mockReset();
});

afterEach(() => {
  ConversationPanel.current?.dispose();
  vi.useRealTimers();
});

describe("ConversationPanel: arrival and the team's threads", () => {
  it("lists the team's conversations for the team that owns this window's workspace", async () => {
    const api = makeApi();
    await openPanel(api);
    // The window's workspace (slug "platform") is on the second team, not the default.
    expect(api.listConversations).toHaveBeenCalledWith(TEAM_B);
    const model = lastState();
    expect(model.team).toEqual({ name: "Acme", role: "developer" });
    expect(model.canSwitchTeam).toBe(true);
    // Never filtered by creator: Bob's thread is listed for this caller too.
    expect(model.conversations.map((c) => c.id)).toEqual([CONV, "conv-2"]);
  });

  it("lets a developer who did not create a thread open it and post", async () => {
    const api = makeApi("developer");
    api.getConversation.mockResolvedValue(
      ok(emptyThread({ conversation: conversation({ created_by: BOB }) }))
    );
    await openThread(api);
    expect(lastState().canPost).toBe(true);
    expect(lastState().active?.id).toBe(CONV);
  });

  it("shows a signed-out state without calling the service", async () => {
    const api = makeApi();
    ConversationPanel.show({
      api: api as unknown as ConversationApi,
      openStream,
      signedIn: async () => false,
      getWorkspaceSlug: () => null,
    });
    await flush();
    expect(api.listTeams).not.toHaveBeenCalled();
    expect(lastState().status?.kind).toBe("signed_out");
  });
});

describe("ConversationPanel: streaming", () => {
  it("renders at least two chunks incrementally before the terminal state", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "what is running?"));
    emit(stateFrame("t1", "running"));
    await flush();
    const before = posted.length;
    emit(deltaFrame("t1", 1, "Two runs "));
    emit(deltaFrame("t1", 2, "are in progress."));
    emit(stateFrame("t1", "completed", { execution_profile: CODEX_FRONTIER }));
    await flush();
    const after = posted.slice(before);
    const deltas = after.filter((m) => m.type === "delta");
    expect(deltas.map((d) => d.text)).toEqual(["Two runs ", "are in progress."]);
    const terminalIndex = after.findIndex(
      (m) =>
        m.type === "state" &&
        (m.model as ChatViewModel).active?.entries.some(
          (e) => e.kind === "reply" && e.state === "completed"
        )
    );
    // Both chunks were posted on their own, before the terminal render.
    expect(after.indexOf(deltas[1]!)).toBeLessThan(terminalIndex);
    const reply = lastState().active!.entries.find((e) => e.kind === "reply")!;
    expect(reply.kind === "reply" && reply.text).toBe("Two runs are in progress.");
    expect(reply.kind === "reply" && reply.inFlight).toBe(false);
  });

  it("cancels the running turn and appends no chunk after the terminal frame", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "long answer please"));
    emit(stateFrame("t1", "running"));
    emit(deltaFrame("t1", 1, "first "));
    await flush();
    expect(lastState().active?.inFlightTurnId).toBe("t1");
    await messageHandler!({ type: "cancel", turnId: "t1" });
    expect(api.cancelTurn).toHaveBeenCalledWith(CONV, "t1");
    emit(stateFrame("t1", "cancelled"));
    await flush();
    const before = posted.length;
    emit(deltaFrame("t1", 2, "late chunk"));
    await flush();
    expect(posted.slice(before)).toEqual([]);
    const reply = lastState().active!.entries.find((e) => e.kind === "reply")!;
    expect(reply.kind === "reply" && reply.state).toBe("cancelled");
    expect(reply.kind === "reply" && reply.text).toBe("first ");
    expect(lastState().active?.inFlightTurnId).toBeNull();
  });

  it("refuses a cancel for a turn that is not in flight in the open thread", async () => {
    const api = makeApi();
    await openThread(api);
    await messageHandler!({ type: "cancel", turnId: "t-unknown" });
    expect(api.cancelTurn).not.toHaveBeenCalled();
  });

  it("purges the thread when the stream answers 404", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "secret plans"));
    await flush();
    streamHandlers!.onNotFound();
    await flush();
    const model = lastState();
    expect(model.active).toBeNull();
    expect(model.conversations.map((c) => c.id)).not.toContain(CONV);
    expect(JSON.stringify(model)).not.toContain("secret plans");
  });

  it("refetches the thread when the stream says its replay window passed", async () => {
    const api = makeApi();
    await openThread(api);
    expect(api.getConversation).toHaveBeenCalledTimes(1);
    emit({ id: "900", type: "stream.reset" });
    await flush();
    expect(api.getConversation).toHaveBeenCalledTimes(2);
  });

  it("drops redacted text from the transcript", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "my token is hunter2"));
    await flush();
    emit({
      id: String(++frameId),
      type: "message.redacted",
      payload: { message_ids: ["m1"], delta_turn_ids: [] },
    });
    await flush();
    expect(JSON.stringify(lastState())).not.toContain("hunter2");
  });
});

describe("ConversationPanel: the platform's record", () => {
  it("keeps the platform's conversation id and message order, with no duplicates after replay", async () => {
    const api = makeApi();
    const thread = emptyThread({
      // The thread read's array order is not the order: position is.
      messages: [
        {
          id: "m3",
          conversation_id: CONV,
          turn_id: "t2",
          position: 3,
          role: "user",
          content: "second question",
          redacted: false,
          actor_account_id: BOB,
          client_message_id: null,
          target_workspace_id: WS_MOBILE,
        },
        {
          id: "m1",
          conversation_id: CONV,
          turn_id: "t1",
          position: 1,
          role: "user",
          content: "first question",
          redacted: false,
          actor_account_id: ALICE,
          client_message_id: null,
          target_workspace_id: WS_PLATFORM,
        },
        {
          id: "m2",
          conversation_id: CONV,
          turn_id: "t1",
          position: 2,
          role: "assistant",
          content: "first answer",
          redacted: false,
          actor_account_id: null,
          client_message_id: null,
          target_workspace_id: WS_PLATFORM,
        },
      ],
      turns: [
        {
          id: "t1",
          poster_account_id: ALICE,
          target_workspace_id: WS_PLATFORM,
          operator_account_id: OPERATOR,
          state: "completed",
          failure_reason: null,
          failure_detail: null,
          execution_profile: CODEX_FRONTIER,
        },
        {
          id: "t2",
          poster_account_id: BOB,
          target_workspace_id: WS_MOBILE,
          operator_account_id: BOB,
          state: "completed",
          failure_reason: null,
          failure_detail: null,
          execution_profile: CLAUDE_ELEVATED,
        },
      ],
    });
    api.getConversation.mockResolvedValue(ok(thread));
    await openThread(api);
    // The stream's first connect replays the whole log.
    emit(msgFrame("m1", "t1", 1, ALICE, "first question"));
    emit(msgFrame("m2", "t1", 2, null, "first answer", "assistant"));
    emit(msgFrame("m3", "t2", 3, BOB, "second question", "user", WS_MOBILE));
    await flush();
    const model = lastState();
    expect(model.active?.id).toBe(CONV);
    const users = model.active!.entries.filter((e) => e.kind === "user");
    expect(users.map((e) => e.kind === "user" && e.id)).toEqual(["m1", "m3"]);
    // A thread whose turns went to two workspaces is one list entry, and each turn shows its own target.
    expect(model.conversations.filter((c) => c.id === CONV)).toHaveLength(1);
    const replies = model.active!.entries.filter((e) => e.kind === "reply");
    expect(replies.map((e) => e.kind === "reply" && e.target)).toEqual(["Platform", "Mobile"]);
  });

  it("attributes two human actors in one conversation to two distinct teammates", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "hello"));
    emit(msgFrame("m3", "t2", 3, BOB, "hi"));
    await flush();
    const authors = lastState()
      .active!.entries.filter((e) => e.kind === "user")
      .map((e) => (e.kind === "user" ? e.author : ""));
    expect(authors).toEqual(["alice", "bob"]);
  });
});

describe("ConversationPanel: the next turn's profile", () => {
  it("shows the hosted picker's profile for another machine and follows it without a reload", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const api = makeApi();
    await openThread(api);
    let model = lastState();
    // The host is another teammate's machine running Codex at frontier: the
    // panel shows that, never what this machine's resolvers would produce.
    expect(model.profile?.provider.text).toBe("Codex");
    expect(model.profile?.mode.text).toBe("frontier");
    expect(model.profile?.effort.text).toBe("high");
    expect(model.profile?.host).toBe("olivia's machine");

    api.picker.mockResolvedValue(ok(picker(CLAUDE_ELEVATED)));
    vi.advanceTimersByTime(15_000);
    await flush();
    model = lastState();
    expect(model.profile?.provider.text).toBe("Claude Headless");
    expect(model.profile?.mode.text).toBe("elevated");
  });

  it("re-reads the picker more often than the 30-second heartbeat", async () => {
    const { PICKER_POLL_MS } = await import("../../../src/views/conversation/ConversationPanel");
    expect(PICKER_POLL_MS).toBeLessThan(30_000);
  });

  it("shows the profile of the workspace the user picked in the composer", async () => {
    const api = makeApi();
    await openThread(api);
    await messageHandler!({ type: "selectTarget", targetWorkspaceId: WS_MOBILE });
    expect(lastState().profile?.provider.text).toBe("Claude Headless");
    expect(lastState().profile?.workspace).toBe("Mobile");
  });
});

describe("ConversationPanel: failure states", () => {
  async function stateAfterSend(api: Api, result: unknown) {
    api.postMessage.mockResolvedValueOnce(result);
    await messageHandler!({ type: "send", content: "go", targetWorkspaceId: null });
    await flush();
    return lastState();
  }

  it("renders offline, not entitled, quota exhausted, turn failed and stream disconnected distinctly", async () => {
    const api = makeApi();
    await openThread(api);
    const rendered: string[] = [];

    const offline = await stateAfterSend(api, refused("offline", 409, "TARGET_OFFLINE"));
    rendered.push(`${offline.status?.kind}|${offline.status?.title}`);
    const entitled = await stateAfterSend(
      api,
      refused("not_entitled", 403, "LICENSE_TIER_EXCEEDED")
    );
    rendered.push(`${entitled.status?.kind}|${entitled.status?.title}`);
    const quota = await stateAfterSend(
      api,
      refused("quota_exhausted", 429, "TURN_ALLOWANCE_EXHAUSTED")
    );
    rendered.push(`${quota.status?.kind}|${quota.status?.title}`);

    emit(msgFrame("m1", "t1", 1, ALICE, "q"));
    emit(stateFrame("t1", "failed", { failure_reason: "timeout" }));
    await flush();
    const failed = lastState().active!.entries.find((e) => e.kind === "reply");
    const failure = failed?.kind === "reply" ? failed.failure : null;
    rendered.push(`${failure?.kind}|${failure?.title}`);

    streamHandlers!.onStatus("reconnecting");
    await flush();
    rendered.push(`stream|${lastState().stream}`);

    expect(rendered).toEqual([
      "offline|Workspace offline",
      "not_entitled|Not included in your plan",
      "quota_exhausted|Allowance used up",
      "turn_failed|Turn failed",
      "stream|reconnecting",
    ]);
    expect(new Set(rendered).size).toBe(5);
  });

  it("renders a turn that lost its host as offline, not as a failed turn", async () => {
    const api = makeApi();
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "q"));
    emit(stateFrame("t1", "failed", { failure_reason: "target_unavailable" }));
    await flush();
    const reply = lastState().active!.entries.find((e) => e.kind === "reply");
    expect(reply?.kind === "reply" && reply.failure?.kind).toBe("offline");
  });
});

describe("ConversationPanel: the composer", () => {
  it("sends the message text as content, with no target unless the user picked one", async () => {
    const api = makeApi();
    await openThread(api);
    api.postMessage.mockResolvedValue(
      ok({
        message: {
          id: "m9",
          conversation_id: CONV,
          turn_id: "t9",
          position: 9,
          role: "user",
          content: "x",
          redacted: false,
          actor_account_id: ALICE,
          client_message_id: "c",
          target_workspace_id: WS_PLATFORM,
        },
        turn: {
          id: "t9",
          poster_account_id: ALICE,
          target_workspace_id: WS_PLATFORM,
          operator_account_id: null,
          state: "queued",
          failure_reason: null,
          failure_detail: null,
          execution_profile: null,
        },
      })
    );
    const text = '$(rm -rf ~); `id` "quoted" && echo pwned';
    await messageHandler!({ type: "send", content: text, targetWorkspaceId: null });
    await flush();
    const [conversationId, input] = api.postMessage.mock.calls[0]!;
    expect(conversationId).toBe(CONV);
    expect(input.content).toBe(text);
    expect(input).not.toHaveProperty("targetWorkspaceId");
    expect(input.clientMessageId).toMatch(/^[0-9a-f-]{36}$/);
    expect(posted.some((m) => m.type === "sent")).toBe(true);
  });

  it("renders the candidates of an ambiguous post as a picker and sends the one chosen", async () => {
    const api = makeApi();
    await openThread(api);
    api.postMessage.mockResolvedValueOnce(
      refused("choose_target", 409, "TARGET_AMBIGUOUS", {
        candidates: [
          { workspace_id: WS_PLATFORM, display_name: "Platform" },
          { workspace_id: WS_MOBILE, display_name: "Mobile" },
        ],
      })
    );
    await messageHandler!({ type: "send", content: "which one?", targetWorkspaceId: null });
    await flush();
    expect(lastState().candidates).toEqual([
      { id: WS_PLATFORM, label: "Platform" },
      { id: WS_MOBILE, label: "Mobile" },
    ]);
    api.postMessage.mockResolvedValueOnce(refused("offline", 409, "TARGET_OFFLINE"));
    await messageHandler!({ type: "send", content: "which one?", targetWorkspaceId: WS_MOBILE });
    expect(api.postMessage.mock.calls[1]![1].targetWorkspaceId).toBe(WS_MOBILE);
  });

  it("refuses a target the picker did not offer", async () => {
    const api = makeApi();
    await openThread(api);
    await messageHandler!({ type: "send", content: "hi", targetWorkspaceId: "ws-elsewhere" });
    expect(api.postMessage).not.toHaveBeenCalled();
  });

  it("refuses a message over the platform's limit without posting", async () => {
    const api = makeApi();
    await openThread(api);
    await messageHandler!({ type: "send", content: "é".repeat(20_000), targetWorkspaceId: null });
    expect(api.postMessage).not.toHaveBeenCalled();
    expect(lastState().status?.kind).toBe("invalid");
  });

  it("keeps a viewer's composer closed and never posts for them", async () => {
    const api = makeApi("viewer");
    await openThread(api);
    expect(lastState().canPost).toBe(false);
    expect(lastState().canCreate).toBe(false);
    await messageHandler!({ type: "send", content: "hi", targetWorkspaceId: null });
    expect(api.postMessage).not.toHaveBeenCalled();
  });
});

describe("ConversationPanel: Needs Attention cards", () => {
  function cardFrame(cardId: string): ConversationFrame {
    return {
      id: String(++frameId),
      type: "turn.reference",
      payload: { turn_id: "t1", kind: "card", object_id: cardId, workspace_id: WS_PLATFORM },
    };
  }

  it("shows a viewer the card without an act control, and ignores an act from them", async () => {
    const api = makeApi("viewer");
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "what is blocked?"));
    emit(cardFrame("dr_card1"));
    await flush();
    const reply = lastState().active!.entries.find((e) => e.kind === "reply");
    expect(reply?.kind === "reply" && reply.references).toEqual([
      {
        kind: "card",
        objectId: "dr_card1",
        label: "Needs Attention card dr_card1",
        actionable: false,
      },
    ]);
    await messageHandler!({ type: "actOnCard", cardId: "dr_card1" });
    expect(api.getAttentionCard).not.toHaveBeenCalled();
  });

  it("lets a developer act on the card as themselves, through the attention routes, creating no turn", async () => {
    const api = makeApi("developer");
    api.getAttentionCard.mockResolvedValue(
      ok({
        id: "dr_card1",
        title: "Approve the release",
        options: [{ id: "approve", label: "Approve" }],
      })
    );
    api.resolveAttentionCard.mockResolvedValue(ok({}));
    showQuickPick.mockImplementation(async (items: unknown[]) => items[0]);
    await openThread(api);
    emit(msgFrame("m1", "t1", 1, ALICE, "what is blocked?"));
    emit(cardFrame("dr_card1"));
    await flush();
    const reply = lastState().active!.entries.find((e) => e.kind === "reply");
    expect(reply?.kind === "reply" && reply.references[0]!.actionable).toBe(true);
    await messageHandler!({ type: "actOnCard", cardId: "dr_card1" });
    expect(api.resolveAttentionCard).toHaveBeenCalledWith("dr_card1", "approve");
    expect(api.postMessage).not.toHaveBeenCalled();
  });

  it("refuses an act on a card no reference frame put in the transcript", async () => {
    const api = makeApi("developer");
    await openThread(api);
    await messageHandler!({ type: "actOnCard", cardId: "dr_forged" });
    expect(api.getAttentionCard).not.toHaveBeenCalled();
  });
});

describe("ConversationPanel: the private choice", () => {
  it("sends visibility on create, off by default", async () => {
    const api = makeApi();
    api.createConversation.mockResolvedValue(ok(conversation({ id: "conv-new" })));
    api.getConversation.mockResolvedValue(
      ok(emptyThread({ conversation: conversation({ id: "conv-new" }) }))
    );
    await openPanel(api);
    expect(lastState().privateDetail).toMatch(/Owners and admins still see/);
    expect(lastState().privateDetail).toMatch(/Work on GitHub/);
    await messageHandler!({ type: "newConversation", private: false });
    await flush();
    expect(api.createConversation).toHaveBeenCalledWith(WS_PLATFORM, "team");
  });

  it("shows the private badge only when the service confirms private", async () => {
    const api = makeApi();
    api.createConversation.mockResolvedValue(
      ok(conversation({ id: "conv-new", visibility: "private" }))
    );
    api.getConversation.mockResolvedValue(
      ok(emptyThread({ conversation: conversation({ id: "conv-new", visibility: "private" }) }))
    );
    await openPanel(api);
    await messageHandler!({ type: "newConversation", private: true });
    await flush();
    expect(api.createConversation).toHaveBeenCalledWith(WS_PLATFORM, "private");
    expect(lastState().active?.privateBadge).toBe(true);
    expect(lastState().notice).toBeNull();
  });

  it("says so and shows no badge when the service does not confirm private", async () => {
    const api = makeApi();
    api.createConversation.mockResolvedValue(
      ok(conversation({ id: "conv-new", visibility: "team" }))
    );
    api.getConversation.mockResolvedValue(
      ok(emptyThread({ conversation: conversation({ id: "conv-new" }) }))
    );
    await openPanel(api);
    await messageHandler!({ type: "newConversation", private: true });
    await flush();
    expect(lastState().active?.privateBadge).toBe(false);
    expect(lastState().notice).toMatch(/did not confirm this conversation as private/);
  });
});

describe("message boundary", () => {
  it("opens only a thread the service listed", async () => {
    const api = makeApi();
    await openPanel(api);
    await messageHandler!({ type: "selectConversation", conversationId: "conv-unlisted" });
    expect(api.getConversation).not.toHaveBeenCalled();
  });

  it("keeps card text from posing as a quick-pick icon", () => {
    expect(plainLabel("$(check) Approve")).not.toContain("$(");
    expect(plainLabel("x".repeat(500))).toHaveLength(200);
  });
});

describe("parseChatMessage", () => {
  it("drops unknown types, extra keys and malformed ids", () => {
    expect(parseChatMessage({ type: "ready" })).toEqual({ type: "ready" });
    expect(parseChatMessage({ type: "exec", cmd: "rm" })).toBeUndefined();
    expect(parseChatMessage({ type: "ready", extra: 1 })).toBeUndefined();
    expect(parseChatMessage({ type: "cancel", turnId: "../../x" })).toBeUndefined();
    expect(parseChatMessage({ type: "send", content: 3, targetWorkspaceId: null })).toBeUndefined();
    expect(
      parseChatMessage({ type: "send", content: "hi", targetWorkspaceId: "a b" })
    ).toBeUndefined();
    expect(parseChatMessage({ type: "newConversation", private: "yes" })).toBeUndefined();
    expect(parseChatMessage({ type: "__proto__" })).toBeUndefined();
  });
});
