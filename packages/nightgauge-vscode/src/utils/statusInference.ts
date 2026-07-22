/**
 * Status Inference — resolves work item status when board status is absent.
 *
 * Implements the 4-step inference chain:
 *   1. Pipeline execution state (issue actively running → "In progress")
 *   2. Board status (if present and valid → return as-is)
 *   3. Label fallback (status:* labels → mapped status)
 *   4. Default readiness rules (blocked → "Backlog", open → "Ready", closed → "Done")
 *
 * This is a pure logic utility — no vscode imports, no side effects.
 * Placement in src/utils/ (not src/services/) matches this pattern.
 *
 * @module utils/statusInference
 * @see Issue #2570
 */

import type { ProjectBoardStatus } from "../views/dashboard/ProjectBoardTypes";
import type { PipelineState } from "../services/PipelineStateService";
import { extractStatusLabel, mapStatusLabel, isStatusValue } from "./projectFieldMapping";

/**
 * Minimal issue shape required for status inference.
 */
export interface StatusInferenceInput {
  /** Issue number — used to match against pipeline state */
  number: number;
  /** Labels for status:* label fallback */
  labels: string[];
  /** Current board status (may be undefined for repo-only issues) */
  status?: string;
  /** Blocking dependency relationships */
  blockedBy?: Array<{ state: string }>;
  /** GitHub issue state */
  issueState?: "OPEN" | "CLOSED";
}

/**
 * Infer the ProjectBoardStatus for a work item.
 *
 * Call this when WorkItem.status is undefined or empty (repo-only issues).
 * If board status is already present and valid, use it directly — do not call
 * this function for already-resolved board items.
 *
 * Inference order:
 * 1. Pipeline execution state: actively running → "In progress"
 * 2. Board status: if present and valid (non-empty), return as-is
 * 3. Label fallback: status:* label → mapped status
 * 4. Default readiness rules:
 *    - CLOSED → "Done"
 *    - blocked by open issue → "Backlog"
 *    - open + unblocked → "Ready" (default)
 *
 * Note: Pipeline state is not available in GitHubIssuesAdapter at construction
 * time (Step 1 is skipped when pipelineState is undefined). CompositeAdapter
 * (#2428) can re-evaluate with pipeline state if needed.
 *
 * @param item - Minimal issue shape needed for inference
 * @param pipelineState - Active pipeline execution state (undefined/null if unavailable)
 * @returns Inferred ProjectBoardStatus, or undefined if no inference is possible
 */
export function inferWorkItemStatus(
  item: StatusInferenceInput,
  pipelineState?: PipelineState | null
): ProjectBoardStatus | undefined {
  // Step 1 — Pipeline execution state: actively running → "In progress"
  if (pipelineState && pipelineState.issue_number === item.number) {
    const isRunning = Object.values(pipelineState.stages).some((s) => s.status === "running");
    if (isRunning) {
      return "In progress";
    }
  }

  // Step 2 — Board status: if present and valid (non-empty), use it directly
  if (item.status && isStatusValue(item.status) && item.status !== "") {
    return item.status as ProjectBoardStatus;
  }

  // Step 3 — Label fallback: scan labels for status:* prefix
  const statusLabel = extractStatusLabel(item.labels);
  if (statusLabel) {
    const mapped = mapStatusLabel(statusLabel);
    if (mapped) {
      return mapped as ProjectBoardStatus;
    }
  }

  // Step 4 — Default readiness rules
  // Closed issue → Done
  if (item.issueState === "CLOSED") {
    return "Done";
  }

  // Blocked by any open issue → Backlog (not ready to start)
  const isBlockedByOpen = (item.blockedBy ?? []).some((b) => b.state === "OPEN");
  if (isBlockedByOpen) {
    return "Backlog";
  }

  // Open, unblocked → Ready (default for repo-only issues)
  return "Ready";
}
