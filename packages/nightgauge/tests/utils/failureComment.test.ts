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
// the command it runs. For unit testing cost values we mock execFile and
// capture the --body argument.
// ---------------------------------------------------------------------------

// Use vi.hoisted so execMock is available inside the vi.mock factory (which
// is hoisted to the top of the file by Vitest before other variable declarations).
const { execMock } = vi.hoisted(() => ({ execMock: vi.fn() }));

vi.mock("child_process", () => ({
  execFile: execMock,
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
  quoteCriterion,
  scrubLocalPaths,
  LOCAL_PATH_PLACEHOLDER,
  ARCHITECTURE_APPROVAL_REQUIRED_MARKER,
  BLOCKED_DEPENDENCY_MARKER,
  HOST_SLEPT_MARKER,
} from "../../src/utils/failureComment";
import { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** The comment body the first `gh issue comment` call posted (its `--body` argument). */
function postedBody(call = 0): string {
  const args: string[] = execMock.mock.calls[call][1];
  return args[args.indexOf("--body") + 1];
}

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
    outcomeType: "failure",
    // Required member the base fixture omitted, so spreading Partial<> left it
    // possibly-undefined and the object was not a PipelineRunResult.
    deferredStages: [],
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
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
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
    const body: string = postedBody();

    // The cost in the summary table must show the accumulated total
    expect(body).toContain("$2.35");
    // And per-stage costs must also appear
    expect(body).toContain("$0.08");
    expect(body).toContain("$0.42");
    expect(body).toContain("$1.85");
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
    const body: string = postedBody();
    expect(body).toContain("N/A");
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

    const body: string = postedBody();

    // All three per-stage costs must appear in the timeline section
    expect(body).toContain("$0.08"); // issue-pickup
    expect(body).toContain("$0.42"); // feature-planning
    expect(body).toContain("$1.85"); // feature-dev
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

    const body: string = postedBody();

    // Both the summary and the per-stage breakdown must come from the same
    // state object — no divergence between what the comment shows and what
    // JSONL records.
    expect(body).toContain("$2.35"); // summary table total
    for (const cost of perStageCosts) {
      expect(body).toContain(`$${cost.toFixed(2)}`);
    }
  });
});

describe("postFailureComment — architecture-approval pause (Issue #4222)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
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

    const body: string = postedBody();
    expect(body).toContain("Awaiting Architecture Approval");
    expect(body).not.toContain("Pipeline Failure Report");
    expect(body).toContain("No development or validation cost was incurred");
  });

  it("gives the approve-and-re-queue action, not the generic 'development failed' guidance", async () => {
    await postFailureComment({
      issueNumber: 173,
      result: approvalResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    expect(body).toContain("approved:architecture");
    expect(body).toContain("approval-173.json");
    expect(body).toContain("architecture_approval");
    // The misleading feature-dev failure boilerplate must NOT appear.
    expect(body).not.toContain("could not implement the feature");
  });

  it("keeps the normal failure report when the marker is absent (regression guard)", async () => {
    await postFailureComment({
      issueNumber: 2777,
      result: makeResult(), // generic budget-exceeded error, no marker
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    expect(body).toContain("Pipeline Failure Report");
    expect(body).not.toContain("Awaiting Architecture Approval");
  });
});

describe("postFailureComment — blocked-dependency deferral (Issue #231)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
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

    const body: string = postedBody();
    expect(body).toContain("Deferred — Blocked by Dependency");
    expect(body).not.toContain("Pipeline Failure Report");
    expect(body).toContain("No development or validation cost was incurred");
  });

  it("gives the no-action-required / deps-gate promote guidance", async () => {
    await postFailureComment({
      issueNumber: 231,
      result: blockedResult(),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    expect(body).toContain("No action required");
    expect(body).toContain("deps-gate promote");
    // The misleading issue-pickup failure boilerplate must NOT appear.
    expect(body).not.toContain("verify the issue exists");
  });
});

// ---------------------------------------------------------------------------
// Issue #1531 — a pr-create kill AFTER the PR was verified must not be reported
// as "PR creation failed". pr-create writes pr-{N}.json the moment Phase 3.6
// confirms an OPEN PR; the stage can still be killed afterwards (the runaway
// monitor fired while it observed CI). The report must name the PR and describe
// a post-create stall instead of sending the operator to re-create the work.
// ---------------------------------------------------------------------------

function prCreateKilledResult(): PipelineRunResult {
  return {
    success: false,
    completedStages: ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"],
    skippedStages: [],
    deferredStages: [],
    failedStage: "pr-create",
    totalDurationMs: 930_000,
    budgetExceeded: false,
    outcomeType: "failure",
    error: new Error(
      "Stage pr-create terminated: No productive progress (commit / new file / phase / CI) for 930s"
    ),
  };
}

function prCreateState(prMeta: Record<string, unknown>): PipelineState {
  return {
    schema_version: "1.0",
    issue_number: 1431,
    title: "Test post-create stall",
    branch: "feat/1431-test",
    base_branch: "main",
    started_at: new Date(Date.now() - 930_000).toISOString(),
    updated_at: new Date().toISOString(),
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pr-create": { status: "failed", error: "runaway progress" },
    },
    ...prMeta,
  } as unknown as PipelineState;
}

