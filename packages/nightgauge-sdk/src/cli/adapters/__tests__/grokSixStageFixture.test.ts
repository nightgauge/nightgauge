/**
 * Fixture stand-in for a six-stage Grok run (#528).
 *
 * A live subscription matrix is still required before calling the adapter
 * more than experimental. This pins the stream contract each stage would
 * produce so token/cost/quota handling cannot regress silently.
 */
import { describe, expect, it } from "vitest";
import { summarizeGrokStream } from "../grokStream.js";

const STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

function stageStream(stage: string, tokens: { in: number; out: number }): string {
  return [
    `{"type":"text","data":"stage ${stage} ok"}`,
    `{"type":"end","sessionId":"${stage}-sess","model":"grok-4.6","usage":{"input_tokens":${tokens.in},"output_tokens":${tokens.out},"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"reasoning_tokens":0}}`,
  ].join("\n");
}

describe("grok six-stage stream fixtures", () => {
  it("records tokens for every pipeline stage shape", () => {
    const totals = { in: 0, out: 0 };
    for (const [i, stage] of STAGES.entries()) {
      const s = summarizeGrokStream(stageStream(stage, { in: 100 + i, out: 20 + i }));
      expect(s.displayText).toContain(stage);
      expect(s.sessionId).toBe(`${stage}-sess`);
      expect(s.usage.input_tokens).toBe(100 + i);
      expect(s.usage.output_tokens).toBe(20 + i);
      expect(s.totalCostUsd).toBeUndefined();
      totals.in += s.usage.input_tokens;
      totals.out += s.usage.output_tokens;
    }
    expect(totals.in).toBe(615);
    expect(totals.out).toBe(135);
  });
});
