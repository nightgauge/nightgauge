package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// PostMergeInput holds parameters for the post-merge hook.
type PostMergeInput struct {
	IssueNumber     int
	RepositoryOwner string
	RepositoryName  string
	ProjectNumber   int // Optional; 0 if not configured
	PRNumber        int // Optional; 0 skips the PR-state guard
}

// PRVerifier abstracts querying the current state of a PR so EvaluatePostMerge
// can verify the PR is actually MERGED before closing the issue.
type PRVerifier interface {
	GetPRState(ctx context.Context, owner, repo string, prNumber int) (string, error)
}

// PRMergeInfoFetcher optionally fetches the merge commit SHA + ISO-8601 merge
// timestamp of a merged PR. A PRVerifier that also satisfies this interface
// enables the post-merge ground-truth breadcrumb (#4133): EvaluatePostMerge
// type-asserts for it and records the merge commit so the survival-feedback
// loop can later verify whether the merged code held up on main. Strictly
// non-blocking — any error leaves the breadcrumb empty and never fails a merge.
type PRMergeInfoFetcher interface {
	GetPRMergeInfo(ctx context.Context, owner, repo string, prNumber int) (sha, mergedAt string, err error)
}

// PostMergeResult holds the outcome of the post-merge hook.
type PostMergeResult struct {
	// IssueClosed is true when the issue is closed after this hook ran —
	// whether it was closed by this hook's own CloseIssue call, or was
	// already closed before the hook ran (e.g. via GitHub's Closes-keyword
	// auto-close racing ahead of the hook, #686). It intentionally does NOT
	// mean "this hook's CloseIssue call itself succeeded"; downstream
	// reconciliation (board-Done sync, survival eligibility) cares about the
	// issue's actual state, not about who closed it.
	IssueClosed bool `json:"issueClosed"`
	// IssueDoneSync reports what happened to the merged issue's own
	// project-board Status (#3981, #691). It replaces a boolean that conflated
	// "there was nothing to sync" with "the sync failed" -- both read as false,
	// so a caller could not tell a configuration gap from a broken board, and
	// the hook's exit code says nothing either (it is deliberately
	// non-blocking).
	IssueDoneSync BoardSyncOutcome `json:"issueDoneSync"`
	EpicNumber    int              `json:"epicNumber,omitempty"`
	AutoClosed    bool             `json:"autoClosed"`
	// OrphanSubsClosed counts sub-issues closed because the merged issue was
	// itself an epic that shipped via an umbrella PR without enumerating
	// `Closes #sub` for each sub (#3979).
	OrphanSubsClosed int    `json:"orphanSubsClosed"`
	Reason           string `json:"reason"` // "no_parent", "closed", "skipped", "issue_fetch_error", "auto_close_error"
	Error            string `json:"error,omitempty"`
	// MergedCommitSha + MergedAt are the post-merge ground-truth breadcrumb
	// (#4133): the merge commit on the base branch and GitHub's ISO-8601 merge
	// timestamp. Captured best-effort via a PRMergeInfoFetcher when one is
	// wired and input.PRNumber > 0; empty on any fetch failure (non-blocking).
	MergedCommitSha string `json:"mergedCommitSha,omitempty"`
	MergedAt        string `json:"mergedAt,omitempty"`
	// SurvivalEligible is true when this merge should seed a post-merge survival
	// record (#4151): the issue closed, a merge commit SHA + mergedAt were
	// captured, and the merged issue is a SINGLE issue (not an epic-umbrella PR,
	// whose N→1 attribution is ambiguous). The caller appends a `pending`
	// survival record to the survival store when this is set — kept as a flag,
	// not a side effect, so the hook stays free of survival-store deps.
	SurvivalEligible bool `json:"survivalEligible,omitempty"`
}

// BoardSyncOutcome is what the post-merge board-Done sync actually did.
type BoardSyncOutcome string

const (
	// BoardSyncNotAttempted: no board syncer wired, no project configured, or
	// the issue is not closed. Nothing to sync, and nothing wrong.
	BoardSyncNotAttempted BoardSyncOutcome = "not_attempted"
	// BoardSyncSynced: the issue already had a board row; its Status is Done.
	BoardSyncSynced BoardSyncOutcome = "synced"
	// BoardSyncRepaired: the issue had no board row, so one was created and then
	// set to Done. The end state is identical to BoardSyncSynced; the
	// distinction is kept because a repair means an upstream creation path
	// skipped the board, which is worth seeing.
	BoardSyncRepaired BoardSyncOutcome = "repaired"
	// BoardSyncFailed: a sync was attempted and the board is NOT Done.
	BoardSyncFailed BoardSyncOutcome = "failed"
)

