/**
 * OrchestratorEventDispatcher unit tests
 *
 * Verifies that the dispatcher:
 * - Invokes each callback type with correct arguments
 * - Handles undefined callbacks without throwing
 * - Isolates callback errors (wraps in try/catch)
 * - Returns correct defaults for async callbacks
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition Part 3
 */

import { describe, it, expect, vi } from "vitest";
import { OrchestratorEventDispatcher } from "../../../src/orchestrator/events/OrchestratorEventDispatcher";
import type { PipelineCallbacks } from "../../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../../src/utils/logger";

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function makeCallbacks(overrides?: Partial<PipelineCallbacks>): PipelineCallbacks {
  return {
    onStageStart: vi.fn(),
    onStageComplete: vi.fn(),
    onStageError: vi.fn(),
    onStageSkipped: vi.fn(),
    onStdout: vi.fn(),
    onStderr: vi.fn(),
    onPipelineComplete: vi.fn(),
    onApprovalRequired: vi.fn().mockResolvedValue(true),
    onBackwardTransitionConfirm: vi.fn().mockResolvedValue(false),
    onRoutingDecisionLoaded: vi.fn(),
    onPhaseStart: vi.fn(),
    onPhaseComplete: vi.fn(),
    onToolCall: vi.fn(),
    onStallWarningClear: vi.fn(),
    onBacktrackTriggered: vi.fn(),
    onBacktrackBlocked: vi.fn(),
    onModelEscalated: vi.fn(),
    onEscalationBlocked: vi.fn(),
    onProactiveEscalation: vi.fn(),
    onHealthPoliciesApplied: vi.fn(),
    onEarlyExit: vi.fn(),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Stage lifecycle
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — stage lifecycle", () => {
  it("onStageStart invokes the callback with stage", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onStageStart("feature-dev");
    expect(cbs.onStageStart).toHaveBeenCalledWith("feature-dev");
  });

  it("onStageComplete passes stage and result", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    const result = { success: true, stage: "feature-dev" as const, durationMs: 100 };
    d.onStageComplete("feature-dev", result);
    expect(cbs.onStageComplete).toHaveBeenCalledWith("feature-dev", result);
  });

  it("onStageError passes stage and error", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    const err = new Error("boom");
    d.onStageError("feature-dev", err);
    expect(cbs.onStageError).toHaveBeenCalledWith("feature-dev", err);
  });

  it("onStageSkipped passes stage and reason", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onStageSkipped("pr-merge", "routing");
    expect(cbs.onStageSkipped).toHaveBeenCalledWith("pr-merge", "routing");
  });
});

// ---------------------------------------------------------------------------
// I/O streams
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — I/O streams", () => {
  it("onStdout passes stage and data", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onStdout("feature-dev", "hello\n");
    expect(cbs.onStdout).toHaveBeenCalledWith("feature-dev", "hello\n");
  });

  it("onStderr passes stage and data", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onStderr("feature-dev", "err\n");
    expect(cbs.onStderr).toHaveBeenCalledWith("feature-dev", "err\n");
  });
});

// ---------------------------------------------------------------------------
// Pipeline completion
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — pipeline completion", () => {
  it("onPipelineComplete passes result", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    const result = { success: true, completedStages: [], skippedStages: [], totalDurationMs: 0 };
    d.onPipelineComplete(result as any);
    expect(cbs.onPipelineComplete).toHaveBeenCalledWith(result);
  });
});

// ---------------------------------------------------------------------------
// Async approval / control flow
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — async callbacks", () => {
  it("onApprovalRequired returns true when no callback registered", async () => {
    const d = new OrchestratorEventDispatcher(undefined, makeLogger());
    expect(await d.onApprovalRequired("feature-dev")).toBe(true);
  });

  it("onApprovalRequired awaits and returns callback result", async () => {
    const cbs = makeCallbacks({ onApprovalRequired: vi.fn().mockResolvedValue(false) });
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    expect(await d.onApprovalRequired("feature-dev")).toBe(false);
    expect(cbs.onApprovalRequired).toHaveBeenCalledWith("feature-dev");
  });

  it("onApprovalRequired returns true on callback error (auto-approve-safe)", async () => {
    const cbs = makeCallbacks({
      onApprovalRequired: vi.fn().mockRejectedValue(new Error("UI crashed")),
    });
    const logger = makeLogger();
    const d = new OrchestratorEventDispatcher(cbs, logger);
    expect(await d.onApprovalRequired("feature-dev")).toBe(true);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("onBackwardTransitionConfirm returns false when no callback", async () => {
    const d = new OrchestratorEventDispatcher(undefined, makeLogger());
    expect(await d.onBackwardTransitionConfirm("feature-dev", "msg")).toBe(false);
  });

  it("onBackwardTransitionConfirm passes stage and message to callback", async () => {
    const cbs = makeCallbacks({
      onBackwardTransitionConfirm: vi.fn().mockResolvedValue(true),
    });
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    expect(await d.onBackwardTransitionConfirm("feature-dev", "Confirm?")).toBe(true);
    expect(cbs.onBackwardTransitionConfirm).toHaveBeenCalledWith("feature-dev", "Confirm?");
  });
});

