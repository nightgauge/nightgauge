package recovery

import (
	"context"
	"fmt"
	"regexp"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// mergeBaseIntoBranch is the seam over git.Service.MergeBaseIntoBranch: it
// merges origin/<base> into origin/<branch> in a throwaway worktree, unions
// conflicts confined to CHANGELOG.md, and pushes without a force.
var mergeBaseIntoBranch = func(ctx context.Context, workspace, branch, base string) (*gitpkg.BaseMergeResult, error) {
	if _, err := execGit(ctx, workspace, "fetch", "origin", base, branch); err != nil {
		return nil, fmt.Errorf("fetch %s and %s: %s", base, branch, truncate(gitErrDetail(err), 200))
	}
	svc, err := gitpkg.NewService(workspace)
	if err != nil {
		return nil, err
	}
	return svc.MergeBaseIntoBranch(branch, base)
}

// queueConflictBaseRE reads the base out of pmstages.MergeQueueConflictMarker's
// reason text: "merge conflict with <base>: PR #N is CONFLICTING (...)".
var queueConflictBaseRE = regexp.MustCompile(regexp.QuoteMeta(pmstages.MergeQueueConflictMarker) + ` ([^\s:]+):`)

// ChangelogUnionMerge resolves a merge-queue conflict confined to CHANGELOG.md
// files without an LLM stage (#2841). It merges the base rather than rebasing
// because MergeBaseIntoBranch already is a merge, then waits for CI on the new
// head and re-runs the deterministic runner. Recovered=true only on PathMerged.
type ChangelogUnionMerge struct {
	runner pmstages.PRMergeRunner
	// ci supplies the bounded post-push CI wait shared with the rebase path.
	ci *BranchOutOfDate
}

// NewChangelogUnionMerge builds the step over the shared merge runner.
func NewChangelogUnionMerge(runner pmstages.PRMergeRunner) *ChangelogUnionMerge {
	return &ChangelogUnionMerge{runner: runner, ci: NewBranchOutOfDate(runner)}
}

// Attempt tries the merge. handled=false means the caller continues to the
// rebase fallback; evidence then records why, including any conflicting files.
func (s *ChangelogUnionMerge) Attempt(ctx context.Context, failure StageFailure, action string) (res RecoveryResult, handled bool, evidence []string) {
	if s.runner == nil {
		return RecoveryResult{}, false, nil
	}
	base := queueConflictBase(failure)
	branch := currentBranch(ctx, failure.Workspace)
	if base == "" || branch == unknownBranch || rebaseStateDir(ctx, failure.Workspace) != "" {
		return RecoveryResult{}, false, []string{"changelog_union=skipped"}
	}

	merge, err := mergeBaseIntoBranch(ctx, failure.Workspace, branch, base)
	if err != nil {
		return RecoveryResult{}, false, []string{
			"changelog_union=error",
			fmt.Sprintf("changelog_union_error=%s", truncate(err.Error(), 200)),
		}
	}
	evidence = []string{
		fmt.Sprintf("pr=%d", failure.PRNumber),
		fmt.Sprintf("changelog_union=%s", merge.Outcome),
	}
	if merge.Outcome == gitpkg.BaseMergeConflict {
		return RecoveryResult{}, false, append(evidence, prefixed("conflicting_file=", merge.ConflictFiles)...)
	}

	triage := func(reason string) (RecoveryResult, bool, []string) {
		return RecoveryResult{
			Action:   action,
			Reason:   reason,
			Evidence: append(evidence, prefixed("conflicting_file=", merge.ConflictFiles)...),
			FollowUp: FollowUpHumanTriageRequired,
		}, true, nil
	}

	if merge.Outcome != gitpkg.BaseMergeUpToDate {
		// The runner and a later dev stage read this worktree, which must hold
		// the commit just pushed.
		if _, err := execGit(ctx, failure.Workspace, "fetch", "origin", branch); err != nil {
			return triage(fmt.Sprintf("base merged and pushed, but fetching %q failed: %s", branch, truncate(gitErrDetail(err), 200)))
		}
		if _, err := execGit(ctx, failure.Workspace, "merge", "--ff-only", "origin/"+branch); err != nil {
			return triage(fmt.Sprintf("base merged and pushed, but %q would not fast-forward: %s", branch, truncate(gitErrDetail(err), 200)))
		}
	}

	if ciRes, ok := s.ci.waitForCI(ctx, failure, evidence); !ok {
		ciRes.Action = action
		return ciRes, true, nil
	}
	run, err := s.runner.Run(ctx, failure.IssueNumber, failure.Repo, failure.Workspace)
	if err != nil {
		return triage(fmt.Sprintf("runner error after merging %s: %s", base, truncate(err.Error(), 200)))
	}
	if run.Path != pmstages.PathMerged {
		return triage(fmt.Sprintf("merged %s and CI is green, but the deterministic merge did not land: %s", base, run.Reason))
	}
	return RecoveryResult{
		Recovered: true,
		Action:    action,
		Reason:    fmt.Sprintf("merged %s into %q (%s), CI passed, PR #%d merged", base, branch, merge.Outcome, run.PRNumber),
		Evidence:  append(evidence, prefixed("changelog_file=", merge.ConflictFiles)...),
		FollowUp:  FollowUpStageCanResume,
	}, true, nil
}

// queueConflictBase returns the base named by a queue-conflict refusal, or "".
func queueConflictBase(failure StageFailure) string {
	for _, text := range []string{failure.StageError, failure.Reason} {
		if m := queueConflictBaseRE.FindStringSubmatch(text); m != nil {
			return m[1]
		}
	}
	return ""
}
