/**
 * completionTracking.failureGuard.test.ts
 *
 * Integration-style tests that verify the `currentRunHadFailure` guard
 * introduced in extension.ts (Issue #1502).
 *
 * The tests inline the same wiring logic that lives in `extension.ts` so they
 * run without importing the extension itself (which requires a full VSCode
 * host). This keeps the tests fast and isolated while still exercising the
 * exact guard behavior.
 *
 * @see Issue #1502 - Pipeline failures show as Completed in sidebar
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── minimal stubs ─────────────────────────────────────────────────────────────

interface MockCompletedIssuesService {
  addCompleted: ReturnType<typeof vi.fn>;
  addFailed: ReturnType<typeof vi.fn>;
  removeFromFailed: ReturnType<typeof vi.fn>;
}

function makeService(): MockCompletedIssuesService {
  return {
    addCompleted: vi.fn(),
    addFailed: vi.fn(),
    removeFromFailed: vi.fn(),
  };
}

// A minimal EventEmitter that mimics vscode.EventEmitter<T>
class SimpleEmitter<T> {
  private handlers: Array<(e: T) => void> = [];

  get event() {
    return (handler: (e: T) => void) => {
      this.handlers.push(handler);
    };
  }

  fire(e: T) {
    for (const h of this.handlers) h(e);
  }
}

// ── guard wiring (mirrors extension.ts exactly) ───────────────────────────────

/**
 * Wire the completion tracking guard identical to extension.ts.
 * Returns helpers to simulate pipeline events.
 */
function wireGuard(svc: MockCompletedIssuesService) {
  const stateEmitter = new SimpleEmitter<any | null>();
  const errorEmitter = new SimpleEmitter<{
    stage: string;
    issueNumber: number;
    error: string;
  }>();

  let lastPipelineState: any = null;
  let currentRunHadFailure = false;

  stateEmitter.event((state) => {
    if (lastPipelineState && !state) {
      if (!currentRunHadFailure) {
        svc.removeFromFailed(lastPipelineState.issue_number);
        svc.addCompleted(
          lastPipelineState.issue_number,
          lastPipelineState.title,
          lastPipelineState.branch
        );
      }
      currentRunHadFailure = false;
    } else if (!lastPipelineState && state) {
      currentRunHadFailure = false;
    }
    lastPipelineState = state;
  });

  errorEmitter.event(({ stage, issueNumber, error }) => {
    currentRunHadFailure = true;
    const state = lastPipelineState;
    if (state && state.issue_number === issueNumber) {
      svc.addFailed(issueNumber, state.title, state.branch, stage, error);
    } else {
      svc.addFailed(issueNumber, `Issue #${issueNumber}`, "", stage, error);
    }
  });

  function startRun(issueNumber: number, title: string, branch: string) {
    stateEmitter.fire({ issue_number: issueNumber, title, branch });
  }

  function endRun() {
    stateEmitter.fire(null);
  }

  function failStage(issueNumber: number, stage: string, error: string) {
    errorEmitter.fire({ stage, issueNumber, error });
  }

  return { startRun, endRun, failStage };
}

// ── tests ──────────────────────────────────────────────────────────────────────

