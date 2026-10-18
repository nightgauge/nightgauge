/**
 * ConversationPanel — the extension's chat panel on the conversation
 * contract (#1571, ADR-017).
 *
 * The panel is a client of the hosted conversation, not a local chat: it
 * lists the team's threads, reads one, posts to it and follows its stream,
 * and every message it shows is one the service returned, with the service's
 * ids and order. A conversation started here is the one the dashboard and the
 * app read. Turns are answered by the workspace host the service selects;
 * this panel never runs a model and never spawns a process, so the stage
 * interactive path (`runInteractiveStage`) is not its transport.
 *
 * The profile it shows for the next turn is the one the hosted picker
 * reports for the host that would answer this caller, which may be another
 * machine; it never reads this machine's own resolvers. It re-reads the
 * picker more often than the agent heartbeat, so a changed adapter or mode on
 * the host reaches the panel within one beat.
 *
 * The webview posts only the messages {@link parseChatMessage} accepts, and
 * the panel checks each against its own state: a cancel names an in-flight
 * turn of the open thread, an act names a card a reference frame put in the
 * transcript, a target is one the picker or a refusal offered.
 */

import { randomUUID } from "node:crypto";
import { getNonce } from "../../utils/webviewNonce.js";
import * as vscode from "vscode";
import type {
  ConversationApi,
  ConversationFailure,
} from "../../services/conversation/ConversationClient";
import {
  describeProfile,
  failureView,
  turnFailureView,
  type FailureView,
} from "../../services/conversation/conversationDisplay";
import type {
  ConversationStreamHandle,
  OpenConversationStream,
} from "../../services/conversation/ConversationStream";
import { ConversationTranscript } from "../../services/conversation/ConversationTranscript";
import type {
  Conversation,
  ConversationFrame,
  ConversationPicker,
  Team,
  WorkspaceSummary,
} from "../../services/conversation/conversationWire";
import { isTerminalTurnState } from "../../services/conversation/conversationWire";
import { PRIVATE_CONVERSATION_DETAIL } from "../../services/RunVisibility";
import { renderConversationHtml } from "./ConversationHtml";
import type { ChatViewModel, ReferenceView, TranscriptEntryView } from "./conversationViewModel";

export const CONVERSATION_VIEW_TYPE = "nightgaugeConversation";
export const OPEN_CONVERSATION_COMMAND = "nightgauge.openConversation";

/** Re-read the picker at half the 30-second agent heartbeat. */
export const PICKER_POLL_MS = 15_000;

/** The platform's limit on one message, in UTF-8 bytes. */
export const MAX_MESSAGE_BYTES = 32_768;

const ID_RE = /^[A-Za-z0-9_-]{1,100}$/;

/** The roles that hold `conversation:post` and `attention:act`. */
const POSTING_ROLES = new Set(["owner", "admin", "developer"]);

export type ChatMessage =
  | { type: "ready" }
  | { type: "refresh" }
  | { type: "signIn" }
  | { type: "switchTeam" }
  | { type: "selectConversation"; conversationId: string }
  | { type: "newConversation"; private: boolean }
  | { type: "send"; content: string; targetWorkspaceId: string | null }
  | { type: "selectTarget"; targetWorkspaceId: string | null }
  | { type: "cancel"; turnId: string }
  | { type: "actOnCard"; cardId: string };

const MESSAGE_KEYS: Record<ChatMessage["type"], readonly string[]> = {
  ready: [],
  refresh: [],
  signIn: [],
  switchTeam: [],
  selectConversation: ["conversationId"],
  newConversation: ["private"],
  send: ["content", "targetWorkspaceId"],
  selectTarget: ["targetWorkspaceId"],
  cancel: ["turnId"],
  actOnCard: ["cardId"],
};

function isId(v: unknown): v is string {
  return typeof v === "string" && ID_RE.test(v);
}

