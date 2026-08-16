/**
 * undeterminedBranch.pipelineState.test.ts
 *
 * Issue #448 — the PIPELINE-STATE path must never fabricate a branch.
 *
 * #397 removed the two Go writers that emitted a synthetic `feat/{issue}` when
 * nothing named a branch, and established the contract those records now use:
 * the `branch` key is always present, `""` means "no branch could be determined
 * for this run", and a non-empty value means a branch that actually resolved.
 * `tests/views/dashboard/undeterminedBranch.record.test.ts` pins the READER
 * half of that contract for the history record.
 *
 * This file pins the same contract one layer earlier, on pipeline state, which
 * was the last surviving origin of the fabrication class. The TypeScript seeds
 * ran BEFORE issue-pickup resolves a branch and invented `feat/{issueNumber}`
 * to satisfy `schemas/pipelineState.ts`'s `branch: z.string().min(1)`. On the
 * interactive path that invented value is not cosmetic — it reaches a DURABLE
 * record:
 *
 *   PipelineStateService.initializePipeline
 *     -> ipc "pipeline.notifyStageTransition" { branch }
 *       -> internal/ipc/server.go: rt.SeedRunContext(p.Repo, p.Title, p.Branch)
 *         -> RuntimeState.Branch          (internal/state/runtime_state.go)
 *           -> snap.Branch
 *             -> state.V2RunInput{ Branch: snap.Branch }   (server.go)
 *               -> the run's V2 history record
 *
 * Post-#397 Go faithfully records whatever it is handed, so the fabrication
 * survived the Go-side fix by entering from the extension instead.
 *
 * `SeedRunContext` is latest-wins ON A NON-EMPTY VALUE ONLY (`if branch != ""`),
 * which is why `""` is the right seed rather than a new sentinel: it writes
 * nothing, so issue-pickup's real branch is still the first value the record
 * ever sees, and a run that never resolves one records `""` — the same honest
 * empty the Go writers now produce.
 *
 * WHY A SOURCE ASSERTION FOR HALF OF IT. Two of the six fabrication sites sit
 * mid-`runPipeline` and mid-webview-subscription, neither reachable without
 * standing up a whole pipeline run or a live webview (the constraint
 * tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts documents for the
 * same two classes of call site). The seeds are one expression each and the fix
 * is their deletion, so the sources are asserted directly; the two sites that
 * ARE reachable are exercised behaviourally below and in
 * tests/views/dashboard/Dashboard.undeterminedBranch.test.ts.
 */

import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import { PipelineStateSchema } from "../../src/schemas/pipelineState";
import {
  getBranchDisplayText,
  getProgressBarHtml,
  UNDETERMINED_BRANCH_LABEL,
} from "../../src/views/dashboard/DashboardComponents";
import type { PipelineRunSummary } from "../../src/views/dashboard/DashboardState";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

const ORCHESTRATOR_PATH = path.resolve(__dirname, "../../src/services/HeadlessOrchestrator.ts");
const DASHBOARD_PATH = path.resolve(__dirname, "../../src/views/dashboard/Dashboard.ts");

/**
 * A `feat/`-prefixed template literal whose ONLY interpolation is an issue
 * number — `feat/${issueNumber}`, `feat/${item.issueNumber}`,
 * `feat/${pipelineState.issue_number}`. That is the fabrication shape: a name
 * derived from nothing but the issue number, indistinguishable in a record from
 * a branch that really resolved.
 *
 * Deliberately NOT matched: `feat/${n}-${slug}` (ConcurrentPipelineManager
 * derives that from the title and then actually CREATES the branch, so it names
 * a branch that exists) and `feat/` inside a comment or an @example.
 */
const FABRICATED_BRANCH = /`feat\/\$\{[A-Za-z0-9_.]*(issueNumber|issue_number)\}`/g;

function fabricationsIn(source: string): string[] {
  return source
    .split("\n")
    .filter((line) => !line.trim().startsWith("*") && !line.trim().startsWith("//"))
    .flatMap((line) => line.match(FABRICATED_BRANCH) ?? []);
}

function makeStateServiceMock(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(null),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    beginRun: vi.fn(),
    endRun: vi.fn(),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  } as unknown as PipelineStateService;
}

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

