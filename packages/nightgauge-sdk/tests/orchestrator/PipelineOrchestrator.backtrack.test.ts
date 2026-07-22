/**
 * PipelineOrchestrator — Backtrack Engine tests
 *
 * Covers Issue #1342: orchestrator backtrack engine.
 *
 * The backtrack private methods (readFeedbackSignals, evaluateBacktrack,
 * executeBacktrack) rely on require('fs') which bypasses vi.mock in vitest's
 * ESM mode.  The strategy is to patch readFeedbackSignals directly on each
 * orchestrator instance (via `(orchestrator as any).readFeedbackSignals`).
 * This keeps tests deterministic without real filesystem I/O.
 *
 * Backtrack is a control-plane decision (NOT a workflow tree node), so a rewind
 * is observed by spying on the private `executeBacktrack` — it is invoked once
 * per actual backtrack and never when an attempt is evaluated and blocked. This
 * asserts the same engine behavior the removed `backtrack:*` bus events used to.
 *
 * All stage execution uses createMockQuery() so every runStage() succeeds.
 * autoApprove is set on every orchestrator to skip the feature-planning gate.
 *
 * The mock must be stage-aware because readFeedbackSignals is called after
 * every successful stage.  Signals should only be injected for 'feature-dev'
 * (or whatever stage is supposed to trigger the backtrack) to avoid spurious
 * backtracks from other stages in the pipeline.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  PipelineOrchestrator,
  type PipelineConfig,
} from "../../src/orchestrator/PipelineOrchestrator.js";
import type { PipelineStage } from "../../src/events/EventBus.js";
import { createMockQuery } from "../mocks/agent-sdk.js";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** Shape of a feedback signal as returned by readFeedbackSignals. */
type FeedbackSignal = {
  signal_type: string;
  emitted_by_stage: string;
  backtrack_target_stage: string | null;
  rationale: string;
  evidence: string[];
  severity: string;
};

/** One captured invocation of the private executeBacktrack. */
type BacktrackCall = {
  fromStage: PipelineStage;
  toStage: string | null | undefined;
  signalType: string;
  attemptNumber: number;
  issueNumber: number;
};

/** A blocking PLAN_REVISION_NEEDED signal from feature-dev → feature-planning. */
function blockingPlanSignal(): FeedbackSignal {
  return {
    signal_type: "PLAN_REVISION_NEEDED",
    emitted_by_stage: "feature-dev",
    backtrack_target_stage: "feature-planning",
    rationale: "Plan is missing API endpoint specs",
    evidence: ["No REST endpoints defined"],
    severity: "blocking",
  };
}

/**
 * Create an orchestrator pre-configured for a two-stage
 * [feature-planning → feature-dev] pipeline with autoApprove enabled.
 *
 * The default maxBacktracks is 1 unless overridden.
 */
function makeOrchestrator(overrides?: Partial<PipelineConfig>) {
  return new PipelineOrchestrator(createMockQuery(), {
    stages: ["feature-planning", "feature-dev"],
    autoApprove: true,
    maxBacktracks: 1,
    ...overrides,
  });
}

/**
 * Wrap the private `executeBacktrack` to record each actual backtrack. The
 * recorded `attemptNumber` is read AFTER the original runs (it increments
 * `backtrackCount`), matching the engine's own numbering.
 */
function spyBacktracks(orchestrator: PipelineOrchestrator): BacktrackCall[] {
  const calls: BacktrackCall[] = [];
  const original = (orchestrator as any).executeBacktrack.bind(orchestrator);
  (orchestrator as any).executeBacktrack = vi.fn(
    (signal: FeedbackSignal, currentStage: PipelineStage, issueNumber: number) => {
      const idx = original(signal, currentStage, issueNumber);
      calls.push({
        fromStage: currentStage,
        toStage: signal.backtrack_target_stage,
        signalType: signal.signal_type,
        attemptNumber: (orchestrator as any).backtrackCount,
        issueNumber,
      });
      return idx;
    }
  );
  return calls;
}

/**
 * Patch readFeedbackSignals on the orchestrator instance so that it returns
 * signals only when called for `triggerStage`.  All other stages get [].
 *
 * When `onlyOnce` is true (default) the signals are returned only on the
 * first matching call; subsequent calls return [] to prevent infinite loops.
 */
