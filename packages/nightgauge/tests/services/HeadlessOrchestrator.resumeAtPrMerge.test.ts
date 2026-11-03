/**
 * HeadlessOrchestrator.resumeAtPrMerge.test.ts
 *
 * Issue #1531 — a re-dispatched issue whose head already has an OPEN pipeline
 * PR must resume at pr-merge, not re-plan.
 *
 * The #500 restore path only fired when pipeline state REMEMBERED pr-create
 * completing. A re-dispatch after a killed pr-create satisfies neither half of
 * that: the new slot gets a fresh PipelineStateService with no state.json (so
 * every stage reads pending), while the reused worktree still holds
 * `pr-{N}.json` naming an open PR. platform#1431 therefore re-ran
 * issue-pickup → feature-planning → feature-dev → feature-validate → pr-create
 * ($2.11, 16 minutes) only to rediscover PR #1447 and push more commits onto it.
 *
 * An open PR recorded on disk is proof the work shipped, whatever state
 * remembers — while the issue's acceptance criteria still match the
 * fingerprint the PR was built against (Issue #2623).
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import { existsSync, readFileSync, rmSync } from "fs";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi
    .fn()
    .mockReturnValue({ model: "claude-haiku-4-5-20251001", source: "stage-default" }),
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(false),
    readFileSync: vi.fn().mockReturnValue("{}"),
    writeFileSync: vi.fn(),
    mkdirSync: vi.fn(),
    rmSync: vi.fn(),
  };
});

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  workspace: { workspaceFolders: [], getConfiguration: vi.fn(() => ({ get: vi.fn() })) },
}));

/** A fresh slot's state: nothing has run yet. */
function freshState(issueNumber: number) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    stages: {},
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
  };
}

function createMockStateService(state: unknown): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(state),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    setMeta: vi.fn(),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  } as unknown as PipelineStateService;
}

/**
 * A state service that books stage transitions the way the Go notify handler
 * does (`internal/ipc/server.go`): `complete` is recorded only for the stage
 * that has begun (#2536); a completion for any other stage is dropped.
 * `skipped` has no such guard.
 */
function createGoFaithfulStateService(issueNumber: number) {
  const stages: Record<string, { status: string }> = {};
  let current = "";
  const service = {
    getState: vi.fn(async () => ({ ...freshState(issueNumber), stages: { ...stages } })),
    startStage: vi.fn(async (stage: string) => {
      current = stage;
      stages[stage] = { status: "running" };
    }),
    completeStage: vi.fn(async (stage: string) => {
      if (current !== stage) return;
      stages[stage] = { status: "complete" };
    }),
    skipStage: vi.fn(async (stage: string) => {
      stages[stage] = { status: "skipped" };
    }),
    setMeta: vi.fn(),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  };
  return { service: service as unknown as PipelineStateService, stages };
}

const STAGE_ORDER = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

const PR_CONTEXT_SUFFIX = "pr-1431.json";

/** The fingerprint every "unchanged criteria" case records and reads back. */
const FP = "sha256:unchanged";

