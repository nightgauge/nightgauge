/**
 * ConversationClient — the chat panel's calls to the hosted conversation
 * contract (#1571), as the signed-in account.
 *
 * Every call is a JSON request carrying the window's session token; a 401 or
 * 403 refreshes the token once and retries, as the workspace throttle reader
 * does. A message's text travels only as the `content` field of a JSON body:
 * nothing here spawns a process or builds a command line, so the text is
 * never interpolated into one.
 *
 * Calls never throw. Each resolves to the parsed answer or to a typed
 * failure, classified so the panel can render each refusal differently.
 */

import type { ITokenStorage } from "../../platform/TokenStorage";
import type { IOnDemandTokenRefresher } from "../../platform/TokenRefreshManager";
import { classifyRefusal, type FailureKind } from "./conversationDisplay";
import {
  parseAttentionCard,
  parseCandidates,
  parseConversation,
  parseConversationList,
  parseMembers,
  parseMessage,
  parsePicker,
  parseTeams,
  parseThread,
  parseTurn,
  parseWorkspaces,
  type AttentionCard,
  type Conversation,
  type ConversationMessage,
  type ConversationPicker,
  type ConversationThread,
  type ConversationTurn,
  type SessionVisibility,
  type TargetCandidate,
  type Team,
  type TeamMember,
  type WorkspaceSummary,
} from "./conversationWire";

export interface ConversationFailure {
  ok: false;
  kind: FailureKind;
  status: number;
  code?: string;
  /** The service's message, as received; shown as text, never as markup. */
  message: string;
  /** Workspaces the caller may pick, on `TARGET_AMBIGUOUS` and `PREVIOUS_TARGET_UNAVAILABLE`. */
  candidates?: TargetCandidate[];
}

export type ConversationResult<T> = { ok: true; value: T } | ConversationFailure;

export interface PostMessageInput {
  content: string;
  clientMessageId: string;
  /** Sent only when the user picked a workspace in the composer. */
  targetWorkspaceId?: string;
}

/** The calls the panel makes. The panel depends on this, so tests stub it. */
export interface ConversationApi {
  listTeams(): Promise<ConversationResult<Team[]>>;
  listWorkspaces(teamId: string): Promise<ConversationResult<WorkspaceSummary[]>>;
  listMembers(teamId: string): Promise<ConversationResult<TeamMember[]>>;
  listConversations(teamId: string): Promise<ConversationResult<Conversation[]>>;
  createConversation(
    workspaceId: string,
    visibility: SessionVisibility
  ): Promise<ConversationResult<Conversation>>;
  getConversation(conversationId: string): Promise<ConversationResult<ConversationThread>>;
  postMessage(
    conversationId: string,
    input: PostMessageInput
  ): Promise<ConversationResult<{ message: ConversationMessage; turn: ConversationTurn }>>;
  cancelTurn(
    conversationId: string,
    turnId: string
  ): Promise<ConversationResult<{ ended: boolean }>>;
  picker(conversationId: string): Promise<ConversationResult<ConversationPicker>>;
  getAttentionCard(cardId: string): Promise<ConversationResult<AttentionCard>>;
  resolveAttentionCard(cardId: string, optionId: string): Promise<ConversationResult<unknown>>;
}

/** Ids the panel puts in a path: the service's own ids, never free text. */
const PATH_ID_RE = /^[A-Za-z0-9_-]{1,100}$/;

export function isPathId(value: unknown): value is string {
  return typeof value === "string" && PATH_ID_RE.test(value);
}

function failure(
  kind: FailureKind,
  status: number,
  message: string,
  extra: Partial<ConversationFailure> = {}
): ConversationFailure {
  return { ok: false, kind, status, message, ...extra };
}

function malformed(status: number): ConversationFailure {
  return failure("error", status, "The hosted service's answer could not be read.");
}

export class ConversationClient implements ConversationApi {
  constructor(
    private readonly getPlatformUrl: () => string,
    private readonly tokenStorage: Pick<ITokenStorage, "retrieve">,
    private readonly tokenRefresher?: IOnDemandTokenRefresher,
    private readonly fetchImpl: typeof fetch = (...args) => fetch(...args)
  ) {}

  async listTeams(): Promise<ConversationResult<Team[]>> {
    return this.read("GET", "/v1/teams", undefined, parseTeams);
  }

  async listWorkspaces(teamId: string): Promise<ConversationResult<WorkspaceSummary[]>> {
    return this.read(
      "GET",
      `/v1/workspaces?team_id=${encodeURIComponent(teamId)}`,
      undefined,
      parseWorkspaces
    );
  }

  async listMembers(teamId: string): Promise<ConversationResult<TeamMember[]>> {
    if (!isPathId(teamId)) return failure("invalid", 0, "Invalid team id.");
    return this.read("GET", `/v1/team/${teamId}/members`, undefined, parseMembers);
  }

  async listConversations(teamId: string): Promise<ConversationResult<Conversation[]>> {
    return this.read(
      "GET",
      `/v1/conversations?team_id=${encodeURIComponent(teamId)}&limit=50`,
      undefined,
      (raw) => parseConversationList(raw)?.conversations ?? null
    );
  }

  async createConversation(
    workspaceId: string,
    visibility: SessionVisibility
  ): Promise<ConversationResult<Conversation>> {
    return this.read(
      "POST",
      "/v1/conversations",
      { workspace_id: workspaceId, visibility },
      parseConversation
    );
  }