/** Shape-check a webview message. Returns undefined to drop it. */
export function parseChatMessage(raw: unknown): ChatMessage | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const msg = raw as Record<string, unknown>;
  const type = msg.type;
  if (typeof type !== "string" || !Object.prototype.hasOwnProperty.call(MESSAGE_KEYS, type)) {
    return undefined;
  }
  const allowed = new Set(["type", ...MESSAGE_KEYS[type as ChatMessage["type"]]]);
  if (Object.keys(msg).some((k) => !allowed.has(k))) return undefined;
  switch (type) {
    case "selectConversation":
      return isId(msg.conversationId) ? (msg as ChatMessage) : undefined;
    case "newConversation":
      return typeof msg.private === "boolean" ? (msg as ChatMessage) : undefined;
    case "send":
      if (typeof msg.content !== "string") return undefined;
      if (msg.targetWorkspaceId !== null && !isId(msg.targetWorkspaceId)) return undefined;
      return msg as ChatMessage;
    case "selectTarget":
      return msg.targetWorkspaceId === null || isId(msg.targetWorkspaceId)
        ? (msg as ChatMessage)
        : undefined;
    case "cancel":
      return isId(msg.turnId) ? (msg as ChatMessage) : undefined;
    case "actOnCard":
      return isId(msg.cardId) ? (msg as ChatMessage) : undefined;
    default:
      return msg as ChatMessage;
  }
}

/**
 * A card's text for a quick pick, which would render `$(name)` as an icon:
 * the sequence is broken so text from a card can never pose as an icon.
 */
