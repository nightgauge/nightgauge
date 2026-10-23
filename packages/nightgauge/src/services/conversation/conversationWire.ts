/**
 * The conversation contract as the extension's chat panel reads it (#1571).
 *
 * The hosted service's committed OpenAPI document is the wire authority;
 * these are the fields the panel uses, parsed defensively. Everything that
 * arrives here is untrusted: message text, display names and failure details
 * come from other people and from a model, so a parser copies only the
 * fields it knows, each checked for its type, and drops anything malformed
 * rather than guessing.
 *
 * @see docs/CONVERSATION_TURNS.md § The extension's chat panel
 */

export type SessionVisibility = "team" | "private";
export type TeamRole = "owner" | "admin" | "developer" | "viewer";
export type TurnState = "queued" | "dispatched" | "running" | "completed" | "failed" | "cancelled";

/** The execution profile the hosted picker or a terminal frame reports, as received. */
export interface WireExecutionProfile {
  adapter: string;
  performance_mode: string;
  effort: string;
}

export interface Conversation {
  id: string;
  team_id: string;
  created_by: string | null;
  title: string | null;
  visibility: SessionVisibility;
  last_activity_at: string;
}

export interface ConversationMessage {
  id: string;
  conversation_id: string;
  turn_id: string;
  position: number;
  role: "user" | "assistant";
  content: string | null;
  redacted: boolean;
  actor_account_id: string | null;
  client_message_id: string | null;
  target_workspace_id: string | null;
}

export interface ConversationTurn {
  id: string;
  poster_account_id: string | null;
  target_workspace_id: string | null;
  operator_account_id: string | null;
  state: TurnState;
  failure_reason: string | null;
  failure_detail: string | null;
  execution_profile: WireExecutionProfile | null;
}

export interface ConversationThread {
  conversation: Conversation;
  messages: ConversationMessage[];
  turns: ConversationTurn[];
}

export interface Team {
  id: string;
  name: string;
  role: TeamRole;
  is_default: boolean;
}

export interface TeamMember {
  account_id: string;
  login: string;
}

export interface WorkspaceSummary {
  id: string;
  slug: string;
  display_name: string;
}

/** One workspace in the conversation picker (`GET /v1/workspaces?conversation_id=`). */
export interface PickerWorkspace extends WorkspaceSummary {
  eligible: boolean;
  reason: string | null;
  host_operator_account_id: string | null;
  execution_profile: WireExecutionProfile | null;
}

export interface ConversationPicker {
  workspaces: PickerWorkspace[];
  previous_target_workspace_id: string | null;
  resolved_target_workspace_id: string | null;
}

export interface TargetCandidate {
  workspace_id: string;
  display_name: string;
}

/** An attention option a card offers, as `GET /v1/attention/{id}` returns it. */
export interface AttentionOption {
  id: string;
  label: string;
}

export interface AttentionCard {
  id: string;
  title: string;
  options: AttentionOption[];
}

/** A stream frame (`GET /v1/conversations/{id}/stream`), narrowed to what the panel reads. */
export type ConversationFrame =
  | {
      id: string;
      type: "message";
      payload: {
        message_id: string;
        turn_id: string;
        position: number;
        role: "user" | "assistant";
        actor_account_id: string | null;
        content: string | null;
        redacted: boolean;
        target_workspace_id: string | null;
        client_message_id: string | null;
      };
    }
  | {
      id: string;
      type: "turn.state";
      payload: {
        turn_id: string;
        state: string;
        failure_reason: string | null;
        failure_detail: string | null;
        target_workspace_id: string | null;
        operator_account_id: string | null;
        execution_profile: WireExecutionProfile | null;
      };
    }
  | { id: string; type: "turn.delta"; payload: { turn_id: string; seq: number; text: string } }
  | {
      id: string;
      type: "turn.reference";
      payload: {
        turn_id: string;
        kind: "card" | "issue" | "run";
        object_id: string;
        workspace_id: string;
      };
    }
  | {
      id: string;
      type: "turn.action";
      payload: { turn_id: string; verb: string; outcome: string; actor_account_id: string | null };
    }
  | {
      id: string;
      type: "message.redacted";
      payload: { message_ids: string[]; delta_turn_ids: string[] };
    }
  | { id: string; type: "stream.reset" };

type Obj = Record<string, unknown>;

function isObj(v: unknown): v is Obj {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}
function strOrNull(v: unknown): string | null {
  return typeof v === "string" ? v : null;
}
function int(v: unknown): number | undefined {
  return typeof v === "number" && Number.isInteger(v) ? v : undefined;
}

const TURN_STATES: readonly TurnState[] = [
  "queued",
  "dispatched",
  "running",
  "completed",
  "failed",
  "cancelled",
];
const ROLES: readonly TeamRole[] = ["owner", "admin", "developer", "viewer"];