describe("postFailureComment — pr-create killed with a verified PR (Issue #1531)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  it("names the PR and does not claim creation failed when pipeline_meta has the PR number", async () => {
    await postFailureComment({
      issueNumber: 1431,
      result: prCreateKilledResult(),
      state: prCreateState({
        pipeline_meta: { pr_number: 1447 },
        pr_url: "https://github.com/acme/acme-platform/pull/1447",
      }),
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    // The false claim must be gone.
    expect(body).not.toContain("PR creation failed");
    expect(body).not.toContain("could not push the branch or create the PR");
    // The PR must be named, and linked when its URL is known.
    expect(body).toContain("PR #1447");
    expect(body).toContain("https://github.com/acme/acme-platform/pull/1447");
    // And the stall must be described as post-create.
    expect(body).toContain("stalled after opening it");
    expect(body).toContain("nothing needs re-creating");
  });

  it("recovers the PR number from pr_url alone when pipeline_meta is absent", async () => {
    await postFailureComment({
      issueNumber: 1431,
      result: prCreateKilledResult(),
      state: prCreateState({
        pr_url: "https://github.com/acme/acme-platform/pull/1447",
      }),
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    expect(body).not.toContain("PR creation failed");
    expect(body).toContain("PR #1447");
  });

  it("still reports a real creation failure when no PR was ever verified", async () => {
    await postFailureComment({
      issueNumber: 1431,
      result: prCreateKilledResult(),
      state: prCreateState({}),
      cwd: "/tmp/repo",
      logger,
    });

    const body: string = postedBody();
    expect(body).toContain("PR creation failed");
    expect(body).toContain("could not push the branch or create the PR");
    expect(body).not.toContain("stalled after opening it");
  });
});

describe("postFailureComment — runaway kill (Issue #2694)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  async function comment(failedStage: string, ceiling: string): Promise<string> {
    execMock.mockClear();
    await postFailureComment({
      issueNumber: 2694,
      result: makeResult({
        budgetExceeded: false,
        failedStage,
        error: new Error(
          `[runaway-progress-exceeded] Stage terminated: x. Ceiling: ${ceiling}. Cost $1`
        ),
      } as Partial<PipelineRunResult>),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });
    return postedBody();
  }

  it("names the monitor and ceiling instead of a validation verdict", async () => {
    const body = await comment(
      "feature-validate",
      "progress-background-task-ceiling=2700000ms (x)"
    );
    expect(body).toContain("Stopped by the runaway monitor");
    expect(body).toContain("progress-background-task-ceiling=2700000ms");
    expect(body).not.toContain("Validation failed");
  });

  it("keeps a decimal limit whole and drops the sentence's period", async () => {
    expect(await comment("feature-validate", "progress-catastrophic-cost=$2.50 (x)")).toContain(
      "`progress-catastrophic-cost=$2.50`"
    );
    expect(await comment("feature-validate", "progress-no-progress-window=90000ms")).toContain(
      "`progress-no-progress-window=90000ms`"
    );
  });

  it("drops the verdict on another stage and survives an unattributed kill", async () => {
    const body = await comment("feature-dev", "(unattributed)");
    expect(body).toContain("ceiling not recorded");
    expect(body).not.toContain("could not implement the feature");
  });

  it("names the ceiling from the text HeadlessOrchestrator really produces", async () => {
    execMock.mockClear();
    await postFailureComment({
      issueNumber: 2694,
      result: makeResult({
        budgetExceeded: false,
        failedStage: "feature-validate",
        error: new Error(
          "[runaway-progress-exceeded] Stage feature-validate terminated: progress-based runaway " +
            "kill at cost $1.6663 (no dollar ceiling crossed). Treated as transient — will retry " +
            "with 30-minute backoff (Issue #3783). Ceiling: progress-background-task-ceiling=5400000ms."
        ),
      } as Partial<PipelineRunResult>),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });
    expect(postedBody()).toContain("`progress-background-task-ceiling=5400000ms`");
  });

  it("keeps the #1531 guidance when a runaway kill follows a verified PR", async () => {
    execMock.mockClear();
    const killed = prCreateKilledResult();
    await postFailureComment({
      issueNumber: 1431,
      result: {
        ...killed,
        error: new Error(`[runaway-progress-exceeded] ${killed.error?.message}`),
      },
      state: prCreateState({ pr_number: 1432, pr_url: "https://github.com/o/r/pull/1432" }),
      cwd: "/tmp/repo",
      logger,
    });
    const body = postedBody();
    expect(body).toContain("Stopped by the runaway monitor");
    expect(body).toContain("The PR was created");
  });
});

