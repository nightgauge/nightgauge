/**
 * EpicQueueFilter - Pre-filter epic sub-issues before enqueue
 *
 * When the user drags an epic onto the pipeline queue, every open sub-issue
 * should not be enqueued indiscriminately. Sub-issues still in Backlog are
 * not pickup-eligible, and sub-issues with an open PR are in-review work
 * whose branch already exists on the remote — enqueuing them produces the
 * "git worktree add fatal: branch already exists" error #2992 is fixing.
 *
 * This service runs only on the drag path. Autonomous scheduling already
 * walks project-board-filtered lists via `ProjectV2.items(query: "status:...")`
 * and does not need a second filter.
 *
 * @see Issue #2992 — epic drag queues Backlog/in-review sub-issues
 */
import type { ReadyIssue } from "./ProjectBoardService";
import type { ProjectBoardService } from "./ProjectBoardService";
import { getPRForIssue, type PRInfo } from "../utils/prDetection";

/**
 * Pluggable PR lookup. Production uses `getPRForIssue`; tests inject a stub
 * so they do not shell out to `gh`.
 */
export type PRLookup = (issueNumber: number, workspaceRoot: string) => Promise<PRInfo | null>;

/**
 * A skipped sub-issue with its reason for rejection. Surfaced to the user
 * via the drag-handler toast.
 */
export interface SkippedSubIssue {
  number: number;
  /** `status` = not in eligibleStatuses, `open-pr` = PR present, `missing` = not in board cache */
  reason: "status" | "open-pr" | "missing";
  /** Short human-readable detail: actual status for `status`, PR url for `open-pr`. */
  detail?: string;
}

export interface EpicFilterResult {
  /** Sub-issues that passed both gates and should be enqueued. */
  eligible: number[];
  /** Sub-issues that were rejected, with the reason each was skipped. */
  skipped: SkippedSubIssue[];
}

export interface EpicFilterOptions {
  subIssueNumbers: number[];
  workspaceRoot: string;
  projectBoardService: Pick<ProjectBoardService, "getAllItems">;
  /** Status names considered pickup-eligible. Case-insensitive. Default `["Ready"]`. */
  eligibleStatuses?: string[];
  /** Skip sub-issues that already have an open PR. Default `true`. */
  skipIfOpenPR?: boolean;
  /** Injection point for tests. Defaults to the real `getPRForIssue`. */
  prLookup?: PRLookup;
  /** How many PR lookups to run in parallel. Default `10` to stay under `gh` rate limits. */
  concurrency?: number;
}

/**
 * Filter an epic's sub-issues down to the set that is pickup-eligible.
 *
 * The filter is conservative: a sub-issue that cannot be found in the
 * project-board cache is skipped with reason `missing`, not optimistically
 * enqueued. Guessing at eligibility is what produced the bug this fixes.
 */
export async function filterEligibleSubIssues(
  options: EpicFilterOptions
): Promise<EpicFilterResult> {
  const {
    subIssueNumbers,
    workspaceRoot,
    projectBoardService,
    eligibleStatuses = ["Ready"],
    skipIfOpenPR = true,
    prLookup = getPRForIssue,
    concurrency = 10,
  } = options;

  if (subIssueNumbers.length === 0) {
    return { eligible: [], skipped: [] };
  }

  const allowed = new Set(eligibleStatuses.map((s) => s.toLowerCase()));

  // Resolve each sub-issue's board status via `getAllItems()`.
  // On cold cache this self-populates with one GraphQL call — the same
  // lookup the board views do on first render.
  const allItems: ReadyIssue[] = await projectBoardService.getAllItems();
  const byNumber = new Map<number, ReadyIssue>(allItems.map((it) => [it.number, it]));

  // First pass: status filter. We collect the candidates that passed so that
  // PR lookup only runs on items we might still enqueue.
  const eligible: number[] = [];
  const skipped: SkippedSubIssue[] = [];
  const prCandidates: number[] = [];

  for (const n of subIssueNumbers) {
    const item = byNumber.get(n);
    if (!item) {
      skipped.push({ number: n, reason: "missing" });
      continue;
    }
    const rawStatus = (item.status ?? "").toLowerCase();
    if (!allowed.has(rawStatus)) {
      skipped.push({
        number: n,
        reason: "status",
        detail: item.status ?? "unknown",
      });
      continue;
    }
    if (skipIfOpenPR) {
      prCandidates.push(n);
    } else {
      eligible.push(n);
    }
  }

  // Second pass: PR lookup in parallel batches.
  if (skipIfOpenPR && prCandidates.length > 0) {
    for (let i = 0; i < prCandidates.length; i += concurrency) {
      const batch = prCandidates.slice(i, i + concurrency);
      const results = await Promise.all(
        batch.map(async (n) => {
          const pr = await prLookup(n, workspaceRoot).catch(() => null);
          return { number: n, pr };
        })
      );
      for (const { number, pr } of results) {
        if (pr) {
          skipped.push({ number, reason: "open-pr", detail: pr.url });
        } else {
          eligible.push(number);
        }
      }
    }
  }

  return { eligible, skipped };
}

/**
 * Format a skipped-reason breakdown for a toast message.
 *
 * @example
 *   summarizeSkipped([{number:1,reason:"status",detail:"Backlog"}, {number:2,reason:"open-pr"}])
 *   // => "Backlog: 1, open PR: 1"
 */
export function summarizeSkipped(skipped: SkippedSubIssue[]): string {
  if (skipped.length === 0) return "";
  const buckets = new Map<string, number>();
  const bump = (label: string) => buckets.set(label, (buckets.get(label) ?? 0) + 1);
  for (const s of skipped) {
    if (s.reason === "status") {
      bump(s.detail ?? "other status");
    } else if (s.reason === "open-pr") {
      bump("open PR");
    } else {
      bump("not in board cache");
    }
  }
  return Array.from(buckets.entries())
    .map(([label, count]) => `${label}: ${count}`)
    .join(", ");
}
