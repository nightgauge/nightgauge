package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
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
// says what the evidence actually was, and the completion block picks the run's
// terminal board status per-arm.
type reconcileOutcome int

const (
	// reconcileNone: nothing on the forge proves the failure is a phantom, so
	// the failure stands. Zero value on purpose — every fail-closed path
	// (malformed input, query error, unparseable response, unidentifiable PR)
	// lands here without having to name it.
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
//
// Every arm is listed explicitly and the default names the unknown value rather
// than folding it into "none": an arm added without updating this method must
// be visible as an unknown arm, not silently reported as "no reconcile
// happened" — those are opposite claims.
func (o reconcileOutcome) String() string {
	switch o {
	case reconcileNone:
		return "none"
	case reconcileIssueClosed:
		return "issue_closed"
	case reconcilePrMerged:
		return "pr_merged"
	case reconcilePrOpenStale:
		return "pr_open_stale"
	default:
		return "unknown_arm_" + strconv.Itoa(int(o))
	}
}

// evidence describes, for the operator-facing reconcile log line, what the arm
// actually MEASURED. The pre-#398 line claimed "closed / branch PR landed" for
// every arm, which is exactly the ambiguity that let an own-run OPEN PR read as
// proof of landing.
func (o reconcileOutcome) evidence() string {
	switch o {
	case reconcileNone:
		return "no forge evidence"
	case reconcileIssueClosed:
		return "the issue is CLOSED on the forge"
	case reconcilePrMerged:
		return "the branch's PR is MERGED"
	case reconcilePrOpenStale:
		return "the branch's OPEN PR is not this run's (its number is not the one this run recorded at pr-create, " +
			"and this run never reached pr-create) — a stale prior-run PR, #3873"
	default:
		return "an unrecognized reconcile arm (" + o.String() + ")"
	}
}

// completionReason explains the terminal board status the run finishes with.
func (o reconcileOutcome) completionReason() string {
	switch o {
	case reconcileNone:
		return "normal completion"
	case reconcileIssueClosed:
		return "reconciled against a CLOSED issue — the work shipped and the issue is closed"
	case reconcilePrMerged:
		return "reconciled against a MERGED PR, but the issue answered OPEN moments earlier — the merge did not " +
			"close it, so the run is in review, not done"
	case reconcilePrOpenStale:
		return "reconciled against a stale foreign OPEN PR — the work is in review, not merged"
	default:
		return "unrecognized reconcile arm (" + o.String() + ")"
	}
}

// completionBoardStatus maps the arm that ended the run to the board status the
// completion block writes (#398).
//
// Done means exactly one thing in this codebase: the issue is CLOSED. Only
// reconcileIssueClosed observed that. The MERGED arm cannot claim it — it runs
// ONLY after issueClosedOnForge has already answered NOT-closed (the issue
// check runs first and short-circuits), and since #299 the reconciled run ends
// right there, so nothing later in the pipeline closes the issue either.
// Writing Done there would durably record Done-with-an-open-issue, breaking the
// Done ⟺ closed invariant the board, the sweeps and the dashboards all encode.
// Everything else — a reconcile backed by a stale OPEN PR, and every normal
// completion — is In Review, which is where the pipeline has always left a run
// with an open PR.
func completionBoardStatus(arm reconcileOutcome) state.BoardStatus {
	switch arm {
	case reconcileIssueClosed:
		return state.StatusDone
	case reconcileNone, reconcilePrMerged, reconcilePrOpenStale:
		return state.StatusInReview
	default:
		// An arm nobody taught this switch about. In Review is the pipeline's
		// long-standing terminal status and the conservative one: it never
		// claims a closure that was not observed.
		return state.StatusInReview
	}
}

// hasReachedPRCreate reports whether THIS run has already executed pr-create,
// read from the runtime's completed-stage record.
//
// It is the belt on the recorded-PR-number identity test (#398). A non-terminal
// `feature-*` stage can only be running after pr-create if something rewound the
// run (conflict recovery rewinds pr-merge → feature-dev, #4072) — and a rewind
// is the only way this run's own PR can be OPEN while a feature stage fails. So
// "this run has been to pr-create" is by itself sufficient to claim the branch's
// open PR, and it still holds on the edge where pr-create ran but its
// pr-{N}.json was never written or could not be read.
func hasReachedPRCreate(runtime *state.RuntimeState) bool {
	if runtime == nil {
		return false
	}
	// EVER completed, not "completed and not since re-dispatched" (#556):
	// opening a PR is an irreversible side effect, so the claim must survive a
	// backtrack that puts pr-create back in flight. HasCompletedStage is the
	// mutex-guarded reader that spans both the standing and the superseded
	// attempts.
	return runtime.HasCompletedStage(state.StagePRCreate)
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
// response, or unidentifiable PR returns reconcileNone so a genuine failure is
// never masked. Only positive, verified evidence reconciles the exit.
//
// branch is the feature branch (from resolveFeatureBranch — NOT the bare
// workspace-root lookup, which answers "" for every worktree-isolated run,
// #299); when empty, only the issue-closed check runs (the PR lookup needs a
// head branch), and the caller must log that the PR half was skipped.
//
// recordedPRNumber and runReachedPRCreate are the run's two IDENTITY facts,
// computed at the scheduler call site: the PR number this run's pr-create wrote
// to pr-{N}.json, and whether pr-create ran at all. Ownership is identity, never
// content — see prOpenPROwnedByRun for why no SHA comparison can answer it.
func reconcileIssueResolved(ctx context.Context, item types.BoardItem, branch string, recordedPRNumber int, runReachedPRCreate bool) reconcileOutcome {
	repo := item.Repo
	// Validate before shelling out. exec (argv, no shell) already prevents
	// metacharacter injection, but reject anything that isn't a well-formed
	// owner/repo + positive issue number as defense-in-depth so a malformed
	// value fails closed rather than producing a bogus gh call.
	if !isWellFormedRepo(repo) || item.Number <= 0 {
		log.Printf("#%d: non-terminal reconcile declined — %q is not a well-formed owner/repo or the issue number is "+
			"not positive, so no forge query was made and the failure stands (#398)", item.Number, repo)
		return reconcileNone
	}

	if issueClosedOnForge(ctx, repo, item.Number) {
		return reconcileIssueClosed
	}

	if branch == "" {
		log.Printf("#%d: non-terminal reconcile declined — the issue is OPEN on %s and no branch could be named, "+
			"so no PR could be examined; the failure stands (#398)", item.Number, repo)
		return reconcileNone
	}

	return branchPrLandedOnForge(ctx, item.Number, repo, branch, recordedPRNumber, runReachedPRCreate)
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

// prOpenPROwnedByRun reports whether an OPEN PR is THIS run's own PR, and
// returns the phrase the blocking log line uses to name the evidence.
//
// Ownership is IDENTITY, not content (#398). A run owns the PR whose NUMBER its
// pr-create recorded, or — belt, for the edge where that record is missing — any
// open PR on the branch once it has been to pr-create at all. Both facts are
// stable under everything git does to the branch, which is why neither a head-SHA
// comparison nor an ancestry test can stand in for them:
//
//   - Head-SHA equality calls an own PR foreign. The post-#4072 rewind
//     re-dispatch rebases and commits on the branch, and WIP checkpoints commit
//     locally with the push deferred, so the local tip routinely sits AHEAD of
//     the pushed head of the run's own PR. That is the exact laundering #398
//     exists to stop, firing on its own motivating path.
//   - Head-SHA equality also calls a foreign PR own. issue-pickup reuses and
//     resets the branch to the pushed tip ("reused-remote"), so a re-run's fresh
//     checkout sits AT a prior run's PR head — killing #3873's stale-prior-run
//     arm in the very shape it was written for.
//   - Ancestry fails in both directions for the same two reasons: a rebase
//     rewrites commits so the old head is no longer an ancestor, and branch reuse
//     puts genuinely foreign heads inside the current tip's ancestry.
//
// FAIL-CLOSED RULE: a PR whose number the probe did not report (pr.Number == 0 —
// an absent, zero or malformed `number` field) cannot be identified at all, and
// counts as OWN. This arm's mistake direction is converting a real failure into
// a recorded success, so an unidentifiable PR must block the reconcile. "Could
// not tell" is never "not ours".
func prOpenPROwnedByRun(prNumber, recordedPRNumber int, runReachedPRCreate bool) (bool, string) {
	if prNumber <= 0 {
		return true, "the probe reported no PR number, so this PR cannot be identified — failing closed"
	}
	if recordedPRNumber != 0 && prNumber == recordedPRNumber {
		return true, fmt.Sprintf("PR #%d is this run's own (recorded at pr-create)", prNumber)
	}
	if runReachedPRCreate {
		return true, "this run has been to pr-create — treating the branch's OPEN PR as its own"
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
//   - A FOREIGN OPEN PR (a PR number this run never opened) is a prior run's
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
//   - An UNIDENTIFIABLE OPEN PR (no number in the probe response) is treated as
//     own-run, per the fail-closed rule in prOpenPROwnedByRun.
//
// Every exit that does NOT reconcile logs why. The reconcile is the only thing
// standing between a real failure and a recorded success, so an operator asking
// "why did my run stay failed" must find the answer in the log rather than infer
// it from silence.
func branchPrLandedOnForge(ctx context.Context, issueNumber int, repo, branch string, recordedPRNumber int, runReachedPRCreate bool) reconcileOutcome {
	out, err := reconcileExecGh(ctx, "pr", "list", "--repo", repo,
		"--head", branch, "--state", "all", "--json", "state,number", "--limit", "10")
	if err != nil {
		log.Printf("#%d: non-terminal reconcile declined — the branch-PR probe for %s on %s failed (%v), so nothing "+
			"was proven and the failure stands (#398)", issueNumber, branch, repo, err)
		return reconcileNone
	}
	var prs []struct {
		State  string `json:"state"`
		Number int    `json:"number"`
	}
	if jsonErr := json.Unmarshal(out, &prs); jsonErr != nil {
		log.Printf("#%d: non-terminal reconcile declined — the branch-PR probe for %s on %s returned unparseable "+
			"output (%v), so nothing was proven and the failure stands (#398)", issueNumber, branch, repo, jsonErr)
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
		if owned, why := prOpenPROwnedByRun(pr.Number, recordedPRNumber, runReachedPRCreate); owned {
			log.Printf("#%d: non-terminal reconcile declined — OPEN PR #%d on %s (branch %s): %s; only a MERGED PR "+
				"proves this run's work landed, so the failure stands (#398)",
				issueNumber, pr.Number, repo, branch, why)
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

	log.Printf("#%d: non-terminal reconcile declined — %s (branch %s) has no MERGED PR and no OPEN PR among the %d "+
		"the probe listed, so nothing proves the work landed and the failure stands (#398)",
		issueNumber, repo, branch, len(prs))
	return reconcileNone
}
