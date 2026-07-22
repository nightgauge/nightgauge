package orchestrator

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// reconcileExecGh is the indirection point for `gh`-backed non-terminal
// reconciliation (#3873) so tests can stub GitHub CLI calls without spinning up
// a real CLI. Mirrors gates.execGh / recovery.execGh (#3266). Default
// implementation runs the real `gh` binary.
//
// Cross-repo invocations pass `--repo <owner/repo>` as part of args, matching
// the recovery.execGh contract — the variadic signature covers arbitrary gh
// flag combinations.
var reconcileExecGh = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}

// reconcileIssueResolved reports whether the issue's work has already landed on
// the forge, making a non-terminal `feature-*` stage's non-zero exit a false
// alarm to be reconciled rather than recorded as a failure (#3873, Case 1).
//
// It returns true when EITHER:
//
//   - the issue is CLOSED (the pipeline closes the issue on merge), OR
//   - the branch's PR is MERGED or OPEN+MERGEABLE (the work shipped in a prior
//     run; this stage died in pre-flight on an already-resolved issue).
//
// Fail-closed by construction (matches isIssueResolvedOnForge / the terminal
// gate fallback): any malformed input, missing branch, query error, or
// unparseable response returns false so a genuine failure is never masked. Only
// a positive, verified resolved state reconciles the exit.
//
// branch is the feature branch (from loadFeatureBranch); when empty, only the
// issue-closed check runs (the PR lookup needs a head branch).
func reconcileIssueResolved(ctx context.Context, item types.BoardItem, branch string) bool {
	repo := item.Repo
	// Validate before shelling out. exec (argv, no shell) already prevents
	// metacharacter injection, but reject anything that isn't a well-formed
	// owner/repo + positive issue number as defense-in-depth so a malformed
	// value fails closed rather than producing a bogus gh call.
	if !isWellFormedRepo(repo) || item.Number <= 0 {
		return false
	}

	if issueClosedOnForge(ctx, repo, item.Number) {
		return true
	}

	if branch != "" && branchPrLandedOnForge(ctx, repo, branch) {
		return true
	}

	return false
}

// isWellFormedRepo guards the repo slug against injection / malformed values.
func isWellFormedRepo(repo string) bool {
	if repo == "" {
		return false
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, p := range parts {
		for _, r := range p {
			if !(r == '-' || r == '_' || r == '.' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}

// issueClosedOnForge returns true only when `gh issue view` reports the issue
// state as CLOSED. Fails closed on any error / unparseable output.
func issueClosedOnForge(ctx context.Context, repo string, number int) bool {
	out, err := reconcileExecGh(ctx, "issue", "view", strconv.Itoa(number),
		"--repo", repo, "--json", "state")
	if err != nil {
		return false
	}
	var resp struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(out, &resp); jsonErr != nil {
		return false
	}
	return strings.EqualFold(resp.State, "CLOSED")
}

// branchPrLandedOnForge returns true when an open or merged PR exists for the
// feature branch. A MERGED PR means the work shipped; an OPEN PR means the work
// is in review and this non-terminal stage's failure is a phantom (the issue
// has already progressed past dev). Fails closed on any error / unparseable
// output. A CLOSED-but-not-merged PR (abandoned) does NOT reconcile — that is a
// genuinely-incomplete issue.
//
// Treating OPEN (not just MERGED) as resolved is an explicit #3873 decision
// (knowledge ADR-002): the regression that motivated this fix paged on an issue
// whose PR was OPEN+MERGEABLE. The accepted tradeoff is that a re-work on a
// branch with a stale OPEN PR could have a genuine feature-* failure suppressed;
// that is judged strictly better than the false-failure paging this fixes,
// because the issue is still visibly OPEN-with-a-PR for an operator to inspect,
// whereas a false page erodes trust in every page. If that tradeoff ever needs
// tightening, restrict this to MERGED only.
func branchPrLandedOnForge(ctx context.Context, repo, branch string) bool {
	out, err := reconcileExecGh(ctx, "pr", "list", "--repo", repo,
		"--head", branch, "--state", "all", "--json", "state", "--limit", "10")
	if err != nil {
		return false
	}
	var prs []struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(out, &prs); jsonErr != nil {
		return false
	}
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") || strings.EqualFold(pr.State, "OPEN") {
			return true
		}
	}
	return false
}
