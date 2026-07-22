/**
 * Session Resume — StageExecutor and PipelineOrchestrator integration tests
 *
 * Covers Issue #1659: Integrate Codex session resume with git context preservation.
 *
 * Tests:
 * - StageExecutor.getLastSessionId() returns session ID from result message
 * - StageExecutor.getLastSessionId() returns null when result has no session_id
 * - StageExecutor passes resumeSessionId through to queryFn
 * - PipelineOrchestrator captures session IDs after each stage
 * - PipelineOrchestrator passes session ID on backtrack retry
 * - PipelineOrchestrator does NOT pass session ID on first (non-retry) attempt
 * - stageSessionIds cleared at start of each run()
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { StageExecutor, type SDKQueryOptions } from "../../src/orchestrator/StageExecutor.js";
import {
  PipelineOrchestrator,
  type PipelineConfig,
} from "../../src/orchestrator/PipelineOrchestrator.js";
import { EventBus, PipelineRunEmitter, type PipelineStage } from "../../src/events/EventBus.js";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";
import {
  createMockQuery,
  createMockResult,
  createMockText,
  createMockInit,
} from "../mocks/agent-sdk.js";
import type { SDKMessage } from "../../src/orchestrator/StageExecutor.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a mock result message with an embedded session_id. */
function createMockResultWithSession(sessionId: string) {
  return {
    ...createMockResult(),
    session_id: sessionId,
  };
}

/** Create a query function that yields a result with a session_id field. */
function createQueryWithSession(sessionId: string) {
  return async function* mockQueryWithSession(): AsyncGenerator<SDKMessage> {
    yield createMockInit();
    yield createMockText("Stage completed.");
    yield createMockResultWithSession(sessionId) as unknown as SDKMessage;
  };
}

/** Create a query function that yields a result WITHOUT a session_id field. */
function createQueryWithoutSession() {
  return async function* mockQueryNoSession(): AsyncGenerator<SDKMessage> {
    yield createMockInit();
    yield createMockText("Stage completed.");
    yield createMockResult() as unknown as SDKMessage;
  };
}

/** Shape of a feedback signal for backtrack tests. */
type FeedbackSignal = {
  signal_type: string;
  emitted_by_stage: string;
  backtrack_target_stage: string | null;
  rationale: string;
  evidence: string[];
  severity: string;
};

function blockingPlanSignal(): FeedbackSignal {
  return {
    signal_type: "PLAN_REVISION_NEEDED",
    emitted_by_stage: "feature-dev",
    backtrack_target_stage: "feature-planning",
    rationale: "Plan missing API specs",
    evidence: ["No REST endpoints defined"],
    severity: "blocking",
  };
}

// ---------------------------------------------------------------------------
// StageExecutor — session ID tracking
// ---------------------------------------------------------------------------

