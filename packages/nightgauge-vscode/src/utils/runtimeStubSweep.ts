/**
 * runtimeStubSweep - classify stale / cross-contaminated pipeline runtime stubs.
 *
 * Startup restore (bootstrap/services.ts, Issue #2008) scans
 * `.nightgauge/pipeline/runtime-<N>.json` files to resurrect paused runs. In a
 * multi-repo workspace, concurrent dispatch used to strand orphan stubs in the
 * WRONG repo: the Go IPC server persisted a run's first "initialized" snapshot —
 * before the run's repo slug was seeded — into the shared launch root, leaving a
 * `runtime-<N>.json` with empty `repo`/`stage` in a repo that never ran the
 * issue (Issue #307). Those stubs are never cleaned and risk zombie-run
 * restoration on the next window reload.
 *
 * This module is the pure decision layer, split out so it is unit-testable
 * without the VSCode/bootstrap surface. The Go-side fix stops NEW empty-repo
 * stubs from being written; this sweep removes any that predate the fix or slip
 * through, and deletes runtime files whose `repo` does not match the repo that
 * contains them.
 *
 * @see Issue #307 - Multi-repo concurrent run state cross-contamination
 * @see Issue #2008 - Restore paused pipeline state on activation
 */

/** The subset of runtime-<N>.json fields the sweep inspects. */
export interface RuntimeStubFields {
  repo?: string | null;
  stage?: string | null;
  issueNumber?: number;
  paused?: boolean;
}

/** Verdict for a single runtime file. */
export type RuntimeStubVerdict =
  { action: "keep" } | { action: "delete"; reason: "empty-identity" | "repo-mismatch" };

/**
 * Case-insensitive repo-slug match, tolerant of `owner/repo` vs short-name form
 * (mirrors {@link WorkspaceManager.findRepositoryByGitHub}). Deliberately
 * lenient: a false "match" merely keeps a file, whereas a false "mismatch"
 * would delete a legitimate runtime — so we only flag a mismatch when the
 * repository names genuinely differ.
 */
export function repoSlugsMatch(a: string, b: string): boolean {
  const norm = (s: string) => s.trim().toLowerCase();
  if (norm(a) === norm(b)) return true;
  const shortName = (s: string) => (s.includes("/") ? (s.split("/")[1] ?? s) : s);
  return norm(shortName(a)) === norm(shortName(b));
}

/**
 * Decide whether a runtime stub is stale cross-contamination that must be
 * ignored AND deleted at startup restore.
 *
 * Rules (in order):
 * 1. Empty `repo` OR empty `stage` → the never-cleaned "initialized" stub
 *    (`empty-identity`). This is the exact #307 signature.
 * 2. `repo` set but pointing at a DIFFERENT repo than the one whose
 *    `.nightgauge` directory contains the file → `repo-mismatch`. Skipped when
 *    `containingRepoSlug` is unknown (undefined), so an unresolvable container
 *    never causes a delete.
 * 3. Otherwise → keep (and let the caller run its paused-restore logic).
 *
 * @param runtime            Parsed runtime-<N>.json fields.
 * @param containingRepoSlug `owner/repo` (or short name) of the repo containing
 *                           this file, or undefined when it cannot be resolved.
 */
export function classifyRuntimeStub(
  runtime: RuntimeStubFields,
  containingRepoSlug?: string
): RuntimeStubVerdict {
  const repo = typeof runtime.repo === "string" ? runtime.repo.trim() : "";
  const stage = typeof runtime.stage === "string" ? runtime.stage.trim() : "";

  if (!repo || !stage) {
    return { action: "delete", reason: "empty-identity" };
  }

  if (containingRepoSlug && !repoSlugsMatch(repo, containingRepoSlug)) {
    return { action: "delete", reason: "repo-mismatch" };
  }

  return { action: "keep" };
}
