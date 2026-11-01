package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatchedPRResumeReason is the skip reason recorded when a run starts at
// pr-merge because its issue already has an open PR. It is the #1531 wording
// the extension uses for the same fast path, so the two runtimes read alike.
const dispatchedPRResumeReason = "an open PR already exists for this issue — resuming at pr-merge (#1531)"

// preservedWorkResumeIndex returns the index of feature-validate when a
// re-dispatch's dev work survived (#2730), or 0 to run every stage.
func preservedWorkResumeIndex(ctx context.Context, stages []state.PipelineStage, workspaceRoot string, issueNumber int) (int, string) {
	for i, st := range stages {
		if st != state.StageFeatureValidate {
			continue
		}
		if i == 0 {
			return 0, ""
		}
		res := pmstages.ResolvePreservedResume(ctx, workspaceRoot, issueNumber, pmstages.ExecIssueBodyReader(workspaceRoot))
		if res.Resume {
			return i, res.Reason
		}
		return 0, ""
	}
	return 0, ""
}

// dispatchedPRResumeIndex returns the index of pr-merge in stages when a run
// dispatched with an open PR (#2541) can start there, restoring pr-{N}.json
// from that PR when the worktree that held it is gone. It returns 0 — run
// every stage, today's behavior — when there is no dispatched PR, the route
// has no pr-merge stage, or the PR cannot be confirmed OPEN on this issue's
// own branch. A local pr-{N}.json naming a PR wins: it came from the run.
func dispatchedPRResumeIndex(ctx context.Context, stages []state.PipelineStage, workspaceRoot, repo string, issueNumber, openPRNumber int) int {
	if openPRNumber <= 0 {
		return 0
	}
	idx := -1
	for i, st := range stages {
		if st == state.StagePRMerge {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return 0
	}
	path := pipelineStatePath(workspaceRoot, fmt.Sprintf("pr-%d.json", issueNumber))
	if path == "" {
		return 0
	}
	if loadPRNumberForRecovery(workspaceRoot, issueNumber) > 0 {
		return idx
	}
	branch, ok := confirmOpenIssuePR(ctx, repo, issueNumber, openPRNumber)
	if !ok {
		log.Printf("#%d: dispatched with PR #%d but it is not an open PR on this issue's branch — running every stage", issueNumber, openPRNumber)
		return 0
	}
	data, err := json.MarshalIndent(map[string]interface{}{
		"issue_number": issueNumber,
		"pr_number":    openPRNumber,
		"status":       "open",
		"branch":       branch,
	}, "", "  ")
	if err != nil {
		return 0
	}
	if err := atomicWriteFile(path, append(data, '\n')); err != nil {
		log.Printf("#%d: could not restore pr-%d.json for PR #%d (%v) — running every stage", issueNumber, issueNumber, openPRNumber, err)
		return 0
	}
	return idx
}

// confirmOpenIssuePR reads the PR once and reports its head branch when it is
// OPEN, from this repository (not a fork, #1707), and on a branch that names
// issueNumber. The scan that saw the PR may be a cycle old, so the number is
// never trusted unconfirmed.
func confirmOpenIssuePR(ctx context.Context, repo string, issueNumber, prNumber int) (string, bool) {
	if !isWellFormedRepo(repo) {
		return "", false
	}
	out, err := reconcileExecGh(ctx, "pr", "view", fmt.Sprint(prNumber), "--repo", repo,
		"--json", "state,headRefName,isCrossRepository")
	if err != nil {
		return "", false
	}
	var pr struct {
		State             string `json:"state"`
		HeadRefName       string `json:"headRefName"`
		IsCrossRepository bool   `json:"isCrossRepository"`
	}
	if json.Unmarshal(out, &pr) != nil || !strings.EqualFold(pr.State, "OPEN") || pr.IsCrossRepository {
		return "", false
	}
	if n, ok := gitpkg.ParseIssueNumberFromBranch(pr.HeadRefName); !ok || n != issueNumber {
		return "", false
	}
	return pr.HeadRefName, true
}