describe("StageExecutor — session ID tracking (Issue #1659)", () => {
  let emitter: PipelineRunEmitter;
  let tokenTracker: TokenTracker;

  beforeEach(() => {
    emitter = new PipelineRunEmitter(new EventBus(), 42);
    tokenTracker = new TokenTracker();
  });

  it("getLastSessionId() returns null before any stage has run", () => {
    const executor = new StageExecutor(tokenTracker, emitter, createMockQuery());

    expect(executor.getLastSessionId()).toBeNull();
  });

  it("getLastSessionId() returns null when result message has no session_id", async () => {
    const executor = new StageExecutor(tokenTracker, emitter, createQueryWithoutSession());

    for await (const _ of executor.execute({
      stage: "issue-pickup",
      issueNumber: 42,
      prompt: "test",
    })) {
      /* consume */
    }

    expect(executor.getLastSessionId()).toBeNull();
  });

  it("getLastSessionId() returns session ID captured from result message", async () => {
    const executor = new StageExecutor(
      tokenTracker,
      emitter,
      createQueryWithSession("captured-session-id")
    );

    for await (const _ of executor.execute({
      stage: "issue-pickup",
      issueNumber: 42,
      prompt: "test",
    })) {
      /* consume */
    }

    expect(executor.getLastSessionId()).toBe("captured-session-id");
  });

  it("getLastSessionId() is updated after each stage execution", async () => {
    const executor = new StageExecutor(
      tokenTracker,
      emitter,
      createQueryWithSession("first-session")
    );

    for await (const _ of executor.execute({
      stage: "issue-pickup",
      issueNumber: 42,
      prompt: "test",
    })) {
      /* consume */
    }

    expect(executor.getLastSessionId()).toBe("first-session");

    // Replace the query function by creating a new executor
    // (StageExecutor doesn't support dynamic query replacement, so verify
    // that a second executor starts fresh)
    const executor2 = new StageExecutor(
      tokenTracker,
      emitter,
      createQueryWithSession("second-session")
    );

    for await (const _ of executor2.execute({
      stage: "feature-dev",
      issueNumber: 42,
      prompt: "test",
    })) {
      /* consume */
    }

    expect(executor2.getLastSessionId()).toBe("second-session");
    // First executor is unchanged
    expect(executor.getLastSessionId()).toBe("first-session");
  });

  it("passes resumeSessionId through to the query function", async () => {
    const capturedOptions: SDKQueryOptions[] = [];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedOptions.push(opts);
      yield createMockInit();
      yield createMockResult() as unknown as SDKMessage;
    };

    const executor = new StageExecutor(tokenTracker, emitter, trackingQuery);

    for await (const _ of executor.execute({
      stage: "feature-dev",
      issueNumber: 42,
      prompt: "test",
      resumeSessionId: "prior-session-id",
    })) {
      /* consume */
    }

    expect(capturedOptions).toHaveLength(1);
    expect(capturedOptions[0].options?.resumeSessionId).toBe("prior-session-id");
  });

  it("passes undefined resumeSessionId when not provided in options", async () => {
    const capturedOptions: SDKQueryOptions[] = [];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedOptions.push(opts);
      yield createMockResult() as unknown as SDKMessage;
    };

    const executor = new StageExecutor(tokenTracker, emitter, trackingQuery);

    for await (const _ of executor.execute({
      stage: "feature-dev",
      issueNumber: 42,
      prompt: "test",
      // resumeSessionId not provided
    })) {
      /* consume */
    }

    expect(capturedOptions[0].options?.resumeSessionId).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// PipelineOrchestrator — session ID capture and retry wiring
// ---------------------------------------------------------------------------

describe("PipelineOrchestrator — session resume on backtrack retry (Issue #1659)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  /**
   * Create an orchestrator with a two-stage pipeline using a query that
   * returns session IDs on result messages.
   */
  function makeOrchestratorWithSession(sessionId: string, overrides?: Partial<PipelineConfig>) {
    return new PipelineOrchestrator(createQueryWithSession(sessionId), {
      stages: ["feature-planning", "feature-dev"],
      autoApprove: true,
      maxBacktracks: 1,
      ...overrides,
    });
  }

  it("does NOT pass resumeSessionId on first (non-retry) attempt", async () => {
    const capturedResumeIds: Array<string | undefined> = [];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedResumeIds.push(opts.options?.resumeSessionId);
      yield createMockInit();
      yield createMockResult() as unknown as SDKMessage;
    };

    const orchestrator = new PipelineOrchestrator(trackingQuery, {
      stages: ["feature-planning", "feature-dev"],
      autoApprove: true,
    });

    await orchestrator.run(42);

    // Both stages run without resumeSessionId on the first pass
    expect(capturedResumeIds).toHaveLength(2);
    expect(capturedResumeIds[0]).toBeUndefined();
    expect(capturedResumeIds[1]).toBeUndefined();
  });

  it("captures session ID after each stage and passes it on backtrack retry", async () => {
    const capturedResumeIds: Array<string | undefined> = [];
    const stageCallOrder: string[] = [];

    let callCount = 0;
    const sessionIdPerCall = [
      "session-planning-1", // feature-planning (1st pass)
      "session-dev-1", // feature-dev (1st pass — triggers backtrack)
      "session-planning-2", // feature-planning (retry)
      "session-dev-2", // feature-dev (retry — no backtrack)
    ];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedResumeIds.push(opts.options?.resumeSessionId);
      const sessionId = sessionIdPerCall[callCount++] ?? "fallback-session";
      yield createMockInit();
      yield createMockResultWithSession(sessionId) as unknown as SDKMessage;
    };

    const orchestrator = new PipelineOrchestrator(trackingQuery, {
      stages: ["feature-planning", "feature-dev"],
      autoApprove: true,
      maxBacktracks: 1,
    });

    // Patch readFeedbackSignals to trigger backtrack on first feature-dev run only
    let devCallCount = 0;
    (orchestrator as any).readFeedbackSignals = vi.fn((stage: PipelineStage) => {
      if (stage !== "feature-dev") return [];
      devCallCount++;
      // Only trigger backtrack on the first feature-dev pass
      return devCallCount === 1 ? [blockingPlanSignal()] : [];
    });

    await orchestrator.run(42);

    // 4 calls: planning(1) → dev(1) [backtrack] → planning(2) → dev(2)
    expect(capturedResumeIds).toHaveLength(4);

    // First planning pass: no prior session
    expect(capturedResumeIds[0]).toBeUndefined();
    // First dev pass: no prior session (first time running this stage)
    expect(capturedResumeIds[1]).toBeUndefined();
    // Retry planning pass: receives session ID from first planning run
    expect(capturedResumeIds[2]).toBe("session-planning-1");
    // Retry dev pass: receives session ID from first dev run
    expect(capturedResumeIds[3]).toBe("session-dev-1");
  });

  it("stageSessionIds is cleared at the start of each run()", async () => {
    const capturedResumeIds: Array<string | undefined> = [];
    let callCount = 0;
    const sessionIdPerCall = [
      "run1-planning",
      "run1-dev", // First run
      "run2-planning",
      "run2-dev", // Second run (should not see run1 IDs)
    ];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedResumeIds.push(opts.options?.resumeSessionId);
      const sessionId = sessionIdPerCall[callCount++] ?? "fallback";
      yield createMockResultWithSession(sessionId) as unknown as SDKMessage;
    };

    const orchestrator = new PipelineOrchestrator(trackingQuery, {
      stages: ["feature-planning", "feature-dev"],
      autoApprove: true,
      maxBacktracks: 0, // No backtracks to keep it simple
    });

    await orchestrator.run(10);
    await orchestrator.run(10);

    // Neither run should see session IDs from the other run (stageSessionIds cleared)
    expect(capturedResumeIds[0]).toBeUndefined(); // run1 planning
    expect(capturedResumeIds[1]).toBeUndefined(); // run1 dev
    expect(capturedResumeIds[2]).toBeUndefined(); // run2 planning (cleared between runs)
    expect(capturedResumeIds[3]).toBeUndefined(); // run2 dev (cleared between runs)
  });

  it("runStage() accepts and passes resumeSessionId option", async () => {
    const capturedResumeIds: Array<string | undefined> = [];

    const trackingQuery = async function* (opts: SDKQueryOptions): AsyncGenerator<SDKMessage> {
      capturedResumeIds.push(opts.options?.resumeSessionId);
      yield createMockResult() as unknown as SDKMessage;
    };

    const orchestrator = new PipelineOrchestrator(trackingQuery, {
      stages: ["feature-dev"],
      autoApprove: true,
    });

    await orchestrator.runStage("feature-dev", 42, {
      resumeSessionId: "explicit-session-id",
    });

    expect(capturedResumeIds).toHaveLength(1);
    expect(capturedResumeIds[0]).toBe("explicit-session-id");
  });

  it("runStage() works without resumeSessionId option (backwards compatible)", async () => {
    const orchestrator = new PipelineOrchestrator(createMockQuery(), {
      stages: ["feature-dev"],
      autoApprove: true,
    });

    // Should not throw when called without options (original signature)
    const result = await orchestrator.runStage("feature-dev", 42);
    expect(result.success).toBe(true);
  });
});
