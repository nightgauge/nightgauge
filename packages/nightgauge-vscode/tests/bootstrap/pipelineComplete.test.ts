/**
 * pipelineComplete.test.ts
 *
 * Issue #500 — the `pipeline.complete` handler's four surviving behaviours were
 * pinned only by source text (`tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts`
 * asserts `toContain("recordHealthSnapshotForRun")` and friends). That pin
 * cannot go red when the behaviour breaks, only when the string disappears.
 *
 * This file imports the REAL `handleGoPipelineComplete` and executes it with
 * stub dependencies, so each behaviour is observed rather than spelled. The
 * deletion pin stays where it is — execution coverage complements it.
 */

import { describe, it, expect, vi } from "vitest";
import {
  handleGoPipelineComplete,
  type GoPipelineCompleteDeps,
} from "../../src/bootstrap/pipelineComplete";

const PAYLOAD = { issueNumber: 42, success: true, totalCostUSD: 1.5 };

/** Builds stub deps plus the shared call-order log the assertions read. */
function makeDeps(overrides: Partial<GoPipelineCompleteDeps> = {}) {
  const calls: string[] = [];
  const pipelineCompleteIssues = new Set<number>();
  const reloadResolved = { value: false };

  const dashboardHistoryReloader = vi.fn(async () => {
    calls.push("reload:start");
    // Yield, so a handler that fires the reload without awaiting it is caught:
    // recordHealthSnapshotForRun would then land between start and end.
    await Promise.resolve();
    reloadResolved.value = true;
    calls.push("reload:end");
  });

  const recordHealthSnapshotForRun = vi.fn(async (issueNumber: number, costUsd: number) => {
    calls.push(`snapshot:${issueNumber}:${costUsd}`);
  });

  const onPipelineCompleted = vi.fn(() => calls.push("upload"));

  const deps: GoPipelineCompleteDeps = {
    pipelineCompleteIssues,
    dashboardHistoryReloader,
    recordHealthSnapshotForRun,
    telemetryUploaderService: { onPipelineCompleted },
    ...overrides,
  };

  return {
    deps,
    calls,
    pipelineCompleteIssues,
    dashboardHistoryReloader,
    recordHealthSnapshotForRun,
    onPipelineCompleted,
    reloadResolved,
  };
}

describe("handleGoPipelineComplete (Issue #500)", () => {
  it("marks the issue Go-driven so the legacy finish handler skips its own write", async () => {
    const h = makeDeps();

    await handleGoPipelineComplete(h.deps, PAYLOAD);

    expect([...h.pipelineCompleteIssues]).toEqual([42]);
  });

  it("awaits the dashboard history reload before recording the health snapshot", async () => {
    const h = makeDeps();

    await handleGoPipelineComplete(h.deps, PAYLOAD);

    // The snapshot must be taken against the RELOADED history, so the reload
    // has to have settled — not merely been started — before it is recorded.
    expect(h.calls).toEqual(["reload:start", "reload:end", "snapshot:42:1.5", "upload"]);
    expect(h.recordHealthSnapshotForRun).toHaveBeenCalledWith(42, 1.5, undefined, undefined);
    expect(h.reloadResolved.value).toBe(true);
  });

  it("flushes telemetry exactly once per completion", async () => {
    const h = makeDeps();

    await handleGoPipelineComplete(h.deps, PAYLOAD);

    expect(h.onPipelineCompleted).toHaveBeenCalledTimes(1);
  });

  it("still records the snapshot and flushes telemetry when the reloader is absent", async () => {
    // The bootstrap registers this subscriber before the Dashboard exists, so
    // an early completion sees `undefined` and must not throw.
    const h = makeDeps({ dashboardHistoryReloader: undefined });

    await expect(handleGoPipelineComplete(h.deps, PAYLOAD)).resolves.toBeUndefined();

    expect(h.recordHealthSnapshotForRun).toHaveBeenCalledWith(42, 1.5, undefined, undefined);
    expect(h.onPipelineCompleted).toHaveBeenCalledTimes(1);
  });

  it("does not throw when the telemetry uploader is absent", async () => {
    const h = makeDeps({ telemetryUploaderService: undefined });

    await expect(handleGoPipelineComplete(h.deps, PAYLOAD)).resolves.toBeUndefined();

    expect([...h.pipelineCompleteIssues]).toEqual([42]);
    expect(h.recordHealthSnapshotForRun).toHaveBeenCalledWith(42, 1.5, undefined, undefined);
  });

  it("still flushes telemetry when the dashboard work throws", async () => {
    // The panel may not be open; the dashboard half is best-effort, the flush
    // is not.
    const h = makeDeps({
      recordHealthSnapshotForRun: vi.fn(async () => {
        throw new Error("panel closed");
      }),
    });

    await expect(handleGoPipelineComplete(h.deps, PAYLOAD)).resolves.toBeUndefined();

    expect(h.onPipelineCompleted).toHaveBeenCalledTimes(1);
    expect([...h.pipelineCompleteIssues]).toEqual([42]);
  });

  /**
   * #1231 — `repo` and `runId` are on the payload and were logged but never
   * forwarded, so a cross-repo run's snapshot was scored against the
   * dashboard's history and filed under the dashboard's path.
   */
  it("forwards the payload's repo and runId to the health snapshot", async () => {
    const { deps, recordHealthSnapshotForRun } = makeDeps();

    await handleGoPipelineComplete(deps, {
      issueNumber: 349,
      success: true,
      totalCostUSD: 10.85,
      repo: "acme/mobile",
      runId: "run-abc",
    });

    expect(
      recordHealthSnapshotForRun,
      "without repo the snapshot cannot be attributed to the repo that ran"
    ).toHaveBeenCalledWith(349, 10.85, "acme/mobile", "run-abc");
  });

  it("passes repo through as undefined when the payload omits it", async () => {
    const { deps, recordHealthSnapshotForRun } = makeDeps();

    await handleGoPipelineComplete(deps, {
      issueNumber: 7,
      success: true,
      totalCostUSD: 2,
    });

    // Undefined, not a substituted default: the receiver decides what an
    // unattributed run means, and it must be able to tell that it IS one.
    expect(recordHealthSnapshotForRun).toHaveBeenCalledWith(7, 2, undefined, undefined);
  });
});