// ---------------------------------------------------------------------------
// Null-safety: undefined callbacks
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — null-safety", () => {
  it("does not throw when callbacks is undefined", () => {
    const d = new OrchestratorEventDispatcher(undefined, makeLogger());
    expect(() => d.onStageStart("feature-dev")).not.toThrow();
    expect(() =>
      d.onStageComplete("feature-dev", { success: true, stage: "feature-dev", durationMs: 0 })
    ).not.toThrow();
    expect(() => d.onStageError("feature-dev", new Error("x"))).not.toThrow();
    expect(() => d.onStageSkipped("feature-dev", "reason")).not.toThrow();
    expect(() => d.onStdout("feature-dev", "data")).not.toThrow();
    expect(() => d.onStderr("feature-dev", "data")).not.toThrow();
    expect(() => d.onRoutingDecisionLoaded({} as any)).not.toThrow();
    expect(() => d.onToolCall("feature-dev", { tool: "Read", target: "", args: {} })).not.toThrow();
    expect(() => d.onStallWarningClear("feature-dev")).not.toThrow();
    expect(() => d.onEarlyExit(42, "reason")).not.toThrow();
  });

  it("does not throw when individual callback fields are missing", () => {
    const partial: PipelineCallbacks = { onStageStart: vi.fn() };
    const d = new OrchestratorEventDispatcher(partial, makeLogger());
    expect(() =>
      d.onStageComplete("feature-dev", { success: true, stage: "feature-dev", durationMs: 0 })
    ).not.toThrow();
    expect(() => d.onStageError("feature-dev", new Error("x"))).not.toThrow();
    expect(() => d.onPipelineComplete({} as any)).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Error isolation
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — error isolation", () => {
  it("logs a warning and does not throw when a sync callback throws", () => {
    const cbs = makeCallbacks({
      onStageStart: vi.fn().mockImplementation(() => {
        throw new Error("callback crashed");
      }),
    });
    const logger = makeLogger();
    const d = new OrchestratorEventDispatcher(cbs, logger);

    expect(() => d.onStageStart("feature-dev")).not.toThrow();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("onStageStart"),
      expect.objectContaining({ err: "callback crashed" })
    );
  });

  it("includes stage in warning context when available", () => {
    const cbs = makeCallbacks({
      onStageComplete: vi.fn().mockImplementation(() => {
        throw new Error("boom");
      }),
    });
    const logger = makeLogger();
    const d = new OrchestratorEventDispatcher(cbs, logger);
    d.onStageComplete("pr-create", { success: true, stage: "pr-create", durationMs: 0 });
    expect(logger.warn).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ stage: "pr-create" })
    );
  });
});

// ---------------------------------------------------------------------------
// Phase events
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — phase events", () => {
  it("onPhaseStart passes all arguments", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onPhaseStart("feature-dev", "Research", 0, 3);
    expect(cbs.onPhaseStart).toHaveBeenCalledWith("feature-dev", "Research", 0, 3);
  });

  it("onPhaseComplete passes all arguments including durationMs", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onPhaseComplete("feature-dev", "Research", 0, 3, 5000);
    expect(cbs.onPhaseComplete).toHaveBeenCalledWith("feature-dev", "Research", 0, 3, 5000);
  });
});

// ---------------------------------------------------------------------------
// Backtrack / escalation events
// ---------------------------------------------------------------------------

describe("OrchestratorEventDispatcher — backtrack and escalation", () => {
  it("onBacktrackTriggered passes record", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    const rec = { from: "feature-dev", to: "feature-planning" } as any;
    d.onBacktrackTriggered(rec);
    expect(cbs.onBacktrackTriggered).toHaveBeenCalledWith(rec);
  });

  it("onModelEscalated passes record", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    const rec = { from: "claude-haiku", to: "claude-sonnet" } as any;
    d.onModelEscalated(rec);
    expect(cbs.onModelEscalated).toHaveBeenCalledWith(rec);
  });

  it("onEarlyExit passes issueNumber and reason", () => {
    const cbs = makeCallbacks();
    const d = new OrchestratorEventDispatcher(cbs, makeLogger());
    d.onEarlyExit(42, "already-resolved");
    expect(cbs.onEarlyExit).toHaveBeenCalledWith(42, "already-resolved");
  });
});
