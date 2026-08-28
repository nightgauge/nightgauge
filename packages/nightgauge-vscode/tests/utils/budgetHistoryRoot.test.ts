/**
 * Issue #1017: the pre-flight estimate ran ~2x under actual, against an
 * all-sizes p75 computed from FOUR samples — in a repo whose corpus holds
 * eighty-seven costed issues.
 *
 * The four-sample cohort is the tell. `resolveMainRepoRoot` strips a worktree
 * marker to find the repo whose execution history to read, and it knew about
 * ONE of the two layouts this project writes:
 *
 *   - VSCode extension: `<repoRoot>/.worktrees/issue-N`
 *   - Go manager:       `<repoRoot>/.nightgauge/worktrees/{repo}-issue-N`
 *
 * On a Go-created worktree it returned the worktree path unchanged, so every
 * history read calibrated against the worktree's own near-empty, gitignored
 * history rather than the repo's.
 *
 * `internal/execution/issue_context_paths.go` records this exact lesson for
 * #994 — "THERE ARE TWO WORKTREE LAYOUTS AND EVERY PREVIOUS SEARCH KNEW ABOUT
 * ONE" — in a file nobody re-checked when this one was written.
 */

import { describe, it, expect } from "vitest";
import * as path from "path";
import { resolveMainRepoRoot } from "../../src/utils/adaptiveBudgetLoader";

const REPO = path.join(path.sep + "repos", "target");

describe("history root resolution across both worktree layouts (#1017)", () => {
  it("strips the Go manager's layout", () => {
    const worktree = path.join(REPO, ".nightgauge", "worktrees", "target-issue-1017");
    expect(
      resolveMainRepoRoot(worktree),
      "a Go-created worktree resolved to itself, so the estimator read the " +
        "worktree's own near-empty history instead of the repo's"
    ).toBe(REPO);
  });

  it("strips the extension's layout", () => {
    const worktree = path.join(REPO, ".worktrees", "issue-1017");
    expect(resolveMainRepoRoot(worktree)).toBe(REPO);
  });

  it("leaves a plain repo root alone", () => {
    expect(resolveMainRepoRoot(REPO)).toBe(REPO);
  });

  it("does not truncate a repo whose own path contains the word worktrees", () => {
    // `/repos/worktrees-demo` has no marker — the separators do not line up —
    // so a naive substring search must not eat it.
    const odd = path.join(path.sep + "repos", "worktrees-demo");
    expect(resolveMainRepoRoot(odd)).toBe(odd);
  });

  it("prefers the longer marker when both could match", () => {
    // The Go layout contains a separator the extension marker could match
    // inside it if the shorter marker were tried first.
    const worktree = path.join(REPO, ".nightgauge", "worktrees", "target-issue-9");
    expect(resolveMainRepoRoot(worktree)).toBe(REPO);
    expect(resolveMainRepoRoot(worktree)).not.toContain(".nightgauge");
  });
});