// Done reports whether the board row ends up at Done.
func (o BoardSyncOutcome) Done() bool {
	return o == BoardSyncSynced || o == BoardSyncRepaired
}

// IssueCloser abstracts closing a single issue by node ID for testability.
type IssueCloser interface {
	CloseIssue(ctx context.Context, issueID string) error
}

// EpicAutoCloser abstracts the epic-level post-merge operations: auto-closing a
// completed parent epic, and closing the orphaned open sub-issues of an
// epic-umbrella PR. Both are implemented by *github.EpicService.
type EpicAutoCloser interface {
	AutoCloseSingle(ctx context.Context, owner, repo string, epicNumber, projectNumber int) (*gh.AutoCloseSingleResult, error)
	CloseOrphanSubs(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...gh.OwnerType) (*gh.OrphanCloseResult, error)
}

// BoardSyncer abstracts syncing a single issue's project-board Status field and
// adding a missing row. Implemented by *github.ProjectService. Optional: when
// nil, the merged issue's board Status is left untouched (the reconcile sweep is
// the backstop).
//
// AddIssueByNumber is part of the REQUIRED surface rather than an optional
// type-assertion (#691). The defect being fixed is a hook that reported success
// while its board half silently did nothing; making the repair capability
// optional would reproduce that as "self-heal happens wherever someone
// remembered to wire it". It is idempotent, so a caller may invoke it on a row
// that already exists.
type BoardSyncer interface {
	SyncStatus(ctx context.Context, owner, repo string, issueNumber int, status string) error
	AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error)
}

// syncIssueDone drives the merged issue's board Status to Done, adding the row
// first when there is not one.
//
// The missing row is the common case, not an edge case: issues filed ad-hoc with
// `gh issue create` never reach the board, and the hook then looked up a row that
// did not exist, logged a warning, and exited 0 -- so the issue closed on the
// tracker while the board showed nothing (#691). The repair is one verb away and
// idempotent, so attempt it rather than reporting a warning nobody reads.
//
// Only ErrIssueNotOnBoard is repaired. An auth or network failure is not made
// better by writing to the board, and retrying it as if it were would turn a
// clear error into a confusing one.
func syncIssueDone(ctx context.Context, boardSvc BoardSyncer, input PostMergeInput) BoardSyncOutcome {
	owner, repo, num := input.RepositoryOwner, input.RepositoryName, input.IssueNumber

	syncErr := boardSvc.SyncStatus(ctx, owner, repo, num, "Done")
	if syncErr == nil {
		fmt.Fprintf(os.Stderr, "Post-merge: synced issue #%d board status to Done\n", num)
		return BoardSyncSynced
	}

	if !errors.Is(syncErr, gh.ErrIssueNotOnBoard) {
		fmt.Fprintf(os.Stderr, "Warning: post-merge board sync to Done failed for #%d: %v\n", num, syncErr)
		return BoardSyncFailed
	}

	fmt.Fprintf(os.Stderr, "Post-merge: issue #%d was never added to project board %s/%d — adding it\n",
		num, owner, input.ProjectNumber)
	if _, addErr := boardSvc.AddIssueByNumber(ctx, owner, repo, num); addErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge board repair failed for #%d: %v\n", num, addErr)
		return BoardSyncFailed
	}
	if retryErr := boardSvc.SyncStatus(ctx, owner, repo, num, "Done"); retryErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge board sync to Done failed for #%d after adding it: %v\n", num, retryErr)
		return BoardSyncFailed
	}
	fmt.Fprintf(os.Stderr, "Post-merge: added issue #%d to the board and set its status to Done\n", num)
	return BoardSyncRepaired
}