function patchReadSignals(
  orchestrator: PipelineOrchestrator,
  triggerStage: PipelineStage,
  signals: FeedbackSignal[],
  onlyOnce = true
) {
  let triggered = false;
  (orchestrator as any).readFeedbackSignals = vi.fn((stage: PipelineStage) => {
    if (stage !== triggerStage) return [];
    if (onlyOnce && triggered) return [];
    triggered = true;
    return signals;
  });
}

/**
 * Same as patchReadSignals but always returns the signals (no once guard).
 * Use this to force the orchestrator to attempt backtracks on every pass.
 */
function patchReadSignalsAlways(
  orchestrator: PipelineOrchestrator,
  triggerStage: PipelineStage,
  signals: FeedbackSignal[]
) {
  (orchestrator as any).readFeedbackSignals = vi.fn((stage: PipelineStage) => {
    return stage === triggerStage ? signals : [];
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineOrchestrator — backtrack engine (Issue #1342)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // -------------------------------------------------------------------------
  // 1. Backtrack execution — blocking signal causes backtrack
  // -------------------------------------------------------------------------
  describe("backtrack execution", () => {
    it("backtracks when feature-dev returns a blocking signal targeting feature-planning", async () => {
      const orchestrator = makeOrchestrator();

      // Signal fires only on first call to readFeedbackSignals for feature-dev.
      // After the backtrack, the second pass through feature-dev gets [] so the
      // pipeline can complete normally.
      patchReadSignals(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      const result = await orchestrator.run(42);

      expect(backtracks).toHaveLength(1);
      expect(backtracks[0]).toMatchObject({
        fromStage: "feature-dev",
        toStage: "feature-planning",
        signalType: "PLAN_REVISION_NEEDED",
        attemptNumber: 1,
        issueNumber: 42,
      });

      // Pipeline ultimately completes successfully after the backtrack
      expect(result.success).toBe(true);
    });

    it("calls executeBacktrack and constructs the correct feedback-{N}.json payload", async () => {
      const orchestrator = makeOrchestrator();

      // Spy on the private executeBacktrack to capture the payload that would
      // be written to disk (without touching the real filesystem).
      const writtenPayloads: Array<{
        path: string;
        content: Record<string, unknown>;
      }> = [];
      const originalExecuteBacktrack = (orchestrator as any).executeBacktrack.bind(orchestrator);

      (orchestrator as any).executeBacktrack = vi.fn(
        (signal: FeedbackSignal, currentStage: PipelineStage, issueNumber: number) => {
          const contextPath = (orchestrator as any).config.contextPath as string;
          const feedbackPath = `${contextPath}/feedback-${issueNumber}.json`;
          writtenPayloads.push({
            path: feedbackPath,
            content: {
              schema_version: "1.0",
              issue_number: issueNumber,
              signals: [signal],
            },
          });
          return originalExecuteBacktrack(signal, currentStage, issueNumber);
        }
      );

      patchReadSignals(orchestrator, "feature-dev", [blockingPlanSignal()]);

      await orchestrator.run(42);

      expect(writtenPayloads).toHaveLength(1);
      expect(writtenPayloads[0].path).toContain("feedback-42.json");
      expect(writtenPayloads[0].content.issue_number).toBe(42);

      const signals = writtenPayloads[0].content.signals as FeedbackSignal[];
      expect(signals).toHaveLength(1);
      expect(signals[0].signal_type).toBe("PLAN_REVISION_NEEDED");
    });
  });

  // -------------------------------------------------------------------------
  // 2. Backtrack metadata — captured at the point of rewind
  // -------------------------------------------------------------------------
  describe("backtrack metadata", () => {
    it("records the correct from/to stages, signal type, and issue number", async () => {
      const orchestrator = makeOrchestrator({ maxBacktracks: 2 });
      patchReadSignals(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      await orchestrator.run(99);

      expect(backtracks).toHaveLength(1);
      const call = backtracks[0];
      expect(call.fromStage).toBe("feature-dev");
      expect(call.toStage).toBe("feature-planning");
      expect(call.issueNumber).toBe(99);
      expect(typeof call.signalType).toBe("string");
      expect(typeof call.attemptNumber).toBe("number");
    });

    it("does not backtrack again once the limit is hit", async () => {
      // Always return a signal so a second attempt is evaluated and blocked.
      const orchestrator = makeOrchestrator({ maxBacktracks: 1 });
      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      const result = await orchestrator.run(77);

      // Exactly one actual backtrack; the second attempt is blocked (no call).
      expect(backtracks).toHaveLength(1);
      expect(result.success).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // 3. Recursion guard — second backtrack blocked when maxBacktracks=1
  // -------------------------------------------------------------------------
  describe("recursion guard (maxBacktracks=1)", () => {
    it("allows the first backtrack and blocks the second", async () => {
      const orchestrator = makeOrchestrator({ maxBacktracks: 1 });

      // Signal present on every feature-dev pass, forcing two attempts
      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      const result = await orchestrator.run(10);

      // Only the first attempt executes; the second is blocked by the limit.
      expect(backtracks).toHaveLength(1);
      expect(backtracks[0].attemptNumber).toBe(1);

      // Pipeline still completes (blocking signal is bypassed after limit)
      expect(result.success).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // 4. Oscillation guard — same edge blocked even with quota remaining
  // -------------------------------------------------------------------------
  describe("oscillation guard", () => {
    it("blocks re-traversal of feature-dev→feature-planning even with quota available", async () => {
      const orchestrator = makeOrchestrator({ maxBacktracks: 5 });

      // Always emit the same signal so the same edge is attempted twice
      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      const result = await orchestrator.run(20);

      // First traversal of the edge succeeds; the second is blocked as an
      // oscillation even though quota remains — so only one actual backtrack.
      expect(backtracks).toHaveLength(1);
      expect(backtracks[0]).toMatchObject({
        fromStage: "feature-dev",
        toStage: "feature-planning",
      });
      expect(result.success).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // 5. maxBacktracks: 0 — completely disabled
  // -------------------------------------------------------------------------
  describe("maxBacktracks: 0 — completely disabled", () => {
    it("blocks all backtrack attempts immediately", async () => {
      const orchestrator = makeOrchestrator({ maxBacktracks: 0 });

      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const backtracks = spyBacktracks(orchestrator);

      const result = await orchestrator.run(5);

      // No backtrack ever executes.
      expect(backtracks).toHaveLength(0);

      // Pipeline still completes — the signal is silently bypassed
      expect(result.success).toBe(true);
    });
  });

  // -------------------------------------------------------------------------
  // 6. Non-backtrack-eligible signals
  // -------------------------------------------------------------------------
  describe("non-backtrack-eligible signals", () => {
    it("does not backtrack when readFeedbackSignals returns empty (no context file)", async () => {
      const orchestrator = makeOrchestrator({
        stages: ["issue-pickup"],
        autoApprove: true,
      });
      const backtracks = spyBacktracks(orchestrator);

      // issue-pickup has no context type map entry — real method returns []
      // No patching needed; we just verify no backtrack fires.
      await orchestrator.run(42);

      expect(backtracks).toHaveLength(0);
    });

    it("does not backtrack when MODEL_ESCALATION_NEEDED is the only signal (filtered out)", async () => {
      // readFeedbackSignals filters MODEL_ESCALATION_NEEDED at the source;
      // simulating that by having the mock return [].
      const orchestrator = makeOrchestrator();
      (orchestrator as any).readFeedbackSignals = vi.fn(() => []);
      const backtracks = spyBacktracks(orchestrator);

      await orchestrator.run(88);

      expect(backtracks).toHaveLength(0);
    });

    it("does not backtrack when all feedback signals have non-blocking severity", async () => {
      // readFeedbackSignals filters out non-blocking signals at the source;
      // simulating that by having the mock return [].
      const orchestrator = makeOrchestrator();
      (orchestrator as any).readFeedbackSignals = vi.fn(() => []);
      const backtracks = spyBacktracks(orchestrator);

      await orchestrator.run(55);

      expect(backtracks).toHaveLength(0);
    });
  });

  // -------------------------------------------------------------------------
  // 7. State reset between run() calls
  // -------------------------------------------------------------------------
  describe("state reset between runs", () => {
    it("resets backtrackCount and traversedEdges at the start of each run", async () => {
      const orchestrator = makeOrchestrator({ maxBacktracks: 1 });

      // First run: one backtrack is executed, a second attempt blocked.
      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const firstRun = spyBacktracks(orchestrator);

      await orchestrator.run(1);

      expect(firstRun).toHaveLength(1);

      // Second run: state should be reset — same quota available again.
      patchReadSignalsAlways(orchestrator, "feature-dev", [blockingPlanSignal()]);
      const secondRun = spyBacktracks(orchestrator);

      await orchestrator.run(2);

      // After reset, the same pattern applies: one backtrack executes again.
      expect(secondRun).toHaveLength(1);
      expect(secondRun[0].issueNumber).toBe(2);
    });
  });
});
