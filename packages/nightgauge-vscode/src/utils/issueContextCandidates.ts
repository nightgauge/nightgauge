/**
 * issueContextCandidates — the TypeScript half of `execution.IssueContextCandidates`.
 *
 * THERE ARE SEVERAL WORKTREE LAYOUTS AND EVERY SINGLE-ROOT READER KNOWS ABOUT NONE.
 *
 *   - The Go manager writes `<worktree base>/{repoName}-issue-N`, outside the
 *     working tree (ADR-024 § 9, #2038). The base comes from the binary, the one
 *     resolver, via `worktreeLocation` (the leaf carries the repo name so two
 *     repos' issue #N cannot collide in one base).
 *   - Before #2038 it wrote `<repoRoot>/.nightgauge/worktrees/{repoName}-issue-N`;
 *     a run that began there keeps that worktree until it ends.
 *   - The VSCode extension's WorktreeManager now writes the same
 *     `<worktree base>/{repoName}-issue-N`; before #2038 it wrote
 *     `<repoRoot>/.worktrees/issue-N`.
 *   - A run that never took a worktree leaves the file at the repo root.
 *
 * Go fixed this for its own readers in #994 with a single shared list, on the
 * reasoning that two readers each knowing half the layouts is how one corpus
 * field acquired two meanings. The TypeScript side was never ported: the
 * Knowledge view read the repo root ONLY, so on the scheduler path — where the
 * context file is written inside the worktree — it found nothing, every time,
 * and rendered "No knowledge base scaffolded for this issue" (#1206).
 *
 * Callers must tolerate a missing file at every candidate. The context is
 * written by the issue-pickup stage, so before that stage runs none of these
 * exist.
 *
 * Kept byte-compatible with the Go list; `internal/execution/issue_context_paths.go`
 * is canonical and this mirrors it.
 *
 * @see internal/execution/issue_context_paths.go
 * @see Issue #994, #1206
 */

import { pipelineStateDir, isUsableWorkspaceRoot } from "./cloneLayout";
import * as path from "node:path";
import { cachedWorktreeBase, goWorktreeDirName, LEGACY_GO_WORKTREE_BASE } from "./worktreeLocation";

/** A run's issue-context file name inside its root's pipeline state dir. */
function issueContextFileName(issueNumber: number): string {
  return `issue-${issueNumber}.json`;
}

/**
 * Every path a run's `issue-{N}.json` may live at, most-specific first.
 *
 * @param repoRoot     the workspace root
 * @param worktreeDir  the run's actual worktree when known; "" when not
 * @param repo         "owner/name" or a bare name; "" skips the Go layout
 * @param worktreeBase the Go worktree base for `repoRoot`; defaults to the
 *                     binary's cached answer (`primeWorktreeBase` fills it)
 */
export function issueContextCandidates(
  repoRoot: string,
  worktreeDir: string,
  repo: string,
  issueNumber: number,
  worktreeBase: string | undefined = cachedWorktreeBase(repoRoot)
): string[] {
  const fileName = issueContextFileName(issueNumber);
  const roots: string[] = [];

  if (worktreeDir) {
    roots.push(worktreeDir);
  }
  if (repoRoot) {
    // "owner/name" → "name". The Go manager's leaf uses the bare repo name.
    const leaf = goWorktreeDirName(repo, issueNumber);
    if (leaf) {
      if (worktreeBase) roots.push(path.join(worktreeBase, leaf));
      roots.push(path.join(repoRoot, LEGACY_GO_WORKTREE_BASE, leaf));
    }
    roots.push(path.join(repoRoot, ".worktrees", `issue-${issueNumber}`));
    roots.push(repoRoot);
  }

  const seen = new Set<string>();
  const paths: string[] = [];
  for (const root of roots) {
    // A root the layout helper refuses (relative, or not in a git repository)
    // is skipped rather than thrown from a best-effort lookup. Every root of
    // one clone resolves to the same directory, so the list collapses to one
    // path per clone (ADR-024 § 7).
    if (!isUsableWorkspaceRoot(root)) continue;
    const p = path.join(pipelineStateDir(root), fileName);
    if (seen.has(p)) continue;
    seen.add(p);
    paths.push(p);
  }
  return paths;
}

/**
 * The same list for a sibling file in `pipelineStateDir(root)/` — `planning-N.json`
 * lives beside `issue-N.json` and moves with it.
 */
export function pipelineFileCandidates(
  repoRoot: string,
  worktreeDir: string,
  repo: string,
  issueNumber: number,
  fileName: string,
  worktreeBase: string | undefined = cachedWorktreeBase(repoRoot)
): string[] {
  return issueContextCandidates(repoRoot, worktreeDir, repo, issueNumber, worktreeBase).map((p) =>
    path.join(path.dirname(p), fileName)
  );
}
