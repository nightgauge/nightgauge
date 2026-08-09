package orchestrator

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/state"
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

// reconcileOutcome names WHICH reconcile arm fired, not merely whether one did.
// The distinction is load-bearing in two places (#398): the reconcile log line
// says what the evidence actually was, and the completion block picks the
// run's terminal board status per-arm — a reconcile backed by a CLOSED issue or
// a MERGED PR means the work shipped (Done), while one backed by a stale
// foreign OPEN PR means the work is still in review (In Review).
type reconcileOutcome int

const (
	// reconcileNone: nothing on the forge proves the failure is a phantom, so
	// the failure stands. Zero value on purpose — every fail-closed path
	// (malformed input, query error, unparseable response, unknowable PR
	// ownership) lands here without having to name it.
	reconcileNone reconcileOutcome = iota
	// reconcileIssueClosed: the issue is CLOSED on the forge.
	reconcileIssueClosed
	// reconcilePrMerged: a PR for the branch is MERGED.
	reconcilePrMerged
	// reconcilePrOpenStale: the branch has an OPEN PR that does NOT belong to
	// this run — the #3873 stale-prior-run case.
	reconcilePrOpenStale
)

// reconciled reports whether any arm fired.
func (o reconcileOutcome) reconciled() bool { return o != reconcileNone }

// String is the stable, machine-facing name recorded in telemetry.
func (o reconcileOutcome) String() string {
	switch o {
	case reconcileIssueClosed:
		return "issue_closed"
	case reconcilePrMerged:
		return "pr_merged"
	case reconcilePrOpenStale:
		return "pr_open_stale"
	default:
		return "none"
	}
}

// evidence describes, for the operator-facing reconcile log line, what the arm
// actually observed. The pre-#398 line claimed "closed / branch PR landed" for
// every arm, which is exactly the ambiguity that let an own-run OPEN PR read as
// proof of landing.
func (o reconcileOutcome) evidence() string {
	switch o {
	case reconcileIssueClosed:
		return "the issue is CLOSED on the forge"
	case reconcilePrMerged:
		return "the branch's PR is MERGED"
	case reconcilePrOpenStale:
		return "the branch has an OPEN PR that does not belong to this run (stale prior-run PR, #3873)"
	default:
		return "no forge evidence"
	}
}

// completionReason explains the terminal board status the run finishes with.
func (o reconcileOutcome) completionReason() string {
	switch o {
	case reconcileIssueClosed:
		return "reconciled against a CLOSED issue — the work already shipped"
	case reconcilePrMerged:
		return "reconciled against a MERGED PR — the work already shipped"
	case reconcilePrOpenStale:
		return "reconciled against a stale foreign OPEN PR — the work is in review, not merged"
	default:
		return "normal completion"
	}
}

// completionBoardStatus maps the arm that ended the run to the board status the
// completion block writes (#398). A run that ended because the work is already
// MERGED / the issue already CLOSED is Done; everything else — including a
// reconcile backed only by a stale OPEN PR, and every normal completion — is In
// Review, which is where the pipeline has always left a run with an open PR.
func completionBoardStatus(arm reconcileOutcome) state.BoardStatus {
	switch arm {
	case reconcileIssueClosed, reconcilePrMerged:
		return state.StatusDone
	default:
		return state.StatusInReview
	}
}

