/**
 * pipelineComplete.ts
 *
 * The Go scheduler's `pipeline.complete` post-run work, extracted out of the
 * `registerServices()` bootstrap (Issue #500).
 *
 * It used to live as an inline `ipc.on("pipeline.complete", …)` closure inside
 * `services.ts`, which meant its four live behaviours could only be guarded by
 * asserting on the handler's *source text* (`tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts`).
 * That pin is the right idiom for #141's deletion — it stops the deleted
 * duplicate run-record writer being reintroduced — but it is a coverage
 * illusion for the behaviours that survived: every one of them could break
 * while the strings stayed put. Taking the dependencies explicitly makes the
 * handler callable from a test with stubs, so the behaviours are executed
 * rather than spelled.
 *
 * The Go scheduler remains the authoritative writer of the run's history
 * record (`Scheduler.recordV2History`, into the run's OWN repo root). Nothing
 * here appends a run record — see #141 in the test named above for why a second
 * writer keyed off bootstrap-level shared state cannot be made correct.
 */

/** The `pipeline.complete` event payload emitted by the Go scheduler. */
export interface GoPipelineCompletePayload {
  repo?: string;
  runId?: string;
  issueNumber: number;
  success: boolean;
  totalCostUSD: number;
}

/**
 * Everything the handler touches, passed in rather than closed over.
 *
 * `dashboardHistoryReloader` and `telemetryUploaderService` are genuinely
 * optional at runtime: the bootstrap assigns them later than it registers this
 * subscriber, so a completion that lands early sees them unset and must be a
 * no-op rather than a throw.
 */
export interface GoPipelineCompleteDeps {
  /** Marks the issue Go-driven so the legacy finish handler skips its own write. */
  pipelineCompleteIssues: Set<number>;
  /** Refreshes the dashboard's history view; absent until the Dashboard exists. */
  dashboardHistoryReloader?: (() => Promise<void>) | null;
  /** `Dashboard.recordHealthSnapshotForRun`, bound to the dashboard singleton. */
  recordHealthSnapshotForRun: (
    issueNumber: number,
    costUsd: number,
    repo?: string,
    runId?: string
  ) => Promise<void> | void;
  /** Telemetry uploader; absent until it is constructed (or when disabled). */
  telemetryUploaderService?: { onPipelineCompleted(): void } | null;
  /** Optional structured logger. */
  logger?: { info(message: string, data?: object): void };
}

/**
 * Post-run work only the extension can do, for a run the Go scheduler has
 * already recorded.
 *
 * Order matters: the dashboard's history reload is awaited *before* the health
 * snapshot is recorded, so the snapshot is taken against the reloaded history
 * rather than the pre-run view. The snapshot exists for concurrent slots
 * (issue 2245), which per-slot `PipelineStateServices` never trigger on the
 * singleton.
 *
 * The dashboard work is best-effort — the panel may not be open — but the
 * telemetry flush must happen either way, so it sits outside the try. That
 * flush is redundant and idempotent: the active-run counter and cadence are
 * driven by the pipeline lifecycle IPC wiring (`onRunStarted`/`onRunCompleted`),
 * so `onPipelineCompleted()` deliberately does NOT touch `activeRunCount` —
 * no double-decrement (#234).
 */
export async function handleGoPipelineComplete(
  deps: GoPipelineCompleteDeps,
  data: unknown
): Promise<void> {
  const d = data as GoPipelineCompletePayload;

  deps.pipelineCompleteIssues.add(d.issueNumber);

  try {
    await deps.dashboardHistoryReloader?.();
    // `d.repo` and `d.runId` were on the payload all along and were logged but
    // never forwarded, so the snapshot was scored against the dashboard's
    // history and filed under the dashboard's path however cross-repo the run
    // was (#1231).
    await deps.recordHealthSnapshotForRun(d.issueNumber, d.totalCostUSD, d.repo, d.runId);
    deps.logger?.info("Pipeline complete: dashboard refreshed", {
      repo: d.repo,
      runId: d.runId,
      issueNumber: d.issueNumber,
      costUsd: d.totalCostUSD,
    });
  } catch {
    // Non-critical — panel may not be open.
  }

  deps.telemetryUploaderService?.onPipelineCompleted();
}
