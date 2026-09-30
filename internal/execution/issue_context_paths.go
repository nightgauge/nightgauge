package execution

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout"
)

func issueContextName(issueNumber int) string {
	return fmt.Sprintf("issue-%d.json", issueNumber)
}

func planningContextName(issueNumber int) string {
	return fmt.Sprintf("planning-%d.json", issueNumber)
}

// pipelineStateDir resolves root's pipeline state directory through
// layout.PipelineStateDir. A relative root is made absolute first. Every root
// of one clone (the checkout and each worktree) resolves to the same directory
// under the git common dir, so the candidates below collapse to one path.
func pipelineStateDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	return layout.PipelineStateDir(abs)
}

// IssueContextCandidates returns every path a run's issue-{N}.json may live at,
// most-specific first.
//
// THERE ARE SEVERAL WORKTREE LAYOUTS AND EVERY PREVIOUS SEARCH KNEW ABOUT ONE (#994).
//
//   - The Go manager writes `<worktree base>/{repoName}-issue-N`, outside the
//     working tree (worktreePath, config.ResolveWorktreeBase; ADR-024 § 9). The
//     leaf carries the repo name so two repos' issue #N cannot collide.
//   - Before #2038 it wrote `<repoRoot>/.nightgauge/worktrees/{repoName}-issue-N`;
//     a run that began there keeps that worktree until it ends.
//   - The VSCode extension's WorktreeManager now writes the same
//     `<worktree base>/{repoName}-issue-N`; before #2038 it wrote
//     `<repoRoot>/.worktrees/issue-N`.
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
	return stageContextCandidates(repoRoot, worktreeDir, repo, issueNumber, issueContextName(issueNumber))
}

// PlanningContextCandidates returns every path a run's planning-{N}.json may
// live at, most-specific first — the same roots, in the same order, as
// IssueContextCandidates, and for the same reason: the two dispatch paths use
// different worktree layouts, and a reader that knows one of them reports
// "absent" for every run of the other. #1515 reads this file for the planner's
// assessed size, so a half-informed search there would reproduce exactly the
// size-less records it exists to fix.
func PlanningContextCandidates(repoRoot, worktreeDir, repo string, issueNumber int) []string {
	return stageContextCandidates(repoRoot, worktreeDir, repo, issueNumber, planningContextName(issueNumber))
}

// stageContextCandidates is the shared root enumeration behind both candidate
// lists. ONE list of layouts, so a new layout cannot be taught to one reader
// and not the other. name is the context file's name inside each root's
// pipeline state directory; a root that directory cannot be resolved for is
// skipped.
func stageContextCandidates(repoRoot, worktreeDir, repo string, issueNumber int, name string) []string {
	roots := make([]string, 0, 5)

	if worktreeDir != "" {
		roots = append(roots, worktreeDir)
	}
	if repoRoot != "" {
		repoName := repo
		if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
			repoName = repoName[idx+1:]
		}
		if leaf, err := layout.WorktreeDirName(repoName, issueNumber); repoName != "" && err == nil {
			// Go manager layout — must match worktreePath exactly. An
			// unresolvable base (invalid config) only drops this candidate:
			// the lookup is best-effort and the error surfaces at creation.
			if base, err := config.ResolveWorktreeBase(repoRoot); err == nil {
				roots = append(roots, filepath.Join(base, leaf))
			}
			// The pre-#2038 in-tree location of the same leaf.
			roots = append(roots, filepath.Join(repoRoot, LegacyWorktreeBaseRel, leaf))
		}
		// The VSCode extension's pre-#2038 layout.
		roots = append(roots, filepath.Join(repoRoot, ".worktrees",
			fmt.Sprintf("issue-%d", issueNumber)))
		// The repo root itself — a run that never took a worktree.
		roots = append(roots, repoRoot)
	}

	paths := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		dir, err := pipelineStateDir(root)
		if err != nil {
			continue
		}
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}