  async getConversation(conversationId: string): Promise<ConversationResult<ConversationThread>> {
    if (!isPathId(conversationId)) return failure("invalid", 0, "Invalid conversation id.");
    return this.read("GET", `/v1/conversations/${conversationId}`, undefined, parseThread);
  }

  async postMessage(
    conversationId: string,
    input: PostMessageInput
  ): Promise<ConversationResult<{ message: ConversationMessage; turn: ConversationTurn }>> {
    if (!isPathId(conversationId)) return failure("invalid", 0, "Invalid conversation id.");
    const body: Record<string, string> = {
      content: input.content,
      client_message_id: input.clientMessageId,
    };
    if (input.targetWorkspaceId !== undefined) body.target_workspace_id = input.targetWorkspaceId;
    return this.read("POST", `/v1/conversations/${conversationId}/messages`, body, (raw) => {
      const obj = raw as { message?: unknown; turn?: unknown } | null;
      const message = parseMessage(obj?.message);
      const turn = parseTurn(obj?.turn);
      return message && turn ? { message, turn } : null;
    });
  }

  async cancelTurn(
    conversationId: string,
    turnId: string
  ): Promise<ConversationResult<{ ended: boolean }>> {
    if (!isPathId(conversationId) || !isPathId(turnId)) {
      return failure("invalid", 0, "Invalid turn id.");
    }
    return this.read(
      "POST",
      `/v1/conversations/${conversationId}/turns/${turnId}/cancel`,
      {},
      (raw) => ({ ended: (raw as { ended?: unknown } | null)?.ended === true })
    );
  }

  async picker(conversationId: string): Promise<ConversationResult<ConversationPicker>> {
    return this.read(
      "GET",
      `/v1/workspaces?conversation_id=${encodeURIComponent(conversationId)}`,
      undefined,
      parsePicker
    );
  }

  async getAttentionCard(cardId: string): Promise<ConversationResult<AttentionCard>> {
    if (!isPathId(cardId)) return failure("invalid", 0, "Invalid card id.");
    return this.read("GET", `/v1/attention/${cardId}`, undefined, parseAttentionCard);
  }

  async resolveAttentionCard(
    cardId: string,
    optionId: string
  ): Promise<ConversationResult<unknown>> {
    if (!isPathId(cardId)) return failure("invalid", 0, "Invalid card id.");
    return this.read(
      "POST",
      `/v1/attention/${cardId}/resolve`,
      { option_id: optionId },
      (raw) => raw ?? {}
    );
  }

  private async read<T>(
    method: "GET" | "POST",
    path: string,
    body: unknown,
    parse: (raw: unknown) => T | null
  ): Promise<ConversationResult<T>> {
    let response: Response;
    try {
      const token = await this.tokenStorage.retrieve("accessToken");
      if (!token) return failure("signed_out", 0, "Sign in to use conversations.");
      response = await this.send(method, path, body, token);
      if ((response.status === 401 || response.status === 403) && this.tokenRefresher) {
        // A 403 may be a role refusal rather than an expired token; only a
        // body that names no permission or tier code is retried.
        const code = response.status === 403 ? await errorCode(response.clone()) : undefined;
        if (code === undefined) {
          const refreshed = await this.tokenRefresher.forceRefresh();
          if (refreshed) response = await this.send(method, path, body, refreshed);
        }
      }
    } catch (err) {
      return failure("network", 0, err instanceof Error ? err.message : String(err));
    }
    let raw: unknown = null;
    if (response.status !== 204) {
      try {
        raw = await response.json();
      } catch {
        raw = null;
      }
    }
    if (!response.ok) return refusal(response.status, raw);
    const parsed = parse(raw);
    return parsed === null ? malformed(response.status) : { ok: true, value: parsed };
  }

  private send(method: string, path: string, body: unknown, token: string): Promise<Response> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
    };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    return this.fetchImpl(`${this.getPlatformUrl()}${path}`, {
      method,
      headers,
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
  }
}

interface ErrorBody {
  code?: string;
  message?: string;
  details?: Record<string, unknown>;
}

/** The error object of either error shape the service answers with. */
function errorBody(raw: unknown): ErrorBody {
  if (typeof raw !== "object" || raw === null) return {};
  const obj = raw as Record<string, unknown>;
  const inner = typeof obj.error === "object" && obj.error !== null ? obj.error : obj;
  const e = inner as Record<string, unknown>;
  return {
    code: typeof e.code === "string" ? e.code : undefined,
    message: typeof e.message === "string" ? e.message : undefined,
    details:
      typeof e.details === "object" && e.details !== null
        ? (e.details as Record<string, unknown>)
        : undefined,
  };
}

async function errorCode(response: Response): Promise<string | undefined> {
  try {
    return errorBody(await response.json()).code;
  } catch {
    return undefined;
  }
}

/** A typed failure from a refused request. Exported for tests. */
export function refusal(status: number, raw: unknown): ConversationFailure {
  const { code, message, details } = errorBody(raw);
  const kind = classifyRefusal(status, code);
  const extra: Partial<ConversationFailure> = { code };
  if (kind === "choose_target") extra.candidates = parseCandidates(details?.candidates);
  return failure(kind, status, message ?? `The hosted service answered HTTP ${status}.`, extra);
}