export function plainLabel(text: string): string {
  return text.replace(/\$\(/g, () => "$\u200b(").slice(0, 200);
}

export interface ConversationPanelDeps {
  api: ConversationApi;
  openStream: OpenConversationStream;
  signedIn: () => Promise<boolean>;
  /** The slug of the platform workspace this window serves, or null. */
  getWorkspaceSlug: () => string | null;
  pickerPollMs?: number;
}

interface TeamContext {
  team: Team;
  workspaces: WorkspaceSummary[];
  /** The workspace a new conversation is opened on: this window's, else the team's first. */
  createWorkspaceId: string | null;
  members: Map<string, string>;
}

export class ConversationPanel implements vscode.Disposable {
  private static currentPanel: ConversationPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private signedIn = false;
  private loading = true;
  private teams: Team[] = [];
  private ctx: TeamContext | null = null;
  private conversations: Conversation[] = [];
  private active: Conversation | null = null;
  private transcript: ConversationTranscript | null = null;
  private stream: ConversationStreamHandle | null = null;
  private streamState: ChatViewModel["stream"] = "idle";
  private picker: ConversationPicker | null = null;
  private selectedTarget: string | null = null;
  private candidates: { id: string; label: string }[] | null = null;
  private status: FailureView | null = null;
  private notice: string | null = null;
  private posting = false;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  /** Turns whose reply element the webview holds, so a chunk can be appended to it. */
  private renderedTurns = new Set<string>();
  /** Bumped on every conversation switch so a late answer for the old one is dropped. */
  private generation = 0;

  private constructor(private deps: ConversationPanelDeps) {}

  static show(deps: ConversationPanelDeps): ConversationPanel {
    if (!ConversationPanel.currentPanel) {
      ConversationPanel.currentPanel = new ConversationPanel(deps);
    } else {
      ConversationPanel.currentPanel.deps = deps;
    }
    const current = ConversationPanel.currentPanel;
    current.reveal();
    return current;
  }

  static get current(): ConversationPanel | undefined {
    return ConversationPanel.currentPanel;
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.Beside);
      return;
    }
    this.panel = vscode.window.createWebviewPanel(
      CONVERSATION_VIEW_TYPE,
      "Nightgauge: Conversation",
      vscode.ViewColumn.Beside,
      { enableScripts: true, retainContextWhenHidden: true, localResourceRoots: [] }
    );
    this.panel.webview.html = renderConversationHtml({
      cspSource: this.panel.webview.cspSource,
      nonce: getNonce(),
    });
    this.panel.webview.onDidReceiveMessage(
      (msg) => this.handleMessage(msg),
      undefined,
      this.disposables
    );
    this.panel.onDidDispose(() => this.dispose(), undefined, this.disposables);
    const pollMs = this.deps.pickerPollMs ?? PICKER_POLL_MS;
    this.pollTimer = setInterval(() => void this.refreshPicker(), pollMs);
    void this.load();
  }

  async handleMessage(raw: unknown): Promise<void> {
    const msg = parseChatMessage(raw);
    if (!msg) return;
    switch (msg.type) {
      case "ready":
        this.postState();
        return;
      case "refresh":
        await this.load();
        return;
      case "signIn":
        await vscode.commands.executeCommand("nightgauge.platform.signIn");
        return;
      case "switchTeam":
        await this.switchTeam();
        return;
      case "selectConversation":
        // Only a thread the service listed for this team.
        if (!this.conversations.some((c) => c.id === msg.conversationId)) return;
        await this.openConversation(msg.conversationId);
        return;
      case "newConversation":
        await this.createConversation(msg.private);
        return;
      case "selectTarget":
        this.selectTarget(msg.targetWorkspaceId);
        return;
      case "send":
        await this.send(msg.content, msg.targetWorkspaceId);
        return;
      case "cancel":
        await this.cancel(msg.turnId);
        return;
      case "actOnCard":
        await this.actOnCard(msg.cardId);
        return;
    }
  }

  // ── Loading ────────────────────────────────────────────────────────────

  /** Read who the caller is on which team, then that team's threads. */
  async load(preferTeamId?: string): Promise<void> {
    this.loading = true;
    this.signedIn = await this.deps.signedIn();
    if (!this.signedIn) {
      this.loading = false;
      this.status = failureView(
        "signed_out",
        "Sign in to the hosted service to use conversations."
      );
      this.postState();
      return;
    }
    const teams = await this.deps.api.listTeams();
    if (!teams.ok) return this.fail(teams);
    this.teams = teams.value;
    if (this.teams.length === 0) {
      this.loading = false;
      this.status = failureView(
        "not_entitled",
        "Your account is not on a team, so it has no conversations."
      );
      this.postState();
      return;
    }
    const ctx = await this.resolveTeam(preferTeamId);
    if (!ctx) return;
    this.ctx = ctx;
    await this.reloadList();
    this.loading = false;
    this.postState();
  }

  /**
   * The team whose threads the panel lists: the one asked for, else the team
   * that owns this window's workspace, else the account's default team.
   */
  private async resolveTeam(preferTeamId?: string): Promise<TeamContext | null> {
    const slug = this.deps.getWorkspaceSlug();
    const ordered = preferTeamId
      ? this.teams.filter((t) => t.id === preferTeamId)
      : [...this.teams];
    let chosen: { team: Team; workspaces: WorkspaceSummary[] } | null = null;
    for (const team of ordered) {
      const ws = await this.deps.api.listWorkspaces(team.id);
      if (!ws.ok) {
        this.fail(ws);
        return null;
      }
      if (preferTeamId || (slug && ws.value.some((w) => w.slug === slug))) {
        chosen = { team, workspaces: ws.value };
        break;
      }
      if (!chosen && team.is_default) chosen = { team, workspaces: ws.value };
    }
    if (!chosen) {
      const team = this.teams[0];
      const ws = await this.deps.api.listWorkspaces(team.id);
      if (!ws.ok) {
        this.fail(ws);
        return null;
      }
      chosen = { team, workspaces: ws.value };
    }
    const own = slug ? chosen.workspaces.find((w) => w.slug === slug) : undefined;
    const members = new Map<string, string>();
    const listed = await this.deps.api.listMembers(chosen.team.id);
    if (listed.ok) for (const m of listed.value) members.set(m.account_id, m.login);
    return {
      team: chosen.team,
      workspaces: chosen.workspaces,
      createWorkspaceId: own?.id ?? chosen.workspaces[0]?.id ?? null,
      members,
    };
  }

  private async reloadList(): Promise<void> {
    if (!this.ctx) return;
    const list = await this.deps.api.listConversations(this.ctx.team.id);
    if (!list.ok) {
      this.fail(list);
      return;
    }
    this.conversations = list.value;
  }

  private async switchTeam(): Promise<void> {
    if (this.teams.length < 2) return;
    const picked = await vscode.window.showQuickPick(
      this.teams.map((t) => ({ label: t.name, description: t.role, id: t.id })),
      { title: "Nightgauge: Which team's conversations?" }
    );
    if (!picked) return;
    this.closeConversation();
    await this.load(picked.id);
  }

  // ── One conversation ───────────────────────────────────────────────────

  async openConversation(conversationId: string): Promise<void> {
    if (!this.ctx) return;
    this.closeConversation();
    const generation = this.generation;
    const thread = await this.deps.api.getConversation(conversationId);
    if (generation !== this.generation) return;
    if (!thread.ok) {
      if (thread.kind === "not_found") {
        this.conversations = this.conversations.filter((c) => c.id !== conversationId);
      }
      this.fail(thread);
      return;
    }
    this.active = thread.value.conversation;
    this.transcript = new ConversationTranscript(thread.value.conversation.id);
    this.transcript.seed(thread.value);
    this.status = null;
    this.postState();
    await this.refreshPicker();
    if (generation !== this.generation) return;
    this.stream = await this.deps.openStream(thread.value.conversation.id, {
      onFrame: (frame) => {
        if (generation === this.generation) void this.onFrame(frame);
      },
      onStatus: (status) => {
        if (generation !== this.generation) return;
        this.streamState = status;
        this.postState();
      },
      onNotFound: () => {
        if (generation !== this.generation) return;
        this.purgeActive();
      },
    });
  }

  private closeConversation(): void {
    this.generation++;
    this.stream?.close();
    this.stream = null;
    this.streamState = "idle";
    this.active = null;
    this.transcript = null;
    this.picker = null;
    this.selectedTarget = null;
    this.candidates = null;
    this.notice = null;
    this.renderedTurns.clear();
  }

  /** The thread is gone or no longer readable: drop every copy of its text. */
  private purgeActive(): void {
    const id = this.active?.id;
    this.closeConversation();
    this.conversations = this.conversations.filter((c) => c.id !== id);
    this.status = failureView("not_found", "This conversation is no longer available to you.");
    this.postState();
  }

  private async onFrame(frame: ConversationFrame): Promise<void> {
    const transcript = this.transcript;
    if (!transcript) return;
    const change = transcript.apply(frame);
    switch (change.kind) {
      case "none":
        return;
      case "reset": {
        const generation = this.generation;
        const thread = await this.deps.api.getConversation(transcript.conversationId);
        if (generation !== this.generation) return;
        if (thread.ok) transcript.seed(thread.value);
        this.postState();
        return;
      }
      case "delta":
        if (this.renderedTurns.has(change.turnId)) {
          this.post({ type: "delta", turnId: change.turnId, seq: change.seq, text: change.text });
        } else {
          this.postState();
        }
        return;
      case "changed":
        this.postState();
        if (frame.type === "turn.state" && isTerminalTurnState(frame.payload.state)) {
          void this.refreshPicker();
        }
        return;
    }
  }

  /** Read the hosted picker: the host and profile that would answer this caller. */
  async refreshPicker(): Promise<void> {
    const active = this.active;
    if (!active) return;
    const generation = this.generation;
    const picker = await this.deps.api.picker(active.id);
    if (generation !== this.generation) return;
    if (!picker.ok) {
      if (picker.kind === "not_found") this.purgeActive();
      return;
    }
    this.picker = picker.value;
    if (this.selectedTarget && !picker.value.workspaces.some((w) => w.id === this.selectedTarget)) {
      this.selectedTarget = null;
    }
    this.postState();
  }

  private async createConversation(wantPrivate: boolean): Promise<void> {
    if (!this.ctx || !this.canPost()) return;
    const workspaceId = this.ctx.createWorkspaceId;
    if (!workspaceId) {
      this.status = failureView("offline", "Your team has no workspace to open a conversation on.");
      this.postState();
      return;
    }
    const created = await this.deps.api.createConversation(
      workspaceId,
      wantPrivate ? "private" : "team"
    );
    if (!created.ok) return this.fail(created);
    await this.reloadList();
    if (!this.conversations.some((c) => c.id === created.value.id)) {
      this.conversations = [created.value, ...this.conversations];
    }
    await this.openConversation(created.value.id);
    // The badge follows the service's answer only: asked private, not
    // confirmed private, means no badge and a notice (#2400's rule).
    if (wantPrivate && created.value.visibility !== "private") {
      this.notice =
        "The hosted service did not confirm this conversation as private, so it is not marked " +
        "private. Treat it as visible to your team.";
      this.postState();
    }
  }

  private selectTarget(targetWorkspaceId: string | null): void {
    if (targetWorkspaceId !== null && !this.isOfferedTarget(targetWorkspaceId)) return;
    this.selectedTarget = targetWorkspaceId;
    this.postState();
  }

  private isOfferedTarget(id: string): boolean {
    return (
      (this.picker?.workspaces.some((w) => w.id === id) ?? false) ||
      (this.candidates?.some((c) => c.id === id) ?? false)
    );
  }

  private async send(content: string, targetWorkspaceId: string | null): Promise<void> {
    const active = this.active;
    if (!active || this.posting || !this.canPost()) return;
    if (content.trim() === "") return;
    if (Buffer.byteLength(content, "utf8") > MAX_MESSAGE_BYTES) {
      this.status = failureView(
        "invalid",
        `A message may be at most ${MAX_MESSAGE_BYTES} bytes. Shorten it and send again.`
      );
      this.postState();
      return;
    }
    if (targetWorkspaceId !== null && !this.isOfferedTarget(targetWorkspaceId)) return;
    this.posting = true;
    this.status = null;
    this.postState();
    const generation = this.generation;
    const result = await this.deps.api.postMessage(active.id, {
      content,
      clientMessageId: randomUUID(),
      ...(targetWorkspaceId !== null ? { targetWorkspaceId } : {}),
    });
    this.posting = false;
    if (generation !== this.generation) return;
    if (!result.ok) {
      if (result.kind === "choose_target") {
        this.candidates = (result.candidates ?? []).map((c) => ({
          id: c.workspace_id,
          label: c.display_name,
        }));
      }
      return this.fail(result);
    }
    this.candidates = null;
    // The accepted message joins the transcript through the stream; the
    // response seeds it too, so a slow stream does not hide the post.
    this.transcript?.apply({
      id: "",
      type: "message",
      payload: {
        message_id: result.value.message.id,
        turn_id: result.value.message.turn_id,
        position: result.value.message.position,
        role: result.value.message.role,
        actor_account_id: result.value.message.actor_account_id,
        content: result.value.message.content,
        redacted: result.value.message.redacted,
        target_workspace_id: result.value.message.target_workspace_id,
        client_message_id: result.value.message.client_message_id,
      },
    });
    this.transcript?.apply({
      id: "",
      type: "turn.state",
      payload: {
        turn_id: result.value.turn.id,
        state: result.value.turn.state,
        failure_reason: result.value.turn.failure_reason,
        failure_detail: result.value.turn.failure_detail,
        target_workspace_id: result.value.turn.target_workspace_id,
        operator_account_id: result.value.turn.operator_account_id,
        execution_profile: null,
      },
    });
    this.post({ type: "sent" });
    this.postState();
  }

  private async cancel(turnId: string): Promise<void> {
    const active = this.active;
    const transcript = this.transcript;
    if (!active || !transcript || !this.canPost()) return;
    if (!transcript.inFlightTurns().some((t) => t.id === turnId)) return;
    const result = await this.deps.api.cancelTurn(active.id, turnId);
    if (!result.ok) {
      if (result.code === "TURN_ENDED") return;
      return this.fail(result);
    }
    // The terminal frame arrives on the stream; until then the turn shows
    // that a cancel was asked for.
    this.notice = result.value.ended ? null : "Cancel sent. Waiting for the host to confirm.";
    this.postState();
  }

  /**
   * Act on a Needs Attention card shown by a reference frame, as the
   * clicking account, through the attention routes. Creates no turn.
   */
  private async actOnCard(cardId: string): Promise<void> {
    if (!this.transcript?.hasCardReference(cardId) || !this.canPost()) return;
    const card = await this.deps.api.getAttentionCard(cardId);
    if (!card.ok) return this.fail(card);
    if (card.value.options.length === 0) {
      void vscode.window.showInformationMessage("Nightgauge: this card offers no action.");
      return;
    }
    const picked = await vscode.window.showQuickPick(
      card.value.options.map((o) => ({ label: plainLabel(o.label), optionId: o.id })),
      { title: `Nightgauge: ${plainLabel(card.value.title)}` }
    );
    if (!picked) return;
    const resolved = await this.deps.api.resolveAttentionCard(cardId, picked.optionId);
    if (!resolved.ok) return this.fail(resolved);
    void vscode.window.showInformationMessage(
      `Nightgauge: recorded "${picked.label}" on the card as you.`
    );
  }

  // ── Rendering ──────────────────────────────────────────────────────────

  private canPost(): boolean {
    return this.ctx !== null && POSTING_ROLES.has(this.ctx.team.role);
  }

  private fail(failure: ConversationFailure): void {
    this.loading = false;
    if (failure.kind === "signed_out") this.signedIn = false;
    this.status = failureView(failure.kind, failure.message);
    this.postState();
  }

  private accountLabel(accountId: string | null): string {
    if (!accountId) return "unknown account";
    return this.ctx?.members.get(accountId) ?? `former member ${accountId.slice(0, 8)}`;
  }

  private workspaceLabel(workspaceId: string | null): string {
    if (!workspaceId) return "";
    const fromPicker = this.picker?.workspaces.find((w) => w.id === workspaceId);
    const fromTeam = this.ctx?.workspaces.find((w) => w.id === workspaceId);
    return fromPicker?.display_name ?? fromTeam?.display_name ?? workspaceId;
  }

  private entries(): TranscriptEntryView[] {
    const transcript = this.transcript;
    if (!transcript) return [];
    const out: TranscriptEntryView[] = [];
    const canAct = this.canPost();
    for (const m of transcript.orderedMessages()) {
      if (m.role === "assistant") continue;
      out.push({
        kind: "user",
        id: m.id,
        turnId: m.turnId,
        author: this.accountLabel(m.actorAccountId),
        authorAccountId: m.actorAccountId,
        text: m.content ?? "",
        redacted: m.redacted,
        target: this.workspaceLabel(m.targetWorkspaceId),
      });
      const turn = transcript.getTurn(m.turnId);
      const reply = transcript.replyMessage(m.turnId);
      const state = turn?.state || "queued";
      const references: ReferenceView[] = (turn?.references ?? []).map((r) => ({
        kind: r.kind,
        objectId: r.objectId,
        label:
          r.kind === "card"
            ? `Needs Attention card ${r.objectId}`
            : r.kind === "issue"
              ? `Issue ${r.objectId}`
              : `Run ${r.objectId}`,
        actionable: r.kind === "card" && canAct,
      }));
      out.push({
        kind: "reply",
        turnId: m.turnId,
        host: turn?.operatorAccountId
          ? `${this.accountLabel(turn.operatorAccountId)}'s machine`
          : "",
        text: transcript.replyText(m.turnId),
        redacted: (turn?.redacted ?? false) || (reply?.redacted ?? false),
        state,
        inFlight: !isTerminalTurnState(state),
        failure:
          state === "failed"
            ? turnFailureView(turn?.failureReason ?? null, turn?.failureDetail ?? null)
            : null,
        profile: turn?.profile ? describeProfile(turn.profile) : null,
        target: this.workspaceLabel(turn?.targetWorkspaceId ?? m.targetWorkspaceId),
        references,
        actions: (turn?.actions ?? []).map(
          (a) => `${a.verb} ${a.outcome} by ${this.accountLabel(a.actorAccountId)}`
        ),
      });
    }
    return out;
  }

  /** The view model the webview renders. Exported for tests through {@link snapshot}. */
  snapshot(): ChatViewModel {
    const entries = this.entries();
    const picker = this.picker;
    const targetId = this.selectedTarget ?? picker?.resolved_target_workspace_id ?? null;
    const target = targetId ? picker?.workspaces.find((w) => w.id === targetId) : undefined;
    let profile: ChatViewModel["profile"] = null;
    let profileNote: string | null = null;
    if (this.active) {
      if (target?.execution_profile) {
        profile = {
          ...describeProfile(target.execution_profile),
          host: target.host_operator_account_id
            ? `${this.accountLabel(target.host_operator_account_id)}'s machine`
            : "",
          workspace: target.display_name,
        };
      } else if (target && !target.eligible) {
        profileNote = `${target.display_name} cannot answer now (${target.reason ?? "unavailable"}).`;
      } else if (picker && !target) {
        profileNote = "The next post will ask you to choose a workspace.";
      }
    }
    const inFlight = entries.find((e) => e.kind === "reply" && e.inFlight);
    return {
      signedIn: this.signedIn,
      loading: this.loading,
      team: this.ctx ? { name: this.ctx.team.name, role: this.ctx.team.role } : null,
      canSwitchTeam: this.teams.length > 1,
      canPost: this.canPost(),
      canCreate: this.canPost() && (this.ctx?.createWorkspaceId ?? null) !== null,
      conversations: this.conversations.map((c) => ({
        id: c.id,
        title: c.title ?? `Conversation ${c.id.slice(0, 8)}`,
        privateBadge: c.visibility === "private",
        active: c.id === this.active?.id,
      })),
      active: this.active
        ? {
            id: this.active.id,
            title: this.active.title ?? `Conversation ${this.active.id.slice(0, 8)}`,
            privateBadge: this.active.visibility === "private",
            entries,
            inFlightTurnId: inFlight && inFlight.kind === "reply" ? inFlight.turnId : null,
          }
        : null,
      profile,
      profileNote,
      targets: (picker?.workspaces ?? []).map((w) => ({
        id: w.id,
        label: w.display_name,
        eligible: w.eligible,
        reason: w.reason,
      })),
      selectedTarget: this.selectedTarget,
      serverTarget: picker?.resolved_target_workspace_id
        ? this.workspaceLabel(picker.resolved_target_workspace_id)
        : null,
      candidates: this.candidates,
      status: this.status,
      stream: this.streamState,
      notice: this.notice,
      posting: this.posting,
      privateDetail: PRIVATE_CONVERSATION_DETAIL,
    };
  }

  private postState(): void {
    const model = this.snapshot();
    this.renderedTurns = new Set(
      (model.active?.entries ?? []).filter((e) => e.kind === "reply").map((e) => e.turnId)
    );
    this.post({ type: "state", model });
  }

  private post(message: Record<string, unknown>): void {
    void this.panel?.webview.postMessage(message);
  }

  dispose(): void {
    if (this.pollTimer !== null) clearInterval(this.pollTimer);
    this.pollTimer = null;
    this.closeConversation();
    this.panel = undefined;
    for (const d of this.disposables.splice(0)) d.dispose();
    if (ConversationPanel.currentPanel === this) ConversationPanel.currentPanel = undefined;
  }
}