describe("HeadlessOrchestrator — resume at pr-merge when an open PR exists (Issue #1531)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  /** Make only `pr-1431.json` exist, with the given contents. */
  function withPrContext(contents: unknown | null) {
    vi.mocked(existsSync).mockImplementation((p: unknown) =>
      String(p).endsWith(PR_CONTEXT_SUFFIX) && contents !== null ? true : false
    );
    vi.mocked(readFileSync).mockImplementation((p: unknown) => {
      if (String(p).endsWith(PR_CONTEXT_SUFFIX) && contents !== null) {
        return typeof contents === "string" ? contents : JSON.stringify(contents);
      }
      return "{}";
    });
  }

  /** Stub the binary's current-criteria fingerprint (#2623). */
  function stubFingerprint(orchestrator: HeadlessOrchestrator, value: string | null) {
    return vi
      .spyOn(
        orchestrator as unknown as { currentAcFingerprint(n: number): Promise<string | null> },
        "currentAcFingerprint"
      )
      .mockResolvedValue(value);
  }

  /** Stub the forge's answer for the recorded PR (#2878). */
  function stubForgeState(orchestrator: HeadlessOrchestrator, value: string | null) {
    return vi
      .spyOn(
        orchestrator as unknown as { forgePrState(n: number): Promise<string | null> },
        "forgePrState"
      )
      .mockResolvedValue(value);
  }

  /** Stub the forge's answer for the issue (#2878). */
  function stubIssueOpen(orchestrator: HeadlessOrchestrator, value: boolean) {
    return vi
      .spyOn(orchestrator as unknown as { issueIsOpen(n: number): Promise<boolean> }, "issueIsOpen")
      .mockResolvedValue(value);
  }

  function makeOrchestrator(state: unknown, currentFingerprint: string | null = FP) {
    const mockState = createMockStateService(state);
    const orchestrator = new HeadlessOrchestrator(mockState, logger, { contextFileWaitMs: 0 });
    stubFingerprint(orchestrator, currentFingerprint);
    stubForgeState(orchestrator, "OPEN");
    return { orchestrator, mockState };
  }

  async function detect(orchestrator: HeadlessOrchestrator, issueNumber: number) {
    await (
      orchestrator as unknown as {
        detectAndRestoreExistingPr(n: number): Promise<void>;
      }
    ).detectAndRestoreExistingPr(issueNumber);
  }

  it("fast-forwards a fresh run to pr-merge when pr-{N}.json names an OPEN PR", async () => {
    withPrContext({
      schema_version: "1.0",
      issue_number: 1431,
      pr_number: 1447,
      pr_url: "https://github.com/acme/acme-platform/pull/1447",
      status: "open",
      ac_fingerprint: FP,
    });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    // Every AI stage before the merge is recorded skipped, with the reason.
    for (const stage of ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"]) {
      expect(mockState.skipStage).toHaveBeenCalledWith(stage, expect.stringContaining("PR #1447"));
    }
    // pr-create is complete, not skipped — its deliverable really exists, and
    // pr-merge's pre-condition check needs it to point at pr-{N}.json.
    expect(mockState.startStage).toHaveBeenCalledWith("pr-create");
    expect(mockState.completeStage).toHaveBeenCalledWith("pr-create");
    expect(mockState.skipStage).not.toHaveBeenCalledWith("pr-create", expect.anything());
    // pr-merge itself is never pre-empted.
    expect(mockState.skipStage).not.toHaveBeenCalledWith("pr-merge", expect.anything());
    expect(mockState.completeStage).not.toHaveBeenCalledWith("pr-merge");
    // The PR is recorded on the run so downstream consumers can name it.
    expect(mockState.setMeta).toHaveBeenCalledWith({ pr_number: 1447 });
  });

  it("does not skip anything when no PR context exists (the ordinary new issue)", async () => {
    withPrContext(null);

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  describe("resume at feature-validate from preserved work (#2730)", () => {
    function stubResumePoint(orchestrator: HeadlessOrchestrator, reason: string | null) {
      return vi
        .spyOn(
          orchestrator as unknown as { preservedResumePoint(n: number): Promise<string | null> },
          "preservedResumePoint"
        )
        .mockResolvedValue(reason);
    }

    it("skips planning and dev, and leaves feature-validate to run", async () => {
      withPrContext(null);
      const { service, stages } = createGoFaithfulStateService(1431);
      const orchestrator = new HeadlessOrchestrator(service, logger, { contextFileWaitMs: 0 });
      stubResumePoint(orchestrator, "restored preserved work");

      await detect(orchestrator, 1431);

      expect(stages["issue-pickup"]?.status).toBe("skipped");
      expect(stages["feature-planning"]?.status).toBe("skipped");
      expect(stages["feature-dev"]?.status).toBe("skipped");
      expect(stages["feature-validate"]).toBeUndefined();
    });

    it("runs every stage when the binary says no: nothing proves the work survived", async () => {
      withPrContext(null);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      stubResumePoint(orchestrator, null);

      await detect(orchestrator, 1431);

      expect(mockState.skipStage).not.toHaveBeenCalled();
    });

    it("does not ask when state already remembers dev completing", async () => {
      withPrContext(null);
      const state = { ...freshState(1431), stages: { "feature-dev": { status: "complete" } } };
      const { orchestrator, mockState } = makeOrchestrator(state);
      const point = stubResumePoint(orchestrator, "1 commit(s) beyond origin/main");

      await detect(orchestrator, 1431);

      expect(point).not.toHaveBeenCalled();
      expect(mockState.skipStage).not.toHaveBeenCalled();
    });

    it("an open PR still wins: pr-merge is the later resume point", async () => {
      withPrContext({ pr_number: 1447, status: "open", ac_fingerprint: FP });
      const { orchestrator } = makeOrchestrator(freshState(1431));
      const point = stubResumePoint(orchestrator, "restored preserved work");

      await detect(orchestrator, 1431);

      expect(point).not.toHaveBeenCalled();
    });
  });

  it("does not skip anything when the recorded PR is closed", async () => {
    withPrContext({ issue_number: 1431, pr_number: 1447, status: "closed" });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  describe("pr-{N}.json outlives the merge (Issue #2878)", () => {
    const recorded = { issue_number: 1431, pr_number: 1447, status: "open", ac_fingerprint: FP };

    it("re-plans a merged PR and retires the delivered run's files", async () => {
      withPrContext(recorded);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      stubForgeState(orchestrator, "MERGED");
      stubIssueOpen(orchestrator, true);
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(mockState.completeStage).not.toHaveBeenCalled();
      const removed = vi.mocked(rmSync).mock.calls.map(([p]) => String(p));
      expect(removed.some((p) => p.endsWith("pr-1431.json"))).toBe(true);
      expect(removed.some((p) => p.endsWith("dev-1431.json"))).toBe(true);
      expect(
        (orchestrator as unknown as { replayStages: Set<string> }).replayStages.has("issue-pickup")
      ).toBe(true);
    });

    it("finishes at pr-merge when the merge closed the issue", async () => {
      withPrContext(recorded);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      stubForgeState(orchestrator, "MERGED");
      stubIssueOpen(orchestrator, false);
      await detect(orchestrator, 1431);
      expect(mockState.completeStage).toHaveBeenCalledWith("pr-create");
      const removed = vi.mocked(rmSync).mock.calls.map(([p]) => String(p));
      expect(removed.some((p) => p.endsWith("dev-1431.json"))).toBe(false);
    });

    it("keeps the files of a PR closed without merging", async () => {
      withPrContext(recorded);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      stubForgeState(orchestrator, "CLOSED");
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(rmSync).not.toHaveBeenCalled();
      expect(
        (orchestrator as unknown as { replayStages: Set<string> }).replayStages.has("issue-pickup")
      ).toBe(true);
    });

    it("runs every stage and keeps the files when the forge cannot answer", async () => {
      withPrContext(recorded);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      stubForgeState(orchestrator, null);
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(rmSync).not.toHaveBeenCalled();
      expect(
        (orchestrator as unknown as { replayStages: Set<string> }).replayStages.has("issue-pickup")
      ).toBe(true);
    });

    describe("state remembers pr-create completing", () => {
      const replayed = (orchestrator: HeadlessOrchestrator) =>
        (orchestrator as unknown as { replayStages: Set<string> }).replayStages;
      const resumed = (orchestrator: HeadlessOrchestrator) =>
        (orchestrator as unknown as { resumedAtPrMerge: boolean }).resumedAtPrMerge;

      function remembered() {
        const state = freshState(1431) as unknown as {
          stages: Record<string, { status: string }>;
        };
        state.stages["pr-create"] = { status: "complete" };
        return state;
      }

      it("resumes at pr-merge only when the forge reads the PR open", async () => {
        withPrContext(recorded);
        const { orchestrator } = makeOrchestrator(remembered());
        await detect(orchestrator, 1431);
        expect(resumed(orchestrator)).toBe(true);
        expect(replayed(orchestrator).size).toBe(0);
        expect(rmSync).not.toHaveBeenCalled();
      });

      it("retires a merged PR on an open issue and queues every stage", async () => {
        withPrContext(recorded);
        const { orchestrator } = makeOrchestrator(remembered());
        stubForgeState(orchestrator, "MERGED");
        stubIssueOpen(orchestrator, true);
        await detect(orchestrator, 1431);
        expect(resumed(orchestrator)).toBe(false);
        expect(replayed(orchestrator).has("feature-planning")).toBe(true);
        expect(replayed(orchestrator).has("pr-merge")).toBe(true);
        const removed = vi.mocked(rmSync).mock.calls.map(([p]) => String(p));
        expect(removed.some((p) => p.endsWith("pr-1431.json"))).toBe(true);
        expect(removed.some((p) => p.endsWith("dev-1431.json"))).toBe(true);
      });

      it("finishes at pr-merge when the merge closed the issue (#500)", async () => {
        withPrContext(recorded);
        const { orchestrator } = makeOrchestrator(remembered());
        stubForgeState(orchestrator, "MERGED");
        stubIssueOpen(orchestrator, false);
        await detect(orchestrator, 1431);
        expect(resumed(orchestrator)).toBe(true);
        expect(replayed(orchestrator).size).toBe(0);
        expect(rmSync).not.toHaveBeenCalled();
      });

      it.each([
        ["closed", "CLOSED", recorded],
        ["unreadable", null, recorded],
        ["unnamed", "OPEN", { issue_number: 1431, status: "open" }],
      ])("runs every stage and keeps the files on a %s PR", async (_, forgeState, context) => {
        withPrContext(context);
        const { orchestrator } = makeOrchestrator(remembered());
        stubForgeState(orchestrator, forgeState);
        await detect(orchestrator, 1431);
        expect(resumed(orchestrator)).toBe(false);
        expect(replayed(orchestrator).has("feature-planning")).toBe(true);
        expect(rmSync).not.toHaveBeenCalled();
      });

      describe("the #500 restore of a merged PR", () => {
        function restoreMerged(orchestrator: HeadlessOrchestrator) {
          withPrContext(null);
          const assembler = (
            orchestrator as unknown as {
              contextAssembler: {
                generateDeterministicContext: (s: string, n: number) => Promise<unknown>;
              };
            }
          ).contextAssembler;
          vi.spyOn(assembler, "generateDeterministicContext").mockImplementation(async () => {
            withPrContext({ issue_number: 1431, pr_number: 1447, status: "merged" });
            return { generated: true };
          });
        }

        it("re-plans instead of finishing the run when the issue is open", async () => {
          const { orchestrator, mockState } = makeOrchestrator(remembered());
          restoreMerged(orchestrator);
          stubIssueOpen(orchestrator, true);
          await detect(orchestrator, 1431);
          expect(mockState.completeStage).not.toHaveBeenCalled();
          expect(replayed(orchestrator).has("feature-planning")).toBe(true);
          expect(rmSync).toHaveBeenCalled();
        });

        it("completes pr-merge when the issue is closed or unreadable", async () => {
          const { orchestrator, mockState } = makeOrchestrator(remembered());
          restoreMerged(orchestrator);
          stubIssueOpen(orchestrator, false);
          await detect(orchestrator, 1431);
          expect(mockState.completeStage).toHaveBeenCalledWith("pr-merge");
          expect(replayed(orchestrator).size).toBe(0);
          expect(rmSync).not.toHaveBeenCalled();
        });
      });
    });
  });

  it("does not skip anything when the PR context is unreadable", async () => {
    withPrContext("{ not json");

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("does not skip anything when the context records no PR number", async () => {
    withPrContext({ issue_number: 1431, status: "open" });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("leaves the #500 path alone when state already remembers pr-create completing", async () => {
    withPrContext({ issue_number: 1431, pr_number: 1447, status: "open" });

    const state = freshState(1431) as unknown as {
      stages: Record<string, { status: string }>;
    };
    state.stages["pr-create"] = { status: "complete" };

    const { orchestrator, mockState } = makeOrchestrator(state);
    await detect(orchestrator, 1431);

    // Resume already works on its own — nothing is re-marked.
    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  describe("acceptance criteria changed since the PR opened (Issue #2623)", () => {
    const open = { issue_number: 1431, pr_number: 1447, status: "open", ac_fingerprint: FP };

    function expectReplanned(mockState: PipelineStateService, why: string) {
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(mockState.completeStage).not.toHaveBeenCalled();
      expect(logger.info).toHaveBeenCalledWith(
        expect.stringContaining(
          `acceptance criteria changed since PR #1447 was opened — re-planning (${why})`
        ),
        expect.anything()
      );
    }

    it("re-plans when a criterion was added after the PR opened", async () => {
      withPrContext(open);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431), "sha256:six-acs");
      await detect(orchestrator, 1431);
      expectReplanned(mockState, "fingerprint mismatch");
    });

    it("re-plans when the PR context recorded no fingerprint", async () => {
      withPrContext({ issue_number: 1431, pr_number: 1447, status: "open" });
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      await detect(orchestrator, 1431);
      expectReplanned(mockState, "no fingerprint recorded");
    });

    it("re-plans when the current fingerprint cannot be computed", async () => {
      withPrContext(open);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431), null);
      await detect(orchestrator, 1431);
      expectReplanned(mockState, "fingerprint unavailable");
    });

    it("replays #2611: four criteria fingerprinted, two added since — no fast-forward", async () => {
      // The real fingerprints, as `nightgauge preflight ac-fingerprint` prints
      // them for the four-criteria body and the six-criteria body.
      withPrContext({ ...open, ac_fingerprint: "sha256:four-criteria" });
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431), "sha256:six-criteria");
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalledWith("feature-planning", expect.anything());
      expect(mockState.completeStage).not.toHaveBeenCalledWith("pr-create");
    });
  });

  describe("dispatched open PR, worktree gone (Issue #2541)", () => {
    /** Stub the deterministic pr-create restore; `writes` is what it puts on disk. */
    function stubRestore(orchestrator: HeadlessOrchestrator, writes: unknown | null) {
      const assembler = (
        orchestrator as unknown as {
          contextAssembler: {
            generateDeterministicContext: (s: string, n: number) => Promise<unknown>;
          };
        }
      ).contextAssembler;
      return vi.spyOn(assembler, "generateDeterministicContext").mockImplementation(async () => {
        if (writes === null) return { generated: false };
        withPrContext(writes);
        return { generated: true };
      });
    }

    it("restores pr-{N}.json from the dispatched PR and resumes at pr-merge", async () => {
      withPrContext(null);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1501));
      orchestrator.setDispatchedOpenPr(2526);
      const generate = stubRestore(orchestrator, {
        issue_number: 1501,
        pr_number: 2526,
        status: "open",
        ac_fingerprint: FP,
      });
      await detect(orchestrator, 1431);

      expect(generate).toHaveBeenCalledWith("pr-create", 1431);
      for (const stage of ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"]) {
        expect(mockState.skipStage).toHaveBeenCalledWith(
          stage,
          expect.stringContaining("resuming at pr-merge (#1531)")
        );
      }
      expect(mockState.completeStage).toHaveBeenCalledWith("pr-create");
      expect(mockState.setMeta).toHaveBeenCalledWith({ pr_number: 2526 });
    });

    it("makes no restore call and skips nothing without a dispatched PR", async () => {
      withPrContext(null);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      const generate = stubRestore(orchestrator, { pr_number: 1, status: "open" });
      await detect(orchestrator, 1431);

      expect(generate).not.toHaveBeenCalled();
      expect(mockState.skipStage).not.toHaveBeenCalled();
    });

    it("refuses to resume on a restored PR other than the dispatched one", async () => {
      withPrContext(null);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      orchestrator.setDispatchedOpenPr(2526);
      stubRestore(orchestrator, { issue_number: 1431, pr_number: 9999, status: "open" });
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(mockState.completeStage).not.toHaveBeenCalled();
    });

    it("runs the normal pipeline when the restore finds no open PR", async () => {
      withPrContext(null);
      const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
      orchestrator.setDispatchedOpenPr(2526);
      stubRestore(orchestrator, { issue_number: 1431, pr_number: 2526, status: "merged" });
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
      expect(mockState.completeStage).not.toHaveBeenCalled();

      stubRestore(orchestrator, null);
      await detect(orchestrator, 1431);
      expect(mockState.skipStage).not.toHaveBeenCalled();
    });

    describe("against Go's stage-booking guard (Issue #2550)", () => {
      const restoredPr = {
        schema_version: "1.0",
        issue_number: 1501,
        pr_number: 2526,
        pr_url: "https://github.com/acme/acme-core/pull/2526",
        branch: "feat/1501-example",
        base_branch: "main",
        title: "feat(#1501): example",
        status: "open",
        ac_fingerprint: FP,
        created_at: "2026-10-25T22:38:56Z",
      };

      function makeGoFaithful() {
        const { service, stages } = createGoFaithfulStateService(1501);
        const orchestrator = new HeadlessOrchestrator(service, logger, { contextFileWaitMs: 0 });
        stubFingerprint(orchestrator, FP);
        stubForgeState(orchestrator, "OPEN");
        return { orchestrator, service, stages };
      }

      function firstPending(stages: Record<string, { status: string }>) {
        return STAGE_ORDER.find(
          (s) => stages[s]?.status !== "complete" && stages[s]?.status !== "skipped"
        );
      }

      it("starts at pr-merge with neither pr-{N}.json nor validate-{N}.json on disk", async () => {
        withPrContext(null);
        const { orchestrator, stages } = makeGoFaithful();
        orchestrator.setDispatchedOpenPr(2526);
        stubRestore(orchestrator, restoredPr);
        await detect(orchestrator, 1431);

        for (const s of ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"]) {
          expect(stages[s]?.status).toBe("skipped");
        }
        expect(stages["pr-create"]?.status).toBe("complete");
        expect(firstPending(stages)).toBe("pr-merge");

        // pr-merge's prerequisite resolves to the restored pr-{N}.json, not to
        // the validate-{N}.json that is not on disk.
        const precondition = (
          orchestrator as unknown as {
            validateStagePreconditions(s: string, n: number, skipped: string[]): Error | null;
          }
        ).validateStagePreconditions("pr-merge", 1431, [
          "issue-pickup",
          "feature-planning",
          "feature-dev",
          "feature-validate",
        ]);
        expect(precondition).toBeNull();
      });

      it("begins pr-create before completing it", async () => {
        withPrContext(null);
        const { orchestrator, service } = makeGoFaithful();
        orchestrator.setDispatchedOpenPr(2526);
        stubRestore(orchestrator, restoredPr);
        await detect(orchestrator, 1431);

        const start = vi.mocked(service.startStage).mock.invocationCallOrder[0];
        const complete = vi.mocked(service.completeStage).mock.invocationCallOrder[0];
        expect(start).toBeDefined();
        expect(complete).toBeDefined();
        expect(start).toBeLessThan(complete);
      });

      it("starts at issue-pickup when there is no open PR", async () => {
        withPrContext(null);
        const { orchestrator, service, stages } = makeGoFaithful();
        await detect(orchestrator, 1431);

        expect(service.startStage).not.toHaveBeenCalled();
        expect(service.skipStage).not.toHaveBeenCalled();
        expect(service.completeStage).not.toHaveBeenCalled();
        expect(firstPending(stages)).toBe("issue-pickup");
      });
    });
  });
});
