/**
 * Unit tests for decideProgressGatedEscalation (Issue #3851).
 *
 * This is the proximate fix for #3811's $112 burn: the unattended cost-ceiling
 * / per-stage budget auto-escalation used to double the limit UNCONDITIONALLY
 * (gated only on the escalation count). It is now gated on PRODUCTIVE progress
 * (commits / new-file writes / phase markers / CI progress) since the last
 * escalation:
 *   - flat progress  → churn → do NOT escalate (stop + save work)
 *   - healthy progress → escalate as before
 */

import { describe, it, expect } from "vitest";
import { decideProgressGatedEscalation } from "../../src/services/HeadlessOrchestrator";

describe("decideProgressGatedEscalation (Issue #3851)", () => {
  it("permits the FIRST escalation (no prior baseline) regardless of progress", () => {
    // lastSnapshot < 0 means "never escalated yet" — a single ceiling hit on
    // otherwise-healthy work must not be blocked.
    expect(decideProgressGatedEscalation(-1, 0)).toEqual({
      escalate: true,
      current: 0,
      delta: 0,
    });
    expect(decideProgressGatedEscalation(-1, 7)).toEqual({
      escalate: true,
      current: 7,
      delta: 7,
    });
  });

  it("ESCALATES when productive progress advanced since the last escalation", () => {
    // 5 productive signals at last escalation, 9 now → +4 → healthy, escalate.
    const result = decideProgressGatedEscalation(5, 9);
    expect(result.escalate).toBe(true);
    expect(result.delta).toBe(4);
    expect(result.current).toBe(9);
  });

  it("REFUSES to escalate when productive progress is flat (churn → stop)", () => {
    // This is the #3811 churn: 12 productive signals at last escalation, still
    // 12 now (only reads/searches/re-edits in between) → +0 → do NOT escalate.
    const result = decideProgressGatedEscalation(12, 12);
    expect(result.escalate).toBe(false);
    expect(result.delta).toBe(0);
  });

  it("REFUSES to escalate when the counter somehow regressed", () => {
    const result = decideProgressGatedEscalation(12, 10);
    expect(result.escalate).toBe(false);
    expect(result.delta).toBe(-2);
  });

  it("treats a non-finite current count as zero (defensive)", () => {
    const result = decideProgressGatedEscalation(3, Number.NaN);
    expect(result.current).toBe(0);
    expect(result.escalate).toBe(false);
  });

  it("models the #3811 escalation sequence: first escalate, then refuse on churn", () => {
    // First budget hit at $75 with no prior baseline → escalate.
    let last = -1;
    const e1 = decideProgressGatedEscalation(last, 3); // 3 productive so far
    expect(e1.escalate).toBe(true);
    last = e1.current; // snapshot = 3

    // Second hit at $150: the stage churned (re-read the same files, 0 new
    // commits/files) → still 3 productive → REFUSE (this is where #3811 burned).
    const e2 = decideProgressGatedEscalation(last, 3);
    expect(e2.escalate).toBe(false);
  });
});