// EvaluatePostMerge closes the merged issue and runs the post-merge epic
// completion check.
//
// Closing the issue explicitly ensures it is closed even when GitHub's
// auto-close keyword mechanism did not fire (e.g., the PR body lacked a
// "Fixes #N" keyword or GitHub's automation was temporarily unavailable).
// If the issue is already closed — most commonly because GitHub's own
// auto-close keyword won the race and closed it first (#686) — the close
// call returns a GraphQL error rather than a no-op; EvaluatePostMerge
// recovers from that using the issue state fetched before the call, so
// downstream reconciliation still treats the issue as closed.
//
// When input.PRNumber > 0 and prVerifier is non-nil, the PR's state is
// verified to be MERGED before closing the issue. This prevents the issue
// from being closed when the merge was assumed but not confirmed (e.g., due
// to EC-budget exhaustion in the deterministic pr-merge runner). If the PR
// is not MERGED, the hook returns early with Reason "pr_not_merged" and does
// not close the issue or run the epic check.
//
// After closing the issue it runs three reconciliation steps so post-merge
// GitHub state stays consistent across the epic ↔ sub ↔ board triad:
//
//   - syncs the merged issue's own board Status to "Done" (#3981), when a board
//     syncer is wired and ProjectNumber > 0;
//   - if the merged issue is itself an epic (an umbrella PR), closes its orphaned
//     open sub-issues — guarded by the epic's stateReason (#3979);
//   - if the merged issue has a parent epic, auto-closes that epic when all its
//     sub-issues are now closed (#3980).
//
// This function is non-blocking: errors are logged to stderr but never returned.
// The merge must not be blocked by a failing close, sync, or epic check.
func EvaluatePostMerge(ctx context.Context, issueSvc IssueFetcher, issueCloser IssueCloser, epicSvc EpicAutoCloser, prVerifier PRVerifier, boardSvc BoardSyncer, input PostMergeInput) PostMergeResult {
	// Guard: verify the PR is actually MERGED before closing the issue.
	// Skipped when PRNumber is 0 (caller does not know the PR number) or
	// prVerifier is nil (no GitHub client available — e.g., some test paths).
	if input.PRNumber > 0 && prVerifier != nil {
		state, err := prVerifier.GetPRState(ctx, input.RepositoryOwner, input.RepositoryName, input.PRNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge hook: could not verify PR #%d state: %v — skipping issue close\n", input.PRNumber, err)
			return PostMergeResult{Reason: "pr_verify_error", Error: err.Error()}
		}
		if !strings.EqualFold(state, "MERGED") {
			fmt.Fprintf(os.Stderr, "Warning: post-merge hook: PR #%d is %q not MERGED — skipping issue close\n", input.PRNumber, state)
			return PostMergeResult{Reason: "pr_not_merged", Error: fmt.Sprintf("PR #%d state=%s", input.PRNumber, state)}
		}
	}

	// (#4133) Capture the post-merge ground-truth breadcrumb: the merge commit
	// SHA + mergedAt. Best-effort and strictly non-blocking — a fetch failure
	// logs a warning and leaves the fields empty, and the issue-close +
	// epic-reconcile path below runs unchanged regardless. Only attempted when
	// the PR number is known and the verifier also exposes merge info.
	var mergedSha, mergedAt string
	if input.PRNumber > 0 && prVerifier != nil {
		if mf, ok := prVerifier.(PRMergeInfoFetcher); ok {
			if sha, at, infoErr := mf.GetPRMergeInfo(ctx, input.RepositoryOwner, input.RepositoryName, input.PRNumber); infoErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: post-merge hook: could not capture merge commit for PR #%d: %v\n", input.PRNumber, infoErr)
			} else {
				mergedSha, mergedAt = sha, at
			}
		}
	}

	issue, err := issueSvc.GetIssue(ctx, input.RepositoryOwner, input.RepositoryName, input.IssueNumber)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge hook: failed to fetch issue #%d: %v\n", input.IssueNumber, err)
		return PostMergeResult{
			Reason: "issue_fetch_error",
			Error:  err.Error(),
		}
	}

	// Explicitly close the issue. This is the fallback in case GitHub's
	// auto-close keyword mechanism did not fire before this hook runs. When
	// the keyword mechanism DID already fire (e.g. the merged PR body said
	// "Closes #N" and GitHub closed the issue as part of the merge), this
	// call races against — and typically loses to — that auto-close: it
	// returns a GraphQL error because the issue is already closed, NOT a
	// no-op (github.IssueService.CloseIssue has no already-closed special
	// case; any GraphQL error propagates verbatim).
	//
	// `alreadyClosed` recovers from that race using the issue.State fetched
	// above — before this call, so it reflects whatever GitHub had already
	// done by fetch time. The issue is considered closed when EITHER this
	// call succeeded OR it was already CLOSED at fetch time, so downstream
	// reconciliation (board-Done sync, survival eligibility) runs in both
	// the closed-by-us and closed-by-keyword-race cases (#686) — not just
	// the case where this hook's own call happened to win the race.
	alreadyClosed := strings.EqualFold(issue.State, "CLOSED")
	closedByUs := false
	if closeErr := issueCloser.CloseIssue(ctx, issue.NodeID); closeErr != nil {
		if alreadyClosed {
			fmt.Fprintf(os.Stderr, "Post-merge: issue #%d was already closed (likely GitHub's Closes-keyword auto-close winning the race) — CloseIssue call was redundant: %v\n", input.IssueNumber, closeErr)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: post-merge issue close failed for #%d: %v\n", input.IssueNumber, closeErr)
		}
	} else {
		closedByUs = true
		fmt.Fprintf(os.Stderr, "Post-merge: closed issue #%d\n", input.IssueNumber)
	}
	issueClosed := closedByUs || alreadyClosed

	out := PostMergeResult{IssueClosed: issueClosed, MergedCommitSha: mergedSha, MergedAt: mergedAt}

	// (#4151) Mark this merge eligible to seed a survival record: the issue
	// closed, both breadcrumb fields were captured, and the merged issue is a
	// single issue — NOT an epic-umbrella PR (whose N→1 attribution is ambiguous,
	// so it is skipped, mirroring the orphan-sub close distinction below).
	isEpicMerge := issue.IsEpic || len(issue.SubIssues) > 0
	out.SurvivalEligible = issueClosed && mergedSha != "" && mergedAt != "" && !isEpicMerge

	// (#3981) Sync the merged issue's own board Status to Done. The board
	// auto-close keyword does not touch the project board, so a just-closed
	// issue otherwise lingers as "In progress"/"Ready" on the board.
	out.IssueDoneSync = BoardSyncNotAttempted
	if boardSvc != nil && input.ProjectNumber > 0 && issueClosed {
		out.IssueDoneSync = syncIssueDone(ctx, boardSvc, input)
	}

	// (#3979) If the merged issue is itself an epic, an umbrella PR may have
	// shipped all the work and closed the epic without enumerating `Closes #sub`
	// for each sub. Close the orphaned open subs so the picker does not re-spawn
	// conflicting work. CloseOrphanSubs enforces the stateReason==COMPLETED guard
	// and (#4197) skips `type:spike` parents entirely — a spike's native
	// sub-issues are traceability links to independently-scheduled follow-up
	// work, not decomposition children that are "done" when the spike closes.
	if issue.IsEpic || len(issue.SubIssues) > 0 {
		if oc, ocErr := epicSvc.CloseOrphanSubs(ctx, input.RepositoryOwner, input.RepositoryName, input.IssueNumber, input.ProjectNumber); ocErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge orphan-sub close failed for epic #%d: %v\n", input.IssueNumber, ocErr)
		} else if oc != nil && oc.Closed > 0 {
			out.OrphanSubsClosed = oc.Closed
			fmt.Fprintf(os.Stderr, "Post-merge: closed %d orphaned sub-issue(s) of epic #%d\n", oc.Closed, input.IssueNumber)
		}
	}

	// (#3980) If the merged issue has a parent epic, auto-close that epic once
	// all of its sub-issues are closed.
	if issue.ParentIssueNumber == 0 {
		out.Reason = "no_parent"
		return out
	}

	epicNumber := issue.ParentIssueNumber
	out.EpicNumber = epicNumber
	result, err := epicSvc.AutoCloseSingle(ctx, input.RepositoryOwner, input.RepositoryName, epicNumber, input.ProjectNumber)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge epic auto-close failed for #%d: %v\n", epicNumber, err)
		out.Reason = "auto_close_error"
		out.Error = err.Error()
		return out
	}

	fmt.Fprintf(os.Stderr, "Post-merge epic check: #%d status=%s reason=%s\n", epicNumber, result.Status, result.Reason)

	out.AutoClosed = result.Status == "closed"
	out.Reason = result.Status
	return out
}
