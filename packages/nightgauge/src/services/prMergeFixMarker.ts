/**
 * The pr-merge fix hand-back marker (#2504, #2833).
 *
 * When the pr-merge skill fixes a CI failure it pushes the fix, writes
 * `pr-merge-fix-{N}.json` to the clone's pipeline state directory and exits 0
 * without waiting for CI. The orchestrator consumes the marker and re-enters
 * the deterministic runner, which owns the bounded CI wait and the merge. The
 * Go scheduler's twin is `consumePRMergeFixMarker` in
 * `internal/orchestrator/scheduler.go`.
 */

import * as fs from "fs";
import * as path from "path";
import { pipelineStateDir } from "../utils/cloneLayout";

/** The only `result` value that hands the stage back to the runner. */
export const PR_MERGE_FIX_PUSHED = "fix-pushed";

export interface PrMergeFixMarker {
  result: string;
  pr_number?: number;
  head_sha?: string;
  attempt?: number;
}

/** Where the skill writes the marker for `issueNumber`, from the run's root. */
export function prMergeFixMarkerPath(root: string, issueNumber: number): string {
  return path.join(pipelineStateDir(root), `pr-merge-fix-${issueNumber}.json`);
}

/** Removes a marker left by an earlier run, so it is never acted on. */
export async function clearStalePrMergeFixMarker(markerPath: string): Promise<void> {
  try {
    await fs.promises.unlink(markerPath);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "ENOENT") throw err;
  }
}

/**
 * Reads and deletes the marker. Returns it when it records a pushed fix;
 * returns null when it is absent (silently) or unusable (after `warn`). A
 * present marker is always deleted, so it is acted on at most once.
 */
export async function consumePrMergeFixMarker(
  markerPath: string,
  warn: (message: string, meta: Record<string, unknown>) => void
): Promise<PrMergeFixMarker | null> {
  let raw: string;
  try {
    raw = await fs.promises.readFile(markerPath, "utf-8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
    warn("pr-merge fix marker unreadable — ignoring it", { path: markerPath, err: String(err) });
    return null;
  }
  await clearStalePrMergeFixMarker(markerPath).catch((err) =>
    warn("pr-merge fix marker could not be deleted", { path: markerPath, err: String(err) })
  );
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    warn("pr-merge fix marker is not valid JSON — ignoring it", {
      path: markerPath,
      err: String(err),
    });
    return null;
  }
  const marker = parsed as PrMergeFixMarker | null;
  if (!marker || typeof marker !== "object" || marker.result !== PR_MERGE_FIX_PUSHED) {
    warn("pr-merge fix marker does not record a pushed fix — ignoring it", {
      path: markerPath,
      result: (marker as { result?: unknown } | null)?.result,
    });
    return null;
  }
  return marker;
}