describe("the pipeline-state path never fabricates a branch (#448)", () => {
  describe("sources — no `feat/{issueNumber}` seed survives", () => {
    it("HeadlessOrchestrator seeds no branch it was not given", () => {
      const source = readFileSync(ORCHESTRATOR_PATH, "utf-8");
      expect(fabricationsIn(source)).toEqual([]);
      // The comment the two deleted seeds carried verbatim. Its absence is a
      // second, independent tell — a reintroduction that renamed the variable
      // would still be caught if it copied the rationale back with it.
      expect(source).not.toContain("Placeholder - issue-pickup updates with real branch");
    });

    it("Dashboard starts no run under a branch it was not given", () => {
      const source = readFileSync(DASHBOARD_PATH, "utf-8");
      expect(fabricationsIn(source)).toEqual([]);
    });

    it("the matcher recognises the exact expressions that were removed", () => {
      // A guard whose regex silently stopped matching would report "clean"
      // forever. These are the six removed seeds, verbatim.
      for (const removed of [
        "        this.state.startRun(issueNumber, `Issue #${issueNumber}`, `feat/${issueNumber}`);",
        "        `feat/${item.issueNumber}` // Placeholder",
        "          `feat/${issueNumber}` // Placeholder",
        "        pipelineState.branch ?? `feat/${pipelineState.issue_number}`",
      ]) {
        expect(fabricationsIn(removed)).not.toEqual([]);
      }
      // …and does not fire on the branch ConcurrentPipelineManager really creates.
      expect(
        fabricationsIn("    const branchName = `feat/${item.issueNumber}-${this.slugify(title)}`;")
      ).toEqual([]);
    });
  });

  describe("startNextQueuedIssue — the reachable seed", () => {
    let stateService: PipelineStateService;
    let orchestrator: HeadlessOrchestrator;

    beforeEach(() => {
      vi.clearAllMocks();
      stateService = makeStateServiceMock();
      orchestrator = new HeadlessOrchestrator(stateService, makeLogger());
      // Avoid the `gh repo view` probe in resolveRunRepoSlug().
      orchestrator.setRepoOverride("nightgauge/nightgauge");
      // The seed is the unit under test; the run that follows it is not.
      vi.spyOn(orchestrator, "runPipeline").mockResolvedValue({
        success: true,
      } as unknown as Awaited<ReturnType<HeadlessOrchestrator["runPipeline"]>>);
    });

    it("initializes pipeline state with an UNDETERMINED branch, not `feat/{n}`", async () => {
      await orchestrator.startNextQueuedIssue({ issueNumber: 448, title: "Queued issue" });

      expect(stateService.initializePipeline).toHaveBeenCalledTimes(1);
      const [issueNumber, title, branch] = vi.mocked(stateService.initializePipeline).mock
        .calls[0] as [number, string, string];

      expect(issueNumber).toBe(448);
      expect(title).toBe("Queued issue");
      // "" is the whole contract: present, and empty because nothing here knows
      // a branch. A `feat/448` would be byte-identical to a resolved branch by
      // the time it reaches V2RunInput.Branch.
      expect(branch).toBe("");
      expect(branch).not.toMatch(/^feat\//);
    });

    it("does not fall back to a fabrication when the queue item has no title", async () => {
      await orchestrator.startNextQueuedIssue({ issueNumber: 449 });

      const [, title, branch] = vi.mocked(stateService.initializePipeline).mock.calls[0] as [
        number,
        string,
        string,
      ];
      // The title DOES have an honest synthetic form — "Issue #449" is not a
      // claim about anything. A branch name is, which is why only one of these
      // two fields gets a fallback.
      expect(title).toBe("Issue #449");
      expect(branch).toBe("");
    });
  });

  describe("schemas/pipelineState — an undetermined branch survives", () => {
    const validState = {
      schema_version: "1.0" as const,
      issue_number: 448,
      title: "Pipeline-state path still fabricates feat/{N}",
      branch: "",
      base_branch: "main",
      started_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      execution_mode: "automatic" as const,
      paused: false,
      stages: {},
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    };

    it('accepts branch "" and keeps it empty', () => {
      const result = PipelineStateSchema.safeParse(validState);
      expect(result.success).toBe(true);
      expect(result.success && result.data.branch).toBe("");
    });

    it("still accepts a resolved branch unchanged", () => {
      const result = PipelineStateSchema.safeParse({
        ...validState,
        branch: "fix/448-no-fabricated-branch-placeholders",
      });
      expect(result.success).toBe(true);
      expect(result.success && result.data.branch).toBe(
        "fix/448-no-fabricated-branch-placeholders"
      );
    });

    it("still REQUIRES the key — absent is a different fact from undetermined", () => {
      // The #397 contract lives in the bytes: `""` means "we looked and found
      // nothing", an absent key means a foreign producer or a pre-contract
      // writer. Relaxing `.min(1)` must not collapse the two.
      const { branch: _dropped, ...withoutBranch } = validState;
      expect(PipelineStateSchema.safeParse(withoutBranch).success).toBe(false);
    });

    it("keeps title non-empty — only `branch` gained the empty meaning", () => {
      expect(PipelineStateSchema.safeParse({ ...validState, title: "" }).success).toBe(false);
    });
  });

  describe("readers — an undetermined branch renders as undetermined", () => {
    function runWithBranch(branch: string): PipelineRunSummary {
      return {
        issueNumber: 448,
        title: "Pipeline-state path still fabricates feat/{N}",
        branch,
        startedAt: new Date(),
        status: "running",
        stages: [
          { stage: "pipeline-start", status: "complete" },
          { stage: "issue-pickup", status: "running" },
        ],
        usage: {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0,
          durationMs: 0,
          stageCount: 0,
        },
        toolCalls: [],
      } as unknown as PipelineRunSummary;
    }

    it("getBranchDisplayText labels the empty seed", () => {
      expect(getBranchDisplayText("")).toBe(UNDETERMINED_BRANCH_LABEL);
    });

    it("the live progress bar prints the label, not `Branch: `", () => {
      const html = getProgressBarHtml(runWithBranch(""));
      expect(html).toContain(`Branch: ${UNDETERMINED_BRANCH_LABEL}`);
      // The failure mode this closes: a blank after "Branch:" reads as a
      // rendering bug, not as a fact about the run.
      expect(html).not.toContain('<span class="progress-branch" title="">Branch: </span>');
      // …and it is marked so styling can say so too.
      expect(html).toContain("progress-branch-undetermined");
      expect(html).toContain("No branch recorded for this run");
    });

    it("a resolved branch is still printed verbatim and unmarked", () => {
      const html = getProgressBarHtml(runWithBranch("fix/448-no-fabricated-branch-placeholders"));
      expect(html).toContain("Branch: fix/448-no-fabricated-branch-placeholders");
      expect(html).not.toContain(UNDETERMINED_BRANCH_LABEL);
      expect(html).not.toContain("progress-branch-undetermined");
    });
  });
});