describe("postFailureComment — push auth refusal (Issue #2742)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  async function comment(message: string): Promise<string> {
    execMock.mockClear();
    await postFailureComment({
      issueNumber: 2706,
      result: makeResult({
        budgetExceeded: false,
        failedStage: "feature-validate",
        error: new Error(message),
      } as Partial<PipelineRunResult>),
      state: null,
      cwd: "/tmp/repo",
      logger,
    });
    return postedBody();
  }

  it("names the push/auth cause and never claims the tests failed", async () => {
    const body = await comment(
      '[push-auth-failed] feature-validate could not push: the remote refused this machine\'s git credentials (validation_status="passed").'
    );
    expect(body).toContain("Push refused for authentication");
    expect(body).not.toContain("did not pass tests");
  });

  it("keeps the validation-failed text for a genuine test failure", async () => {
    const body = await comment(
      '[validation-failed] feature-validate reported validation_status="failed".'
    );
    expect(body).toContain("did not pass tests");
    expect(body).not.toContain("Push refused for authentication");
  });
});

describe("postFailureComment — host slept (Issue #3136)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  it("says how long the host slept and how to prevent it, and never calls it a validation failure", async () => {
    // The text orchestrator.HostSleptStageError writes for a gate timeout
    // that expired while the host slept.
    const result = makeResult({
      failedStage: "feature-validate",
      budgetExceeded: false,
      error: new Error(
        `${HOST_SLEPT_MARKER} the host slept for 15m27s during feature-validate (wall clock 20m0s, awake 4m33s). ` +
          "This run was booked as infrastructure, not as a failure of this issue, and will retry. " +
          "To keep the host awake: the daemon holds a `caffeinate -i -s` assertion while the autonomous " +
          "scheduler runs (see `nightgauge autonomous status`), or disable sleep on AC power with `sudo pmset -c sleep 0`."
      ),
    });
    const state = makeState(1.5);
    state.stage_host_slept = { "feature-validate": true };
    state.gate_results = [
      {
        gate_name: "feature-validate",
        result: "timeout",
        error_summary: "validation gate timed out",
      },
    ] as unknown as PipelineState["gate_results"];

    await postFailureComment({ issueNumber: 3136, result, state, cwd: "/tmp/repo", logger });

    expect(execMock).toHaveBeenCalled();
    const body: string = postedBody();
    expect(body).toContain("15m27s");
    expect(body).toContain("pmset -c sleep 0");
    expect(body).toContain("Host Slept");
    expect(body).not.toMatch(/validation/i);
    expect(body).not.toContain("Pipeline Failure Report");
    expect(body).not.toContain("lifetime failure cap");
  });

  it("posts the ordinary report when the text only echoes the marker (#3273)", async () => {
    const result = makeResult({
      failedStage: "feature-dev",
      budgetExceeded: false,
      error: new Error(`tests failed; log mentioned ${HOST_SLEPT_MARKER} from an earlier run`),
    });
    await postFailureComment({
      issueNumber: 3273,
      result,
      state: makeState(1.5),
      cwd: "/tmp/repo",
      logger,
    });
    const body: string = postedBody();
    expect(body).not.toContain("Host Slept");
    expect(body).toContain("Pipeline Failure Report");
  });
});