// reconcileIssueResolved reports whether the issue's work has already landed on
// the forge, making a non-terminal `feature-*` stage's non-zero exit a false
// alarm to be reconciled rather than recorded as a failure (#3873, Case 1), and
// — since #398 — WHICH evidence said so.
//
// It reconciles when EITHER:
//
//   - the issue is CLOSED (the pipeline closes the issue on merge), OR
//   - the branch has a MERGED PR, or an OPEN PR that belongs to some OTHER run
//     (see branchPrLandedOnForge for the ownership test).
//
// Fail-closed by construction (matches isIssueResolvedOnForge / the terminal
// gate fallback): any malformed input, missing branch, query error, unparseable
// response, or unknowable PR ownership returns reconcileNone so a genuine
// failure is never masked. Only positive, verified evidence reconciles the exit.
//
// branch is the feature branch (from resolveFeatureBranch — NOT the bare
// workspace-root lookup, which answers "" for every worktree-isolated run,
// #299); when empty, only the issue-closed check runs (the PR lookup needs a
// head branch), and the caller must log that the PR half was skipped.
//
// localTip is the run's own branch tip (from localBranchTip at the call site).
// It is the only thing that distinguishes this run's PR from a prior run's; an
// empty value makes every OPEN PR unattributable and therefore blocking.
func reconcileIssueResolved(ctx context.Context, item types.BoardItem, branch, localTip string) reconcileOutcome {
	repo := item.Repo
	// Validate before shelling out. exec (argv, no shell) already prevents
	// metacharacter injection, but reject anything that isn't a well-formed
	// owner/repo + positive issue number as defense-in-depth so a malformed
	// value fails closed rather than producing a bogus gh call.
	if !isWellFormedRepo(repo) || item.Number <= 0 {
		return reconcileNone
	}

	if issueClosedOnForge(ctx, repo, item.Number) {
		return reconcileIssueClosed
	}

	if branch != "" {
		return branchPrLandedOnForge(ctx, repo, branch, localTip)
	}

	return reconcileNone
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

// localBranchTip resolves the run's own branch tip in the checkout the stages
// executed in, so an OPEN PR found on the forge can be attributed to this run
// or to a prior one (#398). Swallows every error into "" — a tip that cannot be
// read is handled by the fail-closed rule in prOpenPROwnedByRun, which treats
// unknown ownership as own-run and therefore blocks reconciliation.
func localBranchTip(workspace, branch string) string {
	if workspace == "" || branch == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", workspace, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// prOpenPROwnedByRun reports whether an OPEN PR's head commit is this run's own
// branch tip, and returns the phrase the blocking log line uses.
//
// Fail-closed: an empty local tip or an empty head SHA means ownership is
// UNKNOWABLE, and this arm's mistake direction is to convert a real failure
// into a recorded success — so unknown ownership counts as own-run and blocks
// the reconcile. "Could not look" is never "not ours".
func prOpenPROwnedByRun(headRefOid, localTip string) (bool, string) {
	head := strings.TrimSpace(headRefOid)
	tip := strings.TrimSpace(localTip)
	if tip == "" || head == "" {
		return true, "ownership is unknowable (empty head SHA or unresolved local branch tip) — failing closed"
	}
	if strings.EqualFold(head, tip) {
		return true, "its head is this run's own branch tip"
	}
	return false, ""
}

// branchPrLandedOnForge reports what the branch's PRs prove about the issue's
// work. A MERGED PR means the work shipped. An OPEN PR means the work is in
// review — but only if it is not THIS run's PR. Fails closed (reconcileNone) on
// any error / unparseable output. A CLOSED-but-not-merged PR (abandoned) does
// NOT reconcile — that is a genuinely-incomplete issue.
//
// The scan is two-pass because the passes disagree and MERGED must win: a
// branch can carry both a merged PR and a later open one, and finding the open
// one first must not decide the question.
//
// Which OPEN PRs count is the #398 correction to #3873. #3873 (knowledge
// ADR-002) accepted OPEN as proof-of-resolution because the regression that
// motivated it paged on an issue whose PR was OPEN+MERGEABLE, and it wrote down
// the escape hatch: "if that tradeoff ever needs tightening, restrict this to
// MERGED only." The tightening is now scoped rather than total, because the two
// shapes are not the same claim:
//
//   - A FOREIGN OPEN PR (head SHA is not this run's branch tip) is a prior run's
//     work sitting in review. Reconciling is the #3873 case, preserved verbatim:
//     the issue is visibly OPEN-with-a-PR for an operator, whereas a false page
//     erodes trust in every page.
//   - An OWN-RUN OPEN PR proves the opposite. It is the post-#4072 rewind shape:
//     conflict-recovery rewinds pr-merge → feature-dev, this run's PR is open,
//     and a genuine feature-dev failure after the rewind would reconcile to
//     success — on the exact path where the run demonstrably has unfinished
//     work. Before #299 this was unreachable on worktree-isolated runs (the
//     branch lookup answered "" so the probe never ran); #299 armed it on every
//     run. Here only MERGED reconciles.
//   - UNKNOWABLE ownership (no head SHA, or no resolvable local tip) is treated
//     as own-run. This arm's failure mode is laundering a real failure into a
//     recorded success, so it must not act on a comparison it could not make.
func branchPrLandedOnForge(ctx context.Context, repo, branch, localTip string) reconcileOutcome {
	out, err := reconcileExecGh(ctx, "pr", "list", "--repo", repo,
		"--head", branch, "--state", "all", "--json", "state,headRefOid,number", "--limit", "10")
	if err != nil {
		return reconcileNone
	}
	var prs []struct {
		State      string `json:"state"`
		HeadRefOid string `json:"headRefOid"`
		Number     int    `json:"number"`
	}
	if jsonErr := json.Unmarshal(out, &prs); jsonErr != nil {
		return reconcileNone
	}

	// Pass 1 — MERGED anywhere in the list is unconditional proof the work
	// landed, whatever else is open on the branch.
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") {
			return reconcilePrMerged
		}
	}

	// Pass 2 — an OPEN PR belonging to this run blocks the reconcile, even if a
	// foreign OPEN PR also exists. The run's own unfinished work is the stronger
	// signal, and list order must not decide it.
	for _, pr := range prs {
		if !strings.EqualFold(pr.State, "OPEN") {
			continue
		}
		if owned, why := prOpenPROwnedByRun(pr.HeadRefOid, localTip); owned {
			log.Printf("non-terminal reconcile: OPEN PR #%d on %s (branch %s) does not reconcile — %s; "+
				"only a MERGED PR proves this run's work landed (#398)", pr.Number, repo, branch, why)
			return reconcileNone
		}
	}

	// Pass 3 — every OPEN PR on the branch is foreign: the #3873 stale-prior-run
	// case, preserved.
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "OPEN") {
			return reconcilePrOpenStale
		}
	}

	return reconcileNone
}
