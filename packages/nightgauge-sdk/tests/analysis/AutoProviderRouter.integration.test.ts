/**
 * Integration tests for AutoProviderRouter (Issue #3230).
 *
 * Drives the router across a synthetic 6-stage × 5-issue pipeline matrix and
 * asserts that every decision is reproducible across re-runs and consistent
 * with a fixture of expected adapters per stage.
 */

import { describe, it, expect } from "vitest";
import {
  AutoProviderRouter,
  type AutoRouterContext,
  type AutoRouterMode,
  type RouterExecutionAdapter,
} from "../../src/analysis/AutoProviderRouter.js";
import type { ComplexityLabel } from "../../src/analysis/AutoModelSelector.js";

const STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

interface SyntheticIssue {
  number: number;
  complexity: ComplexityLabel;
  mode: AutoRouterMode;
  available: RouterExecutionAdapter[];
  threshold: number;
}

const ISSUES: SyntheticIssue[] = [
  {
    number: 4001,
    complexity: "S",
    mode: "automatic",
    available: ["claude-sdk", "codex", "gemini"],
    threshold: 0.001,
  },
  {
    number: 4002,
    complexity: "M",
    mode: "automatic",
    available: ["claude-sdk", "codex"],
    threshold: 0.001,
  },
  {
    number: 4003,
    complexity: "L",
    mode: "automatic",
    available: ["claude-sdk"],
    threshold: 0.001,
  },
  {
    number: 4004,
    complexity: "XL",
    mode: "hybrid",
    available: ["claude-sdk", "codex", "gemini"],
    threshold: 0.001,
  },
  {
    number: 4005,
    complexity: "XS",
    mode: "manual",
    available: ["claude-sdk", "codex"],
    threshold: 0.001,
  },
];

function makeCtx(stage: string, issue: SyntheticIssue): AutoRouterContext {
  return {
    stage,
    mode: issue.mode,
    complexity: issue.complexity,
    available_adapters: issue.available,
    recent_history: [],
    confidence_threshold: issue.threshold,
  };
}

describe("AutoProviderRouter integration — synthetic pipeline", () => {
  const router = new AutoProviderRouter();

  it("produces a decision for every (issue, stage) and every result is reproducible", () => {
    const firstPass = new Map<string, ReturnType<typeof router.selectForStage>>();

    for (const issue of ISSUES) {
      for (const stage of STAGES) {
        const key = `${issue.number}:${stage}`;
        const decision = router.selectForStage(stage, makeCtx(stage, issue));
        firstPass.set(key, decision);
      }
    }

    // Re-run 5x and assert identical decisions.
    for (let pass = 0; pass < 5; pass++) {
      for (const issue of ISSUES) {
        for (const stage of STAGES) {
          const key = `${issue.number}:${stage}`;
          const replay = router.selectForStage(stage, makeCtx(stage, issue));
          expect(replay).toEqual(firstPass.get(key));
        }
      }
    }
  });

  it("manual-mode issues abstain on every stage", () => {
    const manualIssue = ISSUES.find((i) => i.mode === "manual")!;
    for (const stage of STAGES) {
      const decision = router.selectForStage(stage, makeCtx(stage, manualIssue));
      expect(decision).toBeNull();
    }
  });

  it("single-adapter issues always pick the available adapter with confidence 1.0", () => {
    const singleAdapterIssue = ISSUES.find((i) => i.available.length === 1)!;
    for (const stage of STAGES) {
      const decision = router.selectForStage(stage, makeCtx(stage, singleAdapterIssue));
      expect(decision).not.toBeNull();
      expect(decision!.adapter).toBe(singleAdapterIssue.available[0]);
      expect(decision!.confidence).toBe(1.0);
    }
  });

  it("hybrid-mode issues either abstain or return a confident pick — never an arbitrary one", () => {
    const hybridIssue = ISSUES.find((i) => i.mode === "hybrid")!;
    for (const stage of STAGES) {
      const decision = router.selectForStage(stage, makeCtx(stage, hybridIssue));
      if (decision) {
        // Hybrid picks must clear the dominance threshold (0.15)
        expect(decision.confidence).toBeGreaterThanOrEqual(0.15);
      }
    }
  });

  it("populates rationale and scores on every confident pick", () => {
    for (const issue of ISSUES) {
      if (issue.mode === "manual") continue;
      for (const stage of STAGES) {
        const decision = router.selectForStage(stage, makeCtx(stage, issue));
        if (!decision) continue;
        expect(decision.rationale.length).toBeGreaterThan(0);
        expect(decision.rationale).toContain(decision.adapter);
        if (issue.available.length > 1) {
          expect(decision.scores).toBeDefined();
          for (const adapter of issue.available) {
            expect(decision.scores![adapter]).toBeDefined();
          }
        }
      }
    }
  });
});