describe("postFailureComment — partial-delivery no-op (Issue #3487)", () => {
  beforeEach(() => {
    execMock.mockReset();
    execMock.mockImplementation((_file: string, _args: string[], _opts: unknown, cb: Function) => {
      cb(null, "", "");
    });
  });

  const marker = "<!-- nightgauge:partial-delivery pr=7 -->";

  function noOpResult(overrides?: Partial<PipelineRunResult>): PipelineRunResult {
    return makeResult({
      failedStage: "feature-dev",
      budgetExceeded: false,
      // The booking text quotes another kind's words; the comment must not read it.
      error: new Error("[not-pipeline-actionable] wrote no changes — merge_conflict"),
      partialNoOp: {
        remainder: {
          all_met: false,
          criteria: [
            { text: "The slot card renders a no-op.", met: true, evidence: "done in #3516" },
            {
              text: `Close the loop ${marker} **bold** [link](https://x.test) <b>html</b>`,
              met: false,
              evidence: "nothing in the diff touches it",
            },
          ],
        },
      },
      ...overrides,
    });
  }

  async function post(result: PipelineRunResult, repoOverride?: string): Promise<string> {
    await postFailureComment({
      issueNumber: 3487,
      result,
      state: makeState(1.25),
      repoOverride,
      cwd: "/tmp/repo",
      logger,
    });
    return postedBody();
  }

  it("posts its own heading, not the failure report, with each open criterion and the ways forward", async () => {
    const body = await post(noOpResult());
    expect(body).toContain("Nothing Left to Build");
    expect(body).not.toContain("Pipeline Failure Report");
    expect(body).not.toContain("Failed Stage");
    expect(body).not.toContain("merge_conflict");
    expect(body).toContain("nothing left to build");
    // Only the open criterion is listed, with the judge's reason.
    expect(body).not.toContain("The slot card renders a no\\-op");
    expect(body).toContain("Close the loop");
    expect(body).toContain("nothing in the diff touches it");
    // The two ways forward.
    expect(body).toContain("**close**");
    expect(body).toContain("move the issue to **Ready**");
  });

  it("quotes criterion markup inert, including a forged partial-delivery marker", async () => {
    const body = await post(noOpResult());
    expect(body).not.toContain(marker);
    expect(body).not.toContain("<!--");
    expect(body).not.toContain("<b>");
    expect(body).not.toContain("**bold**");
    expect(body).not.toContain("[link](");
    expect(body).toContain("&lt;\\!\\-\\- nightgauge:partial\\-delivery pr=7 \\-\\-&gt;");
    expect(quoteCriterion("a\n# heading")).toBe("a \\# heading");
    // `&` is escaped first, so an entity in the text stays visible as text.
    expect(quoteCriterion("&lt;b&gt;")).toBe("&amp;lt;b&amp;gt;");
    expect(quoteCriterion("a\r\n\r\nb")).toBe("a b");
    expect(quoteCriterion("- item | `code` > quote")).toBe("\\- item \\| \\`code\\` &gt; quote");
    expect(quoteCriterion("")).toBe("");
  });

  it("covers an all-met remainder and an open criterion with no recorded reason", async () => {
    const allMet = await post(
      noOpResult({
        partialNoOp: {
          remainder: { all_met: true, criteria: [{ text: "done", met: true, evidence: "x" }] },
        },
      })
    );
    expect(allMet).toContain("The judge found no criterion still open.");
    execMock.mockClear();
    const noReason = await post(
      noOpResult({
        partialNoOp: {
          remainder: { all_met: false, criteria: [{ text: "open one", met: false, evidence: "" }] },
        },
      })
    );
    expect(noReason).toContain("no reason recorded");
  });

  it("breaks mentions, redacts secrets and bounds each quoted field", async () => {
    const token = "ghp_" + "A".repeat(36);
    const body = await post(
      noOpResult({
        partialNoOp: {
          remainder: {
            all_met: false,
            criteria: [
              {
                text: `Ping @someone and @org/team ${"long ".repeat(200)}`,
                met: false,
                evidence: `the judge saw ${token}`,
              },
            ],
          },
        },
      })
    );
    expect(body).not.toMatch(/@someone|@org/);
    expect(body).toContain("@\u200Bsomeone");
    expect(body).not.toContain(token);
    expect(body).toContain("truncated");
    expect(body.length).toBeLessThan(3000);
  });

  it("says the open list is unavailable when the booking carried no remainder", async () => {
    const body = await post(noOpResult({ partialNoOp: {} }));
    expect(body).toContain("was not available");
    expect(body).not.toContain("Failed Stage");
  });

  it("hands shell-significant text to gh byte-for-byte", async () => {
    const evil = "it's `whoami` $(rm -rf ~); echo";
    const body = await post(
      noOpResult({
        partialNoOp: {
          remainder: { all_met: false, criteria: [{ text: evil, met: false, evidence: "e" }] },
        },
      })
    );
    expect(body).toContain(quoteCriterion(evil));
    expect(body).not.toContain("'\\''");
  });

  it("posts with an argument array through execFile, never a shell string", async () => {
    await post(noOpResult(), "nightgauge/nightgauge");
    const [file, args, opts] = execMock.mock.calls[0];
    expect(file).toBe("gh");
    expect(Array.isArray(args)).toBe(true);
    expect(args.slice(0, 5)).toEqual([
      "issue",
      "comment",
      "3487",
      "--repo",
      "nightgauge/nightgauge",
    ]);
    expect(args[5]).toBe("--body");
    expect(args[6]).toContain(quoteCriterion(`Close the loop ${marker}`));
    expect(args).toHaveLength(7);
    expect(opts).toMatchObject({ cwd: "/tmp/repo" });
  });

  it("keeps the failure report for a first-run gate no-op and for a crash", async () => {
    const gateNoOp = await post(
      makeResult({
        budgetExceeded: false,
        error: new Error("feature-dev gate: no_op — the stage produced no changes"),
      })
    );
    expect(gateNoOp).toContain("Pipeline Failure Report");
    expect(gateNoOp).toContain("Failed Stage");
    execMock.mockClear();
    const crash = await post(
      makeResult({ budgetExceeded: false, error: new Error("subagent crashed: exit 137") })
    );
    expect(crash).toContain("Pipeline Failure Report");
  });

  describe("local-path scrub", () => {
    const home = "/Users/someone";
    const worktree = `${home}/.nightgauge/state/worktrees/abc/repo-issue-1/src/a.ts`;

    it.each([
      ["(", `see (${worktree}) here`],
      ['"', `path "${worktree}" here`],
      ["'", `path '${worktree}' here`],
      ["=", `--dir=${worktree} here`],
      [":", `at:${worktree} here`],
      ["`", "path `" + worktree + "` here"],
      ["space", `path ${worktree} here`],
    ])("removes a path directly after %s", (_delim, text) => {
      const out = scrubLocalPaths(text, home);
      expect(out).not.toContain("/Users/");
      expect(out).not.toContain(".nightgauge/state");
      expect(out).toContain(LOCAL_PATH_PLACEHOLDER);
      expect(out.endsWith(" here")).toBe(true);
    });

    it("removes the home directory wherever it is, and other users' and linux homes", () => {
      expect(scrubLocalPaths("x=/opt/me/repo/a.go", "/opt/me")).toBe(`x=${LOCAL_PATH_PLACEHOLDER}`);
      expect(scrubLocalPaths("(/home/bob/w/x)", "/opt/me")).toBe(`(${LOCAL_PATH_PLACEHOLDER})`);
      expect(scrubLocalPaths("/private/tmp/x", "/opt/me")).toBe(LOCAL_PATH_PLACEHOLDER);
      expect(scrubLocalPaths("relative/src/a.ts and https://github.com/o/r", "/opt/me")).toBe(
        "relative/src/a.ts and https://github.com/o/r"
      );
    });

    it("guards an empty or root home, escapes a home with metacharacters, and scrubs every path", () => {
      expect(scrubLocalPaths("/etc/hosts and /usr/bin/gh", "/")).toBe("/etc/hosts and /usr/bin/gh");
      expect(scrubLocalPaths("/etc/hosts", "")).toBe("/etc/hosts");
      expect(scrubLocalPaths("x /opt/me.x+y/a.go", "/opt/me.x+y")).toBe(
        `x ${LOCAL_PATH_PLACEHOLDER}`
      );
      expect(scrubLocalPaths("/opt/meAx+y/a.go", "/opt/me.x+y")).toBe("/opt/meAx+y/a.go");
      expect(scrubLocalPaths("a /tmp/x b /var/folders/q/y c <" + "/root/z>", "/opt/me")).toBe(
        `a ${LOCAL_PATH_PLACEHOLDER} b ${LOCAL_PATH_PLACEHOLDER} c <${LOCAL_PATH_PLACEHOLDER}>`
      );
      expect(scrubLocalPaths("[/Users/a/b]", "/opt/me")).toBe(`[${LOCAL_PATH_PLACEHOLDER}]`);
    });

    it("scrubs paths out of the posted no-op comment", async () => {
      const body = await post(
        noOpResult({
          partialNoOp: {
            remainder: {
              all_met: false,
              criteria: [
                {
                  text: `Fix (${worktree})`,
                  met: false,
                  evidence: `judge read "/home/bob/repo/x.go"`,
                },
              ],
            },
          },
        })
      );
      expect(body).not.toContain("/Users/someone");
      expect(body).not.toContain("/home/bob");
      expect(body).toContain(LOCAL_PATH_PLACEHOLDER);
    });
  });
});
