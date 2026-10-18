/**
 * ConversationTranscript — the panel's copy of one platform conversation
 * (#1571), built only from what the hosted service returns.
 *
 * It holds no message the platform did not send: the thread read
 * (`GET /v1/conversations/{id}`) seeds it and the stream's frames extend it.
 * Messages keep the platform's ids and are ordered by the platform's
 * `position`, so the panel cannot drift into a local-only store. Applying a
 * frame twice changes nothing: the stream replays its log on connect, and a
 * frame the seed already holds is a no-op.
 *
 * A turn's reply is built from its `turn.delta` frames, in `seq` order, until
 * the stored assistant message replaces it. Once a turn has a terminal state,
 * no further chunk is appended to it.
 */

import {
  isTerminalTurnState,
  type ConversationFrame,
  type ConversationThread,
  type WireExecutionProfile,
} from "./conversationWire";

export interface TranscriptMessage {
  id: string;
  turnId: string;
  position: number;
  role: "user" | "assistant";
  content: string | null;
  redacted: boolean;
  actorAccountId: string | null;
  targetWorkspaceId: string | null;
}

export interface TranscriptReference {
  kind: "card" | "issue" | "run";
  objectId: string;
  workspaceId: string;
}

export interface TranscriptTurn {
  id: string;
  state: string;
  failureReason: string | null;
  failureDetail: string | null;
  targetWorkspaceId: string | null;
  operatorAccountId: string | null;
  profile: WireExecutionProfile | null;
  /** Streamed chunks by sequence number. */
  deltas: Map<number, string>;
  redacted: boolean;
  references: TranscriptReference[];
  actions: { verb: string; outcome: string; actorAccountId: string | null }[];
}

/** What applying a frame changed, so the panel posts only that to the webview. */
export type TranscriptChange =
  | { kind: "none" }
  | { kind: "delta"; turnId: string; seq: number; text: string }
  | { kind: "changed" }
  | { kind: "reset" };

export class ConversationTranscript {
  private readonly messages = new Map<string, TranscriptMessage>();
  private readonly turns = new Map<string, TranscriptTurn>();
  /** The highest frame id applied: frame ids increase within a conversation. */
  private lastFrameId = 0;

  constructor(readonly conversationId: string) {}

  /** Replace everything with the thread the service returned. */
  seed(thread: ConversationThread): void {
    this.messages.clear();
    this.turns.clear();
    for (const t of thread.turns) {
      const turn = this.turn(t.id);
      turn.state = t.state;
      turn.failureReason = t.failure_reason;
      turn.failureDetail = t.failure_detail;
      turn.targetWorkspaceId = t.target_workspace_id;
      turn.operatorAccountId = t.operator_account_id;
      turn.profile = t.execution_profile;
    }
    for (const m of thread.messages) {
      this.messages.set(m.id, {
        id: m.id,
        turnId: m.turn_id,
        position: m.position,
        role: m.role,
        content: m.redacted ? null : m.content,
        redacted: m.redacted,
        actorAccountId: m.actor_account_id,
        targetWorkspaceId: m.target_workspace_id,
      });
      this.turn(m.turn_id);
    }
  }

