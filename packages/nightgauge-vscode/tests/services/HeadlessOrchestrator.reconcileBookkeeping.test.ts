// Tests for reconcileBookkeepingFromDiskState() — Issue #3450.
//
// Pre-fix `HeadlessOrchestrator.runPipeline()` could return
// `{ success: false, failedStage: undefined }` for runs that actually
// completed every stage successfully. The bug surfaced when the in-memory
// `completedStages` array missed `pipeline-finish` (or any other terminal
// stage) for reasons not tied to a real failure — e.g. a late abort signal
// between pr-merge complete and the bookend dispatch, an isPaused trip after
// pr-merge complete, or an unhandled exception in an after-stage hook that
// short-circuits the loop without setting `failedStage`.
//
// Downstream, ConcurrentPipelineManager.processSlot then routed the slot
// through haltQueueOnSlotFailure as "issue #N failed at unknown",
// incrementing `lifetimeIssueFailures` and pausing autonomous mode on a run
// where the PR was already merged and the issue closed (user reported a
// $21.26 successful run misclassified this way).
//
// The fix is a defensive reconciliation that trusts state.json (the
// authoritative on-disk source) over the in-memory pushes: if every
// `STAGE_ORDER` stage shows a terminal status on disk and `failedStage` is
// undefined, the run truly succeeded — promote `success: true` and
// reconcile the in-memory arrays. These tests exercise that helper
// directly.

import { describe, expect, it } from "vitest";

import { reconcileBookkeepingFromDiskState } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStage } from "@nightgauge/sdk";

const STAGE_ORDER: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/** Build a fully-terminal `stages` map for state.json mocking. */
function allCompleteOnDisk(): Record<string, { status: string }> {
  return Object.fromEntries(STAGE_ORDER.map((s) => [s, { status: "complete" }]));
}

describe("reconcileBookkeepingFromDiskState (Issue #3450)", () => {
  it("recovers pipeline-finish when state.json says all stages complete but in-memory missed the bookend", () => {
    // The exact scenario from issue #3375: every stage (incl. pipeline-finish)
    // is `complete` on disk, but the in-memory `completedStages` stops after
    // pr-merge. Pre-fix this returned `success: false, failedStage: undefined`.
    const inMemory = {
      completedStages: [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, allCompleteOnDisk(), STAGE_ORDER);

    expect(result.changed).toBe(true);
    expect(result.recovered).toEqual(["pipeline-finish"]);
    expect(result.completedStages).toContain("pipeline-finish");
    // Total terminal count equals STAGE_ORDER.length — runPipeline's
    // pipelineComplete check now evaluates to true.
    expect(
      result.completedStages.length + result.skippedStages.length + result.deferredStages.length
    ).toBe(STAGE_ORDER.length);
  });

  it("is a no-op when in-memory bookkeeping already matches state.json", () => {
    // Healthy path — nothing missing. The helper should not duplicate or
    // mutate anything.
    const inMemory = {
      completedStages: [...STAGE_ORDER] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, allCompleteOnDisk(), STAGE_ORDER);

    expect(result.changed).toBe(false);
    expect(result.recovered).toEqual([]);
    expect(result.completedStages).toHaveLength(STAGE_ORDER.length);
  });

  it("preserves skipped/deferred classifications from disk when recovering", () => {
    // routing decision skipped feature-dev, pipeline finished, but in-memory
    // dropped pipeline-finish. The recovered stage should be classified
    // according to its disk status, not blindly pushed onto completedStages.
    const stagesOnDisk = {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "skipped" },
      "feature-validate": { status: "skipped" },
      "pr-create": { status: "complete" },
      "pr-merge": { status: "deferred" }, // deferMerge mode
      "pipeline-finish": { status: "deferred" },
    };
    const inMemory = {
      completedStages: ["pipeline-start", "issue-pickup", "feature-planning"] as PipelineStage[],
      skippedStages: ["feature-dev"] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, stagesOnDisk, STAGE_ORDER);

    expect(result.changed).toBe(true);
    expect(result.recovered).toEqual([
      "feature-validate",
      "pr-create",
      "pr-merge",
      "pipeline-finish",
    ]);
    expect(result.completedStages).toContain("pr-create");
    expect(result.skippedStages).toContain("feature-validate");
    expect(result.deferredStages).toEqual(expect.arrayContaining(["pr-merge", "pipeline-finish"]));
  });

  it("does NOT reconcile when any stage is still running on disk (preserves real abort/pause handling)", () => {
    // Pipeline was actually killed mid-pr-merge — state.json shows pr-merge
    // running, pipeline-finish pending. Reconciliation must not paper over
    // this as success.
    const stagesOnDisk = {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "complete" },
      "feature-validate": { status: "complete" },
      "pr-create": { status: "complete" },
      "pr-merge": { status: "running" },
      "pipeline-finish": { status: "pending" },
    };
    const inMemory = {
      completedStages: [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
      ] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, stagesOnDisk, STAGE_ORDER);

    expect(result.changed).toBe(false);
    expect(result.recovered).toEqual([]);
    // Critical: success classification must still fail upstream.
    expect(result.completedStages.length).toBe(6);
  });

  it("does NOT reconcile when a stage is failed on disk (preserves real failures)", () => {
    // feature-dev failed for real. The caller's failedStage will be set,
    // but defense-in-depth: even if the caller forgot, this helper must not
    // promote success on a state with `failed` status anywhere.
    const stagesOnDisk = {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "complete" },
      "feature-planning": { status: "complete" },
      "feature-dev": { status: "failed" },
      "feature-validate": { status: "pending" },
      "pr-create": { status: "pending" },
      "pr-merge": { status: "pending" },
      "pipeline-finish": { status: "pending" },
    };
    const inMemory = {
      completedStages: ["pipeline-start", "issue-pickup", "feature-planning"] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, stagesOnDisk, STAGE_ORDER);

    expect(result.changed).toBe(false);
    expect(result.recovered).toEqual([]);
  });

  it("does not mutate the caller's input arrays", () => {
    // Pure-function contract: the helper must return new arrays, never
    // mutate the caller's references. This guards against accidental
    // shared-state bugs in HeadlessOrchestrator's mutable closure.
    const inMemory = {
      completedStages: [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };
    const completedSnapshot = [...inMemory.completedStages];

    reconcileBookkeepingFromDiskState(inMemory, allCompleteOnDisk(), STAGE_ORDER);

    expect(inMemory.completedStages).toEqual(completedSnapshot);
    expect(inMemory.completedStages).not.toContain("pipeline-finish");
  });

  it("handles an empty stages-on-disk map without throwing", () => {
    // Defensive: if state.json is missing or its stages map is empty, the
    // helper must not crash. allTerminalOnDisk evaluates to false →
    // no reconciliation, no exception.
    const inMemory = {
      completedStages: [] as PipelineStage[],
      skippedStages: [] as PipelineStage[],
      deferredStages: [] as PipelineStage[],
    };

    const result = reconcileBookkeepingFromDiskState(inMemory, {}, STAGE_ORDER);

    expect(result.changed).toBe(false);
    expect(result.recovered).toEqual([]);
  });
});
