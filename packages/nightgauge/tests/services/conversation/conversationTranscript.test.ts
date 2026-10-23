/**
 * The panel's copy of a platform conversation (#1571): built only from the
 * thread read and the stream, in the platform's order, idempotent on replay.
 */

import { describe, expect, it } from "vitest";
import { ConversationTranscript } from "../../../src/services/conversation/ConversationTranscript";
import {
  parseFrame,
  type ConversationFrame,
} from "../../../src/services/conversation/conversationWire";

function frame(id: number, type: string, payload: Record<string, unknown>): ConversationFrame {
  const parsed = parseFrame(
    JSON.stringify({ id: String(id), type, conversation_id: "c1", payload, created_at: "x" })
  );
  if (!parsed) throw new Error(`fixture ${type} did not parse`);
  return parsed;
}

describe("parseFrame", () => {
  it("ignores malformed data and frame types it does not read", () => {
    expect(parseFrame("not json")).toBeNull();
    expect(parseFrame(JSON.stringify({ id: "1", type: "run.status", payload: {} }))).toBeNull();
    expect(
      parseFrame(JSON.stringify({ id: "1", type: "turn.delta", payload: { turn_id: "t" } }))
    ).toBeNull();
    expect(parseFrame(JSON.stringify({ id: "1", type: "stream.reset", payload: {} }))).toEqual({
      id: "1",
      type: "stream.reset",
    });
  });
});

describe("ConversationTranscript", () => {
  it("joins chunks in sequence order, and applies a replayed frame once", () => {
    const t = new ConversationTranscript("c1");
    t.apply(
      frame(1, "message", {
        message_id: "m1",
        turn_id: "t1",
        position: 1,
        role: "user",
        content: "q",
      })
    );
    expect(t.apply(frame(3, "turn.delta", { turn_id: "t1", seq: 2, text: "world" })).kind).toBe(
      "delta"
    );
    expect(t.apply(frame(2, "turn.delta", { turn_id: "t1", seq: 1, text: "hello " })).kind).toBe(
      "none"
    );
    // Frame 2 arrived after 3 on the wire, so it is a replay the transcript has passed.
    const fresh = new ConversationTranscript("c1");
    fresh.apply(
      frame(1, "message", {
        message_id: "m1",
        turn_id: "t1",
        position: 1,
        role: "user",
        content: "q",
      })
    );
    fresh.apply(frame(2, "turn.delta", { turn_id: "t1", seq: 2, text: "world" }));
    expect(
      fresh.apply(frame(3, "turn.delta", { turn_id: "t1", seq: 1, text: "hello " })).kind
    ).toBe("changed");
    expect(fresh.replyText("t1")).toBe("hello world");
    expect(
      fresh.apply(frame(3, "turn.delta", { turn_id: "t1", seq: 1, text: "hello " })).kind
    ).toBe("none");
  });

  it("lets the stored reply replace the streamed chunks", () => {
    const t = new ConversationTranscript("c1");
    t.apply(frame(1, "turn.delta", { turn_id: "t1", seq: 1, text: "partial" }));
    t.apply(
      frame(2, "message", {
        message_id: "m2",
        turn_id: "t1",
        position: 2,
        role: "assistant",
        content: "full reply",
      })
    );
    expect(t.replyText("t1")).toBe("full reply");
    expect(t.apply(frame(3, "turn.delta", { turn_id: "t1", seq: 2, text: "more" })).kind).toBe(
      "none"
    );
  });

  it("never reopens a terminal turn on a replayed earlier state", () => {
    const t = new ConversationTranscript("c1");
    t.apply(frame(5, "turn.state", { turn_id: "t1", state: "completed" }));
    t.apply({
      id: "",
      type: "turn.state",
      payload: {
        turn_id: "t1",
        state: "queued",
        failure_reason: null,
        failure_detail: null,
        target_workspace_id: null,
        operator_account_id: null,
        execution_profile: null,
      },
    });
    expect(t.getTurn("t1")!.state).toBe("completed");
    expect(t.inFlightTurns()).toEqual([]);
  });

  it("purges a redacted turn's text, chunks and messages", () => {
    const t = new ConversationTranscript("c1");
    t.apply(
      frame(1, "message", {
        message_id: "m1",
        turn_id: "t1",
        position: 1,
        role: "user",
        content: "leak",
      })
    );
    t.apply(frame(2, "turn.delta", { turn_id: "t1", seq: 1, text: "leaked reply" }));
    t.apply(frame(3, "message.redacted", { turn_id: "t1" }));
    expect(t.replyText("t1")).toBe("");
    expect(t.orderedMessages()[0]!.content).toBeNull();
    expect(t.orderedMessages()[0]!.redacted).toBe(true);
  });

  it("records card references so only a referenced card can be acted on", () => {
    const t = new ConversationTranscript("c1");
    t.apply(
      frame(1, "turn.reference", {
        turn_id: "t1",
        kind: "card",
        object_id: "dr_1",
        workspace_id: "w",
      })
    );
    t.apply(
      frame(2, "turn.reference", {
        turn_id: "t1",
        kind: "card",
        object_id: "dr_1",
        workspace_id: "w",
      })
    );
    expect(t.getTurn("t1")!.references).toHaveLength(1);
    expect(t.hasCardReference("dr_1")).toBe(true);
    expect(t.hasCardReference("dr_2")).toBe(false);
  });
});