describe("completion tracking guard (Issue #1502)", () => {
  let svc: MockCompletedIssuesService;

  beforeEach(() => {
    svc = makeService();
  });

  // ── Test 1: successful pipeline ──────────────────────────────────────────

  it("calls addCompleted (not addFailed) when no stage error fires", () => {
    const { startRun, endRun } = wireGuard(svc);

    startRun(42, "Add feature", "feat/42-add-feature");
    endRun();

    expect(svc.addCompleted).toHaveBeenCalledOnce();
    expect(svc.addCompleted).toHaveBeenCalledWith(42, "Add feature", "feat/42-add-feature");
    expect(svc.addFailed).not.toHaveBeenCalled();
  });

  // ── Test 2: failed pipeline ──────────────────────────────────────────────

  it("does NOT call addCompleted when onStageError fires before state cleared", () => {
    const { startRun, endRun, failStage } = wireGuard(svc);

    startRun(42, "Add feature", "feat/42-add-feature");
    failStage(42, "feature-planning", "Planning timed out");
    endRun();

    expect(svc.addFailed).toHaveBeenCalledOnce();
    expect(svc.addFailed).toHaveBeenCalledWith(
      42,
      "Add feature",
      "feat/42-add-feature",
      "feature-planning",
      "Planning timed out"
    );
    // The bug: without the guard this called addCompleted after the failure
    expect(svc.addCompleted).not.toHaveBeenCalled();
  });

  // ── Test 3: re-run after failure succeeds ────────────────────────────────

  it("calls addCompleted on subsequent successful run after a failed run", () => {
    const { startRun, endRun, failStage } = wireGuard(svc);

    // --- Run 1: fails ---
    startRun(42, "Add feature", "feat/42-add-feature");
    failStage(42, "pr-create", "PR creation error");
    endRun(); // state→null: flag was true, so addCompleted skipped, flag reset

    expect(svc.addCompleted).not.toHaveBeenCalled();

    // --- Run 2: succeeds ---
    startRun(42, "Add feature", "feat/42-add-feature"); // null→state: flag reset
    endRun(); // state→null: flag is false → addCompleted called

    expect(svc.addCompleted).toHaveBeenCalledOnce();
    expect(svc.addCompleted).toHaveBeenCalledWith(42, "Add feature", "feat/42-add-feature");
    expect(svc.removeFromFailed).toHaveBeenCalledWith(42);
  });

  // ── Test 4: batch — one fails, one succeeds ──────────────────────────────

  it("correctly classifies two sequential runs: first fails, second succeeds", () => {
    const { startRun, endRun, failStage } = wireGuard(svc);

    // --- Issue #10: fails ---
    startRun(10, "Issue ten", "feat/10-issue-ten");
    failStage(10, "feature-dev", "Compilation error");
    endRun(); // flag=true → skip addCompleted, reset flag

    // --- Issue #20: succeeds ---
    startRun(20, "Issue twenty", "feat/20-issue-twenty");
    endRun(); // flag=false → addCompleted

    // #10 should be in Failed only
    expect(svc.addFailed).toHaveBeenCalledOnce();
    expect(svc.addFailed.mock.calls[0][0]).toBe(10);

    // #20 should be in Completed only
    expect(svc.addCompleted).toHaveBeenCalledOnce();
    expect(svc.addCompleted.mock.calls[0][0]).toBe(20);

    // No cross-contamination: addCompleted never called for #10
    const completedIssueNumbers = svc.addCompleted.mock.calls.map((args: any[]) => args[0]);
    expect(completedIssueNumbers).not.toContain(10);
  });

  // ── Test 5: idempotent double-null (debounced watcher edge case) ──────────

  it("is idempotent when state→null fires twice (debounced file watcher)", () => {
    const { startRun, endRun, failStage } = wireGuard(svc);

    startRun(5, "Something", "feat/5");
    failStage(5, "feature-dev", "err");
    endRun(); // first null — flag true → skip addCompleted, reset flag

    // Second null (debounced file watcher fires again with null)
    // lastPipelineState is now null, so `if (lastPipelineState && !state)` is false
    endRun(); // should be a no-op

    expect(svc.addCompleted).not.toHaveBeenCalled();
    expect(svc.addFailed).toHaveBeenCalledOnce(); // only one addFailed
  });

  // ── Test 6: fallback path when state.issue_number doesn't match ──────────

  it("uses fallback title when lastPipelineState issue_number differs from error issueNumber", () => {
    const { startRun, endRun, failStage } = wireGuard(svc);

    startRun(99, "Right issue", "feat/99");
    // Error fires for a different issue number (unexpected edge case)
    failStage(77, "feature-dev", "err");
    endRun();

    expect(svc.addFailed).toHaveBeenCalledWith(77, "Issue #77", "", "feature-dev", "err");
  });
});