export function isTurnState(v: unknown): v is TurnState {
  return (TURN_STATES as readonly unknown[]).includes(v);
}

export function isTerminalTurnState(state: string): boolean {
  return state === "completed" || state === "failed" || state === "cancelled";
}

export function parseExecutionProfile(raw: unknown): WireExecutionProfile | null {
  if (!isObj(raw)) return null;
  const adapter = str(raw.adapter);
  const mode = str(raw.performance_mode);
  const effort = str(raw.effort);
  if (adapter === undefined || mode === undefined) return null;
  return { adapter, performance_mode: mode, effort: effort ?? "" };
}

export function parseConversation(raw: unknown): Conversation | null {
  if (!isObj(raw)) return null;
  const id = str(raw.id);
  const team = str(raw.team_id);
  if (!id || !team) return null;
  const visibility = raw.visibility === "private" ? "private" : "team";
  return {
    id,
    team_id: team,
    created_by: strOrNull(raw.created_by),
    title: strOrNull(raw.title),
    visibility,
    last_activity_at: str(raw.last_activity_at) ?? "",
  };
}

export function parseMessage(raw: unknown): ConversationMessage | null {
  if (!isObj(raw)) return null;
  const id = str(raw.id);
  const turn = str(raw.turn_id);
  const position = int(raw.position);
  const role = raw.role === "user" || raw.role === "assistant" ? raw.role : undefined;
  if (!id || !turn || position === undefined || !role) return null;
  return {
    id,
    conversation_id: str(raw.conversation_id) ?? "",
    turn_id: turn,
    position,
    role,
    content: strOrNull(raw.content),
    redacted: raw.redacted === true,
    actor_account_id: strOrNull(raw.actor_account_id),
    client_message_id: strOrNull(raw.client_message_id),
    target_workspace_id: strOrNull(raw.target_workspace_id),
  };
}

export function parseTurn(raw: unknown): ConversationTurn | null {
  if (!isObj(raw)) return null;
  const id = str(raw.id);
  if (!id || !isTurnState(raw.state)) return null;
  return {
    id,
    poster_account_id: strOrNull(raw.poster_account_id),
    target_workspace_id: strOrNull(raw.target_workspace_id),
    operator_account_id: strOrNull(raw.operator_account_id),
    state: raw.state,
    failure_reason: strOrNull(raw.failure_reason),
    failure_detail: strOrNull(raw.failure_detail),
    execution_profile: parseExecutionProfile(raw.execution_profile),
  };
}

function parseList<T>(raw: unknown, parse: (v: unknown) => T | null): T[] {
  if (!Array.isArray(raw)) return [];
  const out: T[] = [];
  for (const item of raw) {
    const parsed = parse(item);
    if (parsed !== null) out.push(parsed);
  }
  return out;
}

export function parseThread(raw: unknown): ConversationThread | null {
  if (!isObj(raw)) return null;
  const conversation = parseConversation(raw.conversation);
  if (!conversation) return null;
  return {
    conversation,
    messages: parseList(raw.messages, parseMessage),
    turns: parseList(raw.turns, parseTurn),
  };
}

export function parseConversationList(raw: unknown): {
  team_id: string | null;
  conversations: Conversation[];
} | null {
  if (!isObj(raw) || !Array.isArray(raw.conversations)) return null;
  return {
    team_id: strOrNull(raw.team_id),
    conversations: parseList(raw.conversations, parseConversation),
  };
}

export function parseTeams(raw: unknown): Team[] | null {
  if (!isObj(raw) || !Array.isArray(raw.teams)) return null;
  return parseList(raw.teams, (t) => {
    if (!isObj(t)) return null;
    const id = str(t.id);
    const role = (ROLES as readonly unknown[]).includes(t.role) ? (t.role as TeamRole) : undefined;
    if (!id || !role) return null;
    return { id, name: str(t.name) ?? id, role, is_default: t.is_default === true };
  });
}

export function parseMembers(raw: unknown): TeamMember[] | null {
  if (!isObj(raw) || !Array.isArray(raw.members)) return null;
  return parseList(raw.members, (m) => {
    if (!isObj(m)) return null;
    const account = str(m.account_id);
    const login = str(m.login);
    if (!account || !login) return null;
    return { account_id: account, login };
  });
}

function parseWorkspaceSummary(w: unknown): WorkspaceSummary | null {
  if (!isObj(w)) return null;
  const id = str(w.id);
  if (!id) return null;
  const slug = str(w.slug) ?? "";
  return { id, slug, display_name: str(w.display_name) ?? slug };
}

export function parseWorkspaces(raw: unknown): WorkspaceSummary[] | null {
  if (!isObj(raw) || !Array.isArray(raw.workspaces)) return null;
  return parseList(raw.workspaces, parseWorkspaceSummary);
}

