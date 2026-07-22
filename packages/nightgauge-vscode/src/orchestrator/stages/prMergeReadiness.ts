/**
 * prMergeReadiness — pure decision core for the deterministic-first pr-merge
 * path (Issue #297).
 *
 * pr-merge starts immediately after pr-create, so on repos whose CI takes
 * minutes (bowlsheet ~10 min) the PR's first snapshot is BLOCKED/UNSTABLE with
 * still-running checks. The legacy TS orchestrator has no deterministic pr-merge
 * — it always ran the LLM skill, which "won" only by babysitting CI for ~10 min
 * at ~$3–4.44/run — and the existing `tryDeterministicMergeFallback` declines
 * unless the merge state is already CLEAN, so it too gives up on pending CI.
 *
 * This classifier lets the deterministic path WAIT for in-flight CI to finish
 * rather than punt: it distinguishes a PR that is merely waiting on checks
 * (`pending` → poll again) from one with a real, non-self-resolving blocker
 * (`blocked` → punt to the LLM). It mirrors the Go `stages.Decide` /
 * `mergeBlockedByPendingCI` matrix (internal/orchestrator/stages/prmerge.go) so
 * both producers agree.
 */

/** Projection of `gh pr view --json state,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup`. */
export interface MergeSnapshot {
  /** OPEN | MERGED | CLOSED */
  state: string;
  /** MERGEABLE | CONFLICTING | UNKNOWN */
  mergeable: string;
  /** CLEAN | DIRTY | BLOCKED | UNSTABLE | BEHIND | DRAFT | HAS_HOOKS | UNKNOWN */
  mergeStateStatus: string;
  /** APPROVED | REVIEW_REQUIRED | CHANGES_REQUESTED | "" (no review required) */
  reviewDecision?: string;
  /** statusCheckRollup rows; `conclusion` is "" / UNKNOWN / PENDING while in-flight. */
  checks: Array<{ name: string; conclusion: string }>;
}

export type MergeReadiness =
  | { kind: "merged" } // already merged — treat as deterministic success
  | { kind: "ready" } // clean + mergeable + green → issue the merge now
  | { kind: "pending" } // only in-flight CI is blocking → wait and re-poll
  | { kind: "blocked"; reason: string }; // structural blocker → punt to the LLM path

/** A check conclusion that means the check has not concluded yet. */
function isPendingConclusion(conclusion: string): boolean {
  const c = conclusion.toUpperCase();
  return c === "" || c === "PENDING" || c === "UNKNOWN" || c === "IN_PROGRESS" || c === "QUEUED";
}

/**
 * classifyMergeReadiness reduces a snapshot to a merge decision. The ordering
 * matches the Go decision matrix: a hard blocker (conflict, failed check,
 * blocking review, structural dirty state) always wins over a "pending" verdict,
 * so the runner never waits on a PR that cannot self-resolve.
 */
export function classifyMergeReadiness(snap: MergeSnapshot): MergeReadiness {
  if (snap.state === "MERGED") {
    return { kind: "merged" };
  }
  if (snap.state !== "OPEN") {
    return { kind: "blocked", reason: `pr-state-${snap.state.toLowerCase()}` };
  }
  if (snap.mergeable !== "MERGEABLE") {
    return { kind: "blocked", reason: `not-mergeable: ${snap.mergeable}` };
  }

  // A failed/errored check is a hard blocker regardless of merge-state — waiting
  // will not turn a red check green.
  const failed = snap.checks.find(
    (c) => c.conclusion.toUpperCase() === "FAILURE" || c.conclusion.toUpperCase() === "ERROR"
  );
  if (failed) {
    return { kind: "blocked", reason: `failed-ci-checks: ${failed.name}` };
  }

  // A blocking review will not resolve by waiting on CI.
  if (snap.reviewDecision === "REVIEW_REQUIRED" || snap.reviewDecision === "CHANGES_REQUESTED") {
    return { kind: "blocked", reason: `review-not-approved: ${snap.reviewDecision}` };
  }

  if (snap.mergeStateStatus === "CLEAN") {
    return { kind: "ready" };
  }

  // BLOCKED/UNSTABLE with at least one in-flight check is exactly the
  // "waiting on CI" case pr-merge hits right after pr-create.
  if (snap.mergeStateStatus === "BLOCKED" || snap.mergeStateStatus === "UNSTABLE") {
    if (snap.checks.some((c) => isPendingConclusion(c.conclusion))) {
      return { kind: "pending" };
    }
  }

  // DIRTY (conflict), BEHIND, DRAFT, HAS_HOOKS, or BLOCKED/UNSTABLE with no
  // pending checks — structural, will not clear by waiting.
  return { kind: "blocked", reason: `dirty-merge-state: ${snap.mergeStateStatus}` };
}
