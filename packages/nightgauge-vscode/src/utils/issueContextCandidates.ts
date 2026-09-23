/**
 * issueContextCandidates — the TypeScript half of `execution.IssueContextCandidates`.
 *
 * THERE ARE TWO WORKTREE LAYOUTS AND EVERY SINGLE-ROOT READER KNOWS ABOUT NONE.
 *
 *   - The Go manager writes `<repoRoot>/.nightgauge/worktrees/{repoName}-issue-N`
 *     (the leaf carries the repo name so two repos' issue #N cannot collide in
 *     one workspace).
 *   - The VSCode extension writes `<repoRoot>/.worktrees/issue-N`.
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

import {
  pipelineStateDir,
  RELATIVE_PIPELINE_STATE_DIR,
  isUsableWorkspaceRoot,
} from "./cloneLayout";
import * as path from "node:path";

/** A run's issue-context file name inside its root's pipeline state dir. */
function issueContextFileName(issueNumber: number): string {
  return `issue-${issueNumber}.json`;
}

/**
 * Where every writer puts a run's issue context, relative to its own root.
 * Display only: resolve against a root with {@link issueContextCandidates},
 * which goes through `pipelineStateDir`.
 */
export function issueContextRelPath(issueNumber: number): string {
  return path.join(RELATIVE_PIPELINE_STATE_DIR, issueContextFileName(issueNumber));
}

/**
 * Every path a run's `issue-{N}.json` may live at, most-specific first.
 *
 * @param repoRoot    the workspace root
 * @param worktreeDir the run's actual worktree when known; "" when not
 * @param repo        "owner/name" or a bare name; "" skips the Go layout
 */
export function issueContextCandidates(
  repoRoot: string,
  worktreeDir: string,
  repo: string,
  issueNumber: number
): string[] {
  const fileName = issueContextFileName(issueNumber);
  const roots: string[] = [];

  if (worktreeDir) {
    roots.push(worktreeDir);
  }
  if (repoRoot) {
    // "owner/name" → "name". The Go manager's leaf uses the bare repo name.
    const slash = repo.lastIndexOf("/");
    const repoName = slash >= 0 ? repo.slice(slash + 1) : repo;
    if (repoName) {
      roots.push(
        path.join(repoRoot, ".nightgauge", "worktrees", `${repoName}-issue-${issueNumber}`)
      );
    }
    roots.push(path.join(repoRoot, ".worktrees", `issue-${issueNumber}`));
    roots.push(repoRoot);
  }

  const seen = new Set<string>();
  const paths: string[] = [];
  for (const root of roots) {
    // A root the layout helper refuses (relative) would resolve against the
    // host's cwd; skip it rather than throw from a best-effort lookup.
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
  fileName: string
): string[] {
  return issueContextCandidates(repoRoot, worktreeDir, repo, issueNumber).map((p) =>
    path.join(path.dirname(p), fileName)
  );
}
