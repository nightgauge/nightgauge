import { describe, expect, it } from "vitest";
import { reconcileAcceptanceCriteria } from "../reconcile.js";
import { ACReconcileContextSchema } from "../../context/schemas/ac-reconcile.js";
import type { Classification, RuleEvaluator } from "../types.js";

/**
 * Build a fake rule that always classifies the same way and matches all input.
 * Used to drive the reconciler through every aggregate-status branch without
 * depending on the real rule library or filesystem.
 */
function fakeRule(classification: Classification): RuleEvaluator {
  return {
    name: `fake-${classification}`,
    applies: () => ({}),
    evaluate: async () => ({
      classification,
      reason: `forced ${classification}`,
      evidence: classification === "satisfied" ? ["evidence.txt"] : [],
    }),
  };
}

const baseOpts = {
  workdir: "/tmp/anywhere",
  issueNumber: 1,
  mainSha: "abc1234",
  evaluatedAt: "2026-04-25T00:00:00.000Z",
};

describe("reconcileAcceptanceCriteria", () => {
  it("aggregates to no-acs-detected when body has no checkboxes", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "no checkboxes here",
    });
    expect(r.aggregate_status).toBe("no-acs-detected");
    expect(r.suggested_route.approach).toBe("standard");
    expect(r.acceptance_criteria).toEqual([]);
  });

  it("aggregates to all-satisfied when every AC is satisfied", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] one\n- [ ] two\n- [ ] three",
      rules: [fakeRule("satisfied")],
    });
    expect(r.aggregate_status).toBe("all-satisfied");
    expect(r.suggested_route.approach).toBe("verify-and-close");
    expect(r.suggested_route.focus_acs).toEqual([]);
    expect(r.acceptance_criteria).toHaveLength(3);
  });

  it("aggregates to mostly-satisfied at the 80% threshold with no unsatisfied", async () => {
    // 4 satisfied, 1 undetectable => 4/5 = 80% — meets threshold.
    const acRules: RuleEvaluator[] = [
      {
        name: "byindex",
        applies: (t: string) => ({ t }),
        async evaluate(ctx) {
          const which = ctx.ac.index < 4 ? "satisfied" : "undetectable";
          return { classification: which, reason: "", evidence: [] };
        },
      },
    ];
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b\n- [ ] c\n- [ ] d\n- [ ] e",
      rules: acRules,
    });
    expect(r.aggregate_status).toBe("mostly-satisfied");
    expect(r.suggested_route.approach).toBe("narrow-scope");
    expect(r.suggested_route.focus_acs).toEqual([4]);
  });

  it("aggregates to partial when satisfied count is below threshold", async () => {
    const acRules: RuleEvaluator[] = [
      {
        name: "byindex",
        applies: (t: string) => ({ t }),
        async evaluate(ctx) {
          const which = ctx.ac.index === 0 ? "satisfied" : "unsatisfied";
          return { classification: which, reason: "", evidence: [] };
        },
      },
    ];
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b\n- [ ] c",
      rules: acRules,
    });
    expect(r.aggregate_status).toBe("partial");
    expect(r.suggested_route.approach).toBe("standard");
    expect(r.suggested_route.focus_acs).toEqual([1, 2]);
  });

  it("aggregates to unsatisfied when all ACs are unsatisfied", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b",
      rules: [fakeRule("unsatisfied")],
    });
    expect(r.aggregate_status).toBe("unsatisfied");
    expect(r.suggested_route.approach).toBe("standard");
    expect(r.suggested_route.focus_acs).toEqual([0, 1]);
  });

  it("aggregates to undetectable when all ACs are undetectable", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b",
      rules: [fakeRule("undetectable")],
    });
    expect(r.aggregate_status).toBe("undetectable");
    expect(r.suggested_route.approach).toBe("standard");
  });

  it("classifies unmatched ACs as undetectable with rule_applied=null", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b",
      rules: [], // no rules => nothing applies
    });
    expect(r.acceptance_criteria.every((c) => c.rule_applied === null)).toBe(true);
    expect(r.acceptance_criteria.every((c) => c.classification === "undetectable")).toBe(true);
    expect(r.aggregate_status).toBe("undetectable");
  });

  it("emits a Zod-valid ACReconcileContext", async () => {
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [x] one",
      rules: [fakeRule("satisfied")],
    });
    expect(() => ACReconcileContextSchema.parse(r)).not.toThrow();
  });

  it("falls back to partial when satisfied is non-zero but unsatisfied is also present", async () => {
    // 4 satisfied, 1 unsatisfied (no undetectable) — even at 80% ratio, presence
    // of any unsatisfied prevents `mostly-satisfied`.
    const acRules: RuleEvaluator[] = [
      {
        name: "byindex",
        applies: (t: string) => ({ t }),
        async evaluate(ctx) {
          const which = ctx.ac.index < 4 ? "satisfied" : "unsatisfied";
          return { classification: which, reason: "", evidence: [] };
        },
      },
    ];
    const r = await reconcileAcceptanceCriteria({
      ...baseOpts,
      issueBody: "- [ ] a\n- [ ] b\n- [ ] c\n- [ ] d\n- [ ] e",
      rules: acRules,
    });
    expect(r.aggregate_status).toBe("partial");
  });
});