  apply(frame: ConversationFrame): TranscriptChange {
    // A frame with no id is the panel's own copy of a post's response, not a
    // stream frame: it is never used to skip later frames, and it never
    // overrides a turn state the stream already reported.
    const sequenced = frame.id !== "";
    if (sequenced && frame.type !== "stream.reset") {
      const seq = Number(frame.id);
      if (Number.isSafeInteger(seq)) {
        if (seq <= this.lastFrameId) return { kind: "none" };
        this.lastFrameId = seq;
      }
    }
    switch (frame.type) {
      case "stream.reset":
        return { kind: "reset" };
      case "message": {
        const p = frame.payload;
        const existing = this.messages.get(p.message_id);
        const next: TranscriptMessage = {
          id: p.message_id,
          turnId: p.turn_id,
          position: p.position,
          role: p.role,
          content: p.redacted ? null : p.content,
          redacted: p.redacted || existing?.redacted === true,
          actorAccountId: p.actor_account_id,
          targetWorkspaceId: p.target_workspace_id,
        };
        if (next.redacted) next.content = null;
        if (existing && sameMessage(existing, next)) return { kind: "none" };
        this.messages.set(p.message_id, next);
        this.turn(p.turn_id);
        return { kind: "changed" };
      }
      case "turn.state": {
        const p = frame.payload;
        const turn = this.turn(p.turn_id);
        if (!sequenced && turn.state !== "") return { kind: "none" };
        // A terminal state is final: a replayed earlier state never reopens it.
        if (isTerminalTurnState(turn.state) && !isTerminalTurnState(p.state))
          return { kind: "none" };
        turn.state = p.state;
        turn.failureReason = p.failure_reason;
        turn.failureDetail = p.failure_detail;
        if (p.target_workspace_id !== null) turn.targetWorkspaceId = p.target_workspace_id;
        if (p.operator_account_id !== null) turn.operatorAccountId = p.operator_account_id;
        if (p.execution_profile) turn.profile = p.execution_profile;
        return { kind: "changed" };
      }
      case "turn.delta": {
        const p = frame.payload;
        const turn = this.turn(p.turn_id);
        if (isTerminalTurnState(turn.state) || turn.redacted) return { kind: "none" };
        if (this.replyMessage(p.turn_id)) return { kind: "none" };
        if (turn.deltas.has(p.seq)) return { kind: "none" };
        turn.deltas.set(p.seq, p.text);
        // An out-of-order chunk changes text before the end; render it whole.
        const maxSeq = Math.max(...turn.deltas.keys());
        if (p.seq !== maxSeq) return { kind: "changed" };
        return { kind: "delta", turnId: p.turn_id, seq: p.seq, text: p.text };
      }
      case "turn.reference": {
        const p = frame.payload;
        const turn = this.turn(p.turn_id);
        if (turn.references.some((r) => r.kind === p.kind && r.objectId === p.object_id)) {
          return { kind: "none" };
        }
        turn.references.push({ kind: p.kind, objectId: p.object_id, workspaceId: p.workspace_id });
        return { kind: "changed" };
      }
      case "turn.action": {
        const p = frame.payload;
        this.turn(p.turn_id).actions.push({
          verb: p.verb,
          outcome: p.outcome,
          actorAccountId: p.actor_account_id,
        });
        return { kind: "changed" };
      }
      case "message.redacted": {
        for (const id of frame.payload.message_ids) {
          const m = this.messages.get(id);
          if (m) {
            m.redacted = true;
            m.content = null;
          }
        }
        for (const turnId of frame.payload.delta_turn_ids) {
          const turn = this.turn(turnId);
          turn.redacted = true;
          turn.deltas.clear();
          for (const m of this.messages.values()) {
            if (m.turnId === turnId) {
              m.redacted = true;
              m.content = null;
            }
          }
        }
        return { kind: "changed" };
      }
    }
  }

  /** Messages in the platform's order. */
  orderedMessages(): TranscriptMessage[] {
    return [...this.messages.values()].sort((a, b) => a.position - b.position);
  }

  getTurn(turnId: string): TranscriptTurn | undefined {
    return this.turns.get(turnId);
  }

  /** The turn still waiting or running, if any (one per conversation, ADR-017 Decision 8). */
  inFlightTurns(): TranscriptTurn[] {
    return [...this.turns.values()].filter((t) => t.state !== "" && !isTerminalTurnState(t.state));
  }

  /** The stored reply of a turn, once the service has stored it. */
  replyMessage(turnId: string): TranscriptMessage | undefined {
    for (const m of this.messages.values()) {
      if (m.turnId === turnId && m.role === "assistant") return m;
    }
    return undefined;
  }

  /** The reply text so far: the stored reply, else the streamed chunks in order. */
  replyText(turnId: string): string {
    const reply = this.replyMessage(turnId);
    if (reply) return reply.content ?? "";
    const turn = this.turns.get(turnId);
    if (!turn) return "";
    return [...turn.deltas.entries()]
      .sort(([a], [b]) => a - b)
      .map(([, text]) => text)
      .join("");
  }

  /** True when the transcript shows this card, so an act on it came from a reference frame. */
  hasCardReference(cardId: string): boolean {
    for (const turn of this.turns.values()) {
      if (turn.references.some((r) => r.kind === "card" && r.objectId === cardId)) return true;
    }
    return false;
  }

  private turn(id: string): TranscriptTurn {
    let turn = this.turns.get(id);
    if (!turn) {
      turn = {
        id,
        state: "",
        failureReason: null,
        failureDetail: null,
        targetWorkspaceId: null,
        operatorAccountId: null,
        profile: null,
        deltas: new Map(),
        redacted: false,
        references: [],
        actions: [],
      };
      this.turns.set(id, turn);
    }
    return turn;
  }
}

function sameMessage(a: TranscriptMessage, b: TranscriptMessage): boolean {
  return (
    a.position === b.position &&
    a.content === b.content &&
    a.redacted === b.redacted &&
    a.actorAccountId === b.actorAccountId &&
    a.targetWorkspaceId === b.targetWorkspaceId &&
    a.turnId === b.turnId &&
    a.role === b.role
  );
}
