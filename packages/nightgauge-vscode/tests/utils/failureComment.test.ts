/**
 * failureComment.test.ts
 *
 * Unit tests for the failure comment builder.
 *
 * Specifically verifies Issue #2777: cost shown in the failure comment summary
 * table matches the accumulated pipeline cost in state (the single source of
 * truth), so that comment cost and JSONL history cost are consistent.
 *
 * @see Issue #2777 - Cost reporting inconsistent
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineState } from "../../src/services/PipelineStateService";
import type { PipelineRunResult } from "../../src/services/HeadlessOrchestrator";

// ---------------------------------------------------------------------------
// Isolate the buildCommentBody helper by importing via private export.
// Since buildCommentBody is not exported, we test postFailureComment via
// the command output it builds. For unit testing cost values we can mock
// exec and capture the --body arg.
// ---------------------------------------------------------------------------

// Use vi.hoisted so execMock is available inside the vi.mock factory (which
// is hoisted to the top of the file by Vitest before other variable declarations).
const { execMock } = vi.hoisted(() => ({ execMock: vi.fn() }));

vi.mock("child_process", () => ({
  exec: execMock,
}));

vi.mock("util", () => ({
  promisify: (fn: unknown) => {
    // Return a function that calls the underlying mock and resolves with {stdout, stderr}
    return (...args: unknown[]) =>
      new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
        (fn as (...a: unknown[]) => void)(
          ...args,
          (err: Error | null, stdout: string, stderr: string) => {
            if (err) reject(Object.assign(err, { stdout, stderr }));
            else resolve({ stdout: stdout ?? "", stderr: stderr ?? "" });
          }
        );
      });
  },
}));

import {
  postFailureComment,
  ARCHITECTURE_APPROVAL_REQUIRED_MARKER,
  BLOCKED_DEPENDENCY_MARKER,
} from "../../src/utils/failureComment";
import { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeResult(overrides?: Partial<PipelineRunResult>): PipelineRunResult {
  return {
    success: false,
    completedStages: ["issue-pickup", "feature-planning", "feature-dev"],
    skippedStages: [],
    failedStage: "feature-dev",
    totalDurationMs: 120_000,
    budgetExceeded: true,
    error: new Error(
      "Stage feature-dev terminated: budget exceeded. Cost $302.06 exceeded the hard limit ($150.00)."
    ),
    outcomeType: "failure" as unknown as PipelineRunResult["outcomeType"],
    ...overrides,
  };
}

function makeState(estimatedCostUsd: number): PipelineState {
  return {
    schema_version: "1.0",
    issue_number: 2777,
    title: "Test budget exceeded",
    branch: "feat/2777-test",
    base_branch: "main",
    started_at: new Date(Date.now() - 120_000).toISOString(),
    updated_at: new Date().toISOString(),
    execution_mode: "automatic",
    paused: false,
    stages: {
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "failed", error: "budget exceeded" },
    },
    tokens: {
      total_input: 180000,
      total_output: 35000,
      total_cache_read: 125000,
      total_cache_creation: 0,
      estimated_cost_usd: estimatedCostUsd,
      per_stage: {
        "issue-pickup": {
          input: 15000,
          output: 2000,
          cache_read: 10000,
          cache_creation: 0,
          cost_usd: 0.08,
        },
        "feature-planning": {
          input: 45000,
          output: 8000,
          cache_read: 35000,
          cache_creation: 0,
          cost_usd: 0.42,
        },
        "feature-dev": {
          input: 120000,
          output: 25000,
          cache_read: 80000,
          cache_creation: 0,
          cost_usd: 1.85,
        },
      },
    },
  } as unknown as PipelineState;
}

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

const logger = new Logger("test");

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("postFailureComment — cost reporting (Issue #2777)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_cmd: string, _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  it("failure comment includes non-zero total cost when state has accumulated cost", async () => {
    const state = makeState(2.35); // 0.08 + 0.42 + 1.85
    const result = makeResult();

    await postFailureComment({
      issueNumber: 2777,
      result,
      state,
      cwd: "/tmp/repo",
      logger,
    });

    expect(execMock).toHaveBeenCalled();
    const cmd: string = execMock.mock.calls[0][0];

    // The cost in the summary table must show the accumulated total
    expect(cmd).toContain("$2.35");
    // And per-stage costs must also appear
    expect(cmd).toContain("$0.08");
    expect(cmd).toContain("$0.42");
    expect(cmd).toContain("$1.85");
  });

  it("failure comment shows N/A for cost when state tokens are missing", async () => {
    const result = makeResult();

    await postFailureComment({
      issueNumber: 2777,
      result,
      state: null, // no state available
      cwd: "/tmp/repo",
      logger,
    });

    expect(execMock).toHaveBeenCalled();
    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("N/A");
  });

  it("per-stage costs in timeline match the per_stage values in state", async () => {
    const state = makeState(2.35);
    const result = makeResult();

    await postFailureComment({
      issueNumber: 2777,
      result,
      state,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];

    // All three per-stage costs must appear in the timeline section
    expect(cmd).toContain("$0.08"); // issue-pickup
    expect(cmd).toContain("$0.42"); // feature-planning
    expect(cmd).toContain("$1.85"); // feature-dev
  });

  it("total cost in table equals sum of per-stage costs (consistency check)", async () => {
    // This is the core regression: total in summary table must match
    // what would appear in JSONL (both come from state.tokens).
    const perStageCosts = [0.08, 0.42, 1.85];
    const expectedTotal = perStageCosts.reduce((a, b) => a + b, 0); // 2.35

    const state = makeState(expectedTotal);
    const result = makeResult();

    await postFailureComment({
      issueNumber: 2777,
      result,
      state,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];

    // Both the summary and the per-stage breakdown must come from the same
    // state object — no divergence between what the comment shows and what
    // JSONL records.
    expect(cmd).toContain("$2.35"); // summary table total
    for (const cost of perStageCosts) {
      expect(cmd).toContain(`$${cost.toFixed(2)}`);
    }
  });
});

describe("postFailureComment — architecture-approval pause (Issue #4222)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_cmd: string, _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  function approvalResult(): PipelineRunResult {
    return makeResult({
      budgetExceeded: false,
      error: new Error(
        `${ARCHITECTURE_APPROVAL_REQUIRED_MARKER} — issue #173 is a high-impact decision that must ` +
          "be human-approved before feature-dev implements it. Why: production-touching change — " +
          "irreversible blast radius (#4135). To proceed: add the `approved:architecture` label."
      ),
    });
  }

  it("renders an 'Awaiting Architecture Approval' pause header, not a failure report", async () => {
    await postFailureComment({
      issueNumber: 173,
      result: approvalResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("Awaiting Architecture Approval");
    expect(cmd).not.toContain("Pipeline Failure Report");
    expect(cmd).toContain("No development or validation cost was incurred");
  });

  it("gives the approve-and-re-queue action, not the generic 'development failed' guidance", async () => {
    await postFailureComment({
      issueNumber: 173,
      result: approvalResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("approved:architecture");
    expect(cmd).toContain("approval-173.json");
    expect(cmd).toContain("architecture_approval");
    // The misleading feature-dev failure boilerplate must NOT appear.
    expect(cmd).not.toContain("could not implement the feature");
  });

  it("keeps the normal failure report when the marker is absent (regression guard)", async () => {
    await postFailureComment({
      issueNumber: 2777,
      result: makeResult(), // generic budget-exceeded error, no marker
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("Pipeline Failure Report");
    expect(cmd).not.toContain("Awaiting Architecture Approval");
  });
});

describe("postFailureComment — blocked-dependency deferral (Issue #231)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_cmd: string, _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  function blockedResult(): PipelineRunResult {
    return makeResult({
      budgetExceeded: false,
      failedStage: "issue-pickup",
      completedStages: [],
      error: new Error(
        `${BLOCKED_DEPENDENCY_MARKER} blocked by open dependency #123 (PR not merged)`
      ),
    });
  }

  it("renders a 'Deferred — Blocked by Dependency' notice, not a failure report", async () => {
    await postFailureComment({
      issueNumber: 231,
      result: blockedResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("Deferred — Blocked by Dependency");
    expect(cmd).not.toContain("Pipeline Failure Report");
    expect(cmd).toContain("No development or validation cost was incurred");
  });

  it("gives the no-action-required / deps-gate promote guidance", async () => {
    await postFailureComment({
      issueNumber: 231,
      result: blockedResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const cmd: string = execMock.mock.calls[0][0];
    expect(cmd).toContain("No action required");
    expect(cmd).toContain("deps-gate promote");
    // The misleading issue-pickup failure boilerplate must NOT appear.
    expect(cmd).not.toContain("verify the issue exists");
  });
});