export function parsePicker(raw: unknown): ConversationPicker | null {
  if (!isObj(raw) || !Array.isArray(raw.workspaces)) return null;
  const workspaces = parseList(raw.workspaces, (w): PickerWorkspace | null => {
    const summary = parseWorkspaceSummary(w);
    if (!summary || !isObj(w)) return null;
    const target = isObj(w.conversation_target) ? w.conversation_target : {};
    const host = isObj(target.host) ? target.host : {};
    return {
      ...summary,
      eligible: target.eligible === true,
      reason: strOrNull(target.reason),
      host_operator_account_id: strOrNull(host.operator_account_id),
      execution_profile: parseExecutionProfile(target.execution_profile),
    };
  });
  return {
    workspaces,
    previous_target_workspace_id: strOrNull(raw.previous_target_workspace_id),
    resolved_target_workspace_id: strOrNull(raw.resolved_target_workspace_id),
  };
}

export function parseCandidates(raw: unknown): TargetCandidate[] {
  return parseList(raw, (c) => {
    if (!isObj(c)) return null;
    const id = str(c.workspace_id);
    if (!id) return null;
    return { workspace_id: id, display_name: str(c.display_name) ?? id };
  });
}

export function parseAttentionCard(raw: unknown): AttentionCard | null {
  if (!isObj(raw)) return null;
  const id = str(raw.id);
  if (!id) return null;
  const options = parseList(raw.options, (o) => {
    if (!isObj(o)) return null;
    const optionId = str(o.id);
    if (!optionId) return null;
    return { id: optionId, label: str(o.label) ?? optionId };
  });
  return { id, title: str(raw.title) ?? id, options };
}

/** Parse one SSE `data:` line into a frame the panel reads, or null to ignore it. */
export function parseFrame(data: string): ConversationFrame | null {
  let raw: unknown;
  try {
    raw = JSON.parse(data);
  } catch {
    return null;
  }
  if (!isObj(raw)) return null;
  const id = str(raw.id);
  const type = str(raw.type);
  if (!id || !type) return null;
  if (type === "stream.reset") return { id, type };
  const p = isObj(raw.payload) ? raw.payload : null;
  if (!p) return null;
  const turnId = str(p.turn_id);
  switch (type) {
    case "message": {
      const messageId = str(p.message_id);
      const position = int(p.position);
      const role = p.role === "user" || p.role === "assistant" ? p.role : undefined;
      if (!messageId || !turnId || position === undefined || !role) return null;
      return {
        id,
        type,
        payload: {
          message_id: messageId,
          turn_id: turnId,
          position,
          role,
          actor_account_id: strOrNull(p.actor_account_id),
          content: strOrNull(p.content),
          redacted: p.redacted === true,
          target_workspace_id: strOrNull(p.target_workspace_id),
          client_message_id: strOrNull(p.client_message_id),
        },
      };
    }
    case "turn.state": {
      const state = str(p.state);
      if (!turnId || !state) return null;
      return {
        id,
        type,
        payload: {
          turn_id: turnId,
          state,
          failure_reason: strOrNull(p.failure_reason),
          failure_detail: strOrNull(p.failure_detail),
          target_workspace_id: strOrNull(p.target_workspace_id),
          operator_account_id: strOrNull(p.operator_account_id),
          execution_profile: parseExecutionProfile(p.execution_profile),
        },
      };
    }
    case "turn.delta": {
      const seq = int(p.seq);
      if (!turnId || seq === undefined) return null;
      return { id, type, payload: { turn_id: turnId, seq, text: str(p.text) ?? "" } };
    }
    case "turn.reference": {
      const kind = p.kind === "card" || p.kind === "issue" || p.kind === "run" ? p.kind : undefined;
      const objectId = str(p.object_id);
      if (!turnId || !kind || !objectId) return null;
      return {
        id,
        type,
        payload: {
          turn_id: turnId,
          kind,
          object_id: objectId,
          workspace_id: str(p.workspace_id) ?? "",
        },
      };
    }
    case "turn.action": {
      if (!turnId) return null;
      return {
        id,
        type,
        payload: {
          turn_id: turnId,
          verb: str(p.verb) ?? "",
          outcome: str(p.outcome) ?? "",
          actor_account_id: strOrNull(p.actor_account_id),
        },
      };
    }
    case "message.redacted": {
      const ids = Array.isArray(p.message_ids)
        ? p.message_ids.filter((x) => typeof x === "string")
        : [];
      const single = str(p.message_id);
      if (single) ids.push(single);
      const deltaTurns = Array.isArray(p.delta_turn_ids)
        ? p.delta_turn_ids.filter((x) => typeof x === "string")
        : [];
      if (turnId) deltaTurns.push(turnId);
      return {
        id,
        type,
        payload: { message_ids: ids as string[], delta_turn_ids: deltaTurns as string[] },
      };
    }
    default:
      return null;
  }
}
