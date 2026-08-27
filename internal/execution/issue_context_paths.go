package execution

import (
	"fmt"
	"path/filepath"
	"strings"
)

// IssueContextRelPath is where every writer puts a run's issue context,
// relative to whichever root it considers "the run".
func IssueContextRelPath(issueNumber int) string {
	return filepath.Join(".nightgauge", "pipeline", fmt.Sprintf("issue-%d.json", issueNumber))
}

// IssueContextCandidates returns every path a run's issue-{N}.json may live at,
// most-specific first.
//
// THERE ARE TWO WORKTREE LAYOUTS AND EVERY PREVIOUS SEARCH KNEW ABOUT ONE (#994).
//
//   - The Go manager writes `<repoRoot>/.nightgauge/worktrees/{repoName}-issue-N`
//     (see worktreePath — the leaf carries the repo name so two repos' issue #N
//     cannot collide in one workspace).
//   - The VSCode extension writes `<repoRoot>/.worktrees/issue-N`.
//
// The scheduler searched neither — it read the plain repo root only — so an
// autonomous run recorded complexity score 0 and no model prediction on every
// row, for the life of the corpus. The IPC path searched the extension layout
// only, so it would have missed a Go-created worktree.
//
// A single list, used by both readers, is the point: two readers each knowing
// half the layouts is how one corpus field acquired two meanings. Callers must
// tolerate a missing file at every candidate — the context is written by the
// issue-pickup stage, so before that stage runs NONE of these exist, which is
// the second half of the same defect (the read used to happen at pickup, before
// the stage that writes the file).
//
// worktreeDir is the run's actual worktree when the caller knows it, and is
// tried first; pass "" when unknown. repo may be "owner/name" or a bare name.
func IssueContextCandidates(repoRoot, worktreeDir, repo string, issueNumber int) []string {
	rel := IssueContextRelPath(issueNumber)
	roots := make([]string, 0, 4)

	if worktreeDir != "" {
		roots = append(roots, worktreeDir)
	}
	if repoRoot != "" {
		repoName := repo
		if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
			repoName = repoName[idx+1:]
		}
		if repoName != "" {
			// Go manager layout — must match worktreePath exactly.
			roots = append(roots, filepath.Join(repoRoot, ".nightgauge", "worktrees",
				fmt.Sprintf("%s-issue-%d", repoName, issueNumber)))
		}
		// VSCode extension layout.
		roots = append(roots, filepath.Join(repoRoot, ".worktrees",
			fmt.Sprintf("issue-%d", issueNumber)))
		// The repo root itself — a run that never took a worktree.
		roots = append(roots, repoRoot)
	}

	paths := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		p := filepath.Join(root, rel)
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}
