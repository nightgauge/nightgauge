package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// PostMergeInput holds parameters for the post-merge hook.
type PostMergeInput struct {
	IssueNumber     int
	RepositoryOwner string
	RepositoryName  string
	ProjectNumber   int // Optional; 0 if not configured
	PRNumber        int // Optional; 0 skips the PR-state guard
	// MainChecks reads the merge commit's check runs so the hook can observe
	// whether the merge turned the base branch red (#1249). Nil skips the
	// verification and the result says so (MainChecksSkipped) — it never reads
	// as green. Requires PRNumber > 0 and a PRMergeInfoFetcher, because the
	// merge SHA comes from the same breadcrumb the survival record uses.
	MainChecks MainCheckReader
	// MainCheckWait bounds the verification poll. The zero value is a single
	// read (see MainCheckWait); the pipeline passes DefaultMainCheckWait().
	MainCheckWait MainCheckWait
	// ValidationMarks writes the post-merge validation marker (#2574) on the
	// merged issue when MainChecks reports the merge commit green. Nil skips
	// it and the result says so (ValidationMarkerNotWired).
	ValidationMarks ValidationMarkWriter
	// Remainder comments on an issue whose merged PR did not close it
	// (#2595), naming the unmet acceptance criteria. Nil skips the comment;
	// the issue is still left open and returned to Ready.
	Remainder IssueCommenter
}

// IssueCommenter comments on an issue by node ID. Implemented by
// *github.IssueService.
type IssueCommenter interface {
	AddComment(ctx context.Context, subjectID, body string) error
}

// ValidationMarkWriter reads an issue's comments through the trust filter and
// comments on it by node ID. Implemented by a trust.Gate over the forge's
// comment source plus *github.IssueService.
type ValidationMarkWriter interface {
	TrustedComments(ctx context.Context, owner, repo string, number int) ([]trust.Comment, error)
	AddComment(ctx context.Context, subjectID, body string) error
}

// ValidationMarkerOutcome is what the hook did about the validation marker.
type ValidationMarkerOutcome string

const (
	// ValidationMarkerWritten: main verified green and the marker was posted.
	ValidationMarkerWritten ValidationMarkerOutcome = "written"
	// ValidationMarkerPresent: main verified green and a trusted comment
	// already carried the marker, so nothing was posted (idempotent re-run).
	ValidationMarkerPresent ValidationMarkerOutcome = "present"
	// ValidationMarkerNotGreen: main was not verified green (red, pending, no
	// checks, unreadable, or skipped), so no marker — dependents stay held.
	ValidationMarkerNotGreen ValidationMarkerOutcome = "not_green"
	// ValidationMarkerNotWired: no writer was wired.
	ValidationMarkerNotWired ValidationMarkerOutcome = "not_wired"
	// ValidationMarkerNotClosing: the merged PR did not close the issue
	// (#2595), so its program dependents stay held until the remainder ships.
	ValidationMarkerNotClosing ValidationMarkerOutcome = "not_closing"
	// ValidationMarkerError: main was green but the marker could not be read
	// or written. Nothing is posted when the read fails, so a re-run cannot
	// duplicate it.
	ValidationMarkerError ValidationMarkerOutcome = "error"
)

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
	GetPRMergeInfo(ctx context.Context, owner, repo string, prNumber int) (gh.PRMergeInfo, error)
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
	// project-board Status (#691). It replaces a boolean that conflated
	// "there was nothing to sync" with "the sync failed" -- both read as false,
	// so a caller could not tell a configuration gap from a broken board, and
	// the hook's exit code says nothing either (it is deliberately
	// non-blocking).
	IssueDoneSync BoardSyncOutcome `json:"issueDoneSync"`
	EpicNumber    int              `json:"epicNumber,omitempty"`
	// EpicRepo is the "owner/repo" the parent epic was resolved against. In a
	// multi-repo workspace this is usually NOT the merged issue's repository,
	// and reporting it is the difference between seeing #1181 and not: the hook
	// used to resolve the epic number against the sub-issue's repo, which lands
	// on an unrelated issue whenever the epic lives elsewhere.
	EpicRepo   string `json:"epicRepo,omitempty"`
	AutoClosed bool   `json:"autoClosed"`
	// OrphanSubsClosed counts sub-issues closed because the merged issue was
	// itself an epic that shipped via an umbrella PR without enumerating
	// `Closes #sub` for each sub (#3979).
	OrphanSubsClosed int `json:"orphanSubsClosed"`
	// OrphanSubsFlagged counts open sub-issues of the merged epic left open
	// because nothing proves their work shipped (#2602): the epic was not
	// closed by merging its epic branch and no merged PR references them.
	OrphanSubsFlagged int `json:"orphanSubsFlagged"`
	// Reason is the hook's own outcome word. The full vocabulary:
	//   no_parent | closed | skipped        — the hook did its job
	//   partial_delivery                    — the PR said `Refs #N`, so the
	//                                         issue stays open (#2595)
	//   pr_verify_error | pr_not_merged     — it refused before doing anything
	//   issue_fetch_error | auto_close_error — it tried and failed
	// "error" is deliberately NOT in this set (#1025). It used to be, copied
	// straight from the epic service's status word, and it was the one value no
	// caller recognised — so a failed rollup printed through the silent default
	// branch while a successful one printed a banner.
	Reason string `json:"reason"`
	// EpicReason is the epic service's own discriminator (has_open, check_failed,
	// fetch_failed, close_failed…). It answers "why" once Reason has answered
	// "what", and before #1025 it was computed on every call and never copied
	// out — so "Epic #N skipped: skipped" was the best the CLI could print.
	EpicReason string `json:"epicReason,omitempty"`
	// EpicPendingTasks lists the epic body's outstanding task-list items when
	// the close was declined as unfinished_decomposition (#1581).
	EpicPendingTasks []string `json:"epicPendingTasks,omitempty"`
	// Integration reports how the epic branch was brought onto main before
	// the epic closed, or why the epic stayed open (#2560).
	Integration *gh.EpicIntegration `json:"integration,omitempty"`
	// Failed is set on every return that did not do what the caller asked. It
	// exists because the hook is deliberately non-blocking and always exits 0:
	// without one boolean, every consumer has to re-derive failure from a string
	// vocabulary, and every consumer got it wrong. AGENTS.md tells operators to
	// READ this hook's output; this is the field that makes that possible.
	Failed bool   `json:"failed"`
	Error  string `json:"error,omitempty"`
	// MergedCommitSha + MergedAt are the post-merge ground-truth breadcrumb
	// (#4133): the merge commit on the base branch and GitHub's ISO-8601 merge
	// timestamp. Captured best-effort via a PRMergeInfoFetcher when one is
	// wired and input.PRNumber > 0; empty on any fetch failure (non-blocking).
	MergedCommitSha string `json:"mergedCommitSha,omitempty"`
	MergedAt        string `json:"mergedAt,omitempty"`
	// BaseRef is the branch the merge landed on, from the same breadcrumb.
	BaseRef string `json:"baseRef,omitempty"`
	// MergedBy is the forge login that merged the PR, from the same
	// breadcrumb (#1283). Empty when the forge returned no actor.
	MergedBy string `json:"mergedBy,omitempty"`
	// MainChecks is the post-merge verification of the base branch (#1249,
	// #2055, github.EvaluateMergedCommit), polled to completion within
	// input.MainCheckWait. Nil when the hook refused before the merge was
	// confirmed; MainChecksSkipped when no reader was wired or no SHA was
	// captured. A red verdict does NOT set Failed — the hook did its job; what
	// failed is main, and the Action Center card is where that goes.
	MainChecks *MainCheckResult `json:"mainChecks,omitempty"`
	// ValidationMarker reports the post-merge validation marker (#2574). Empty
	// when the hook refused before the merge was confirmed. An error sets
	// Failed: a green merge without its marker holds its program dependents.
	ValidationMarker ValidationMarkerOutcome `json:"validationMarker,omitempty"`
	// SurvivalEligible is true when this merge should seed a post-merge survival
	// record (#4151): the issue closed, a merge commit SHA + mergedAt were
	// captured, and the merged issue is a SINGLE issue (not an epic-umbrella PR,
	// whose N→1 attribution is ambiguous). The caller appends a `pending`
	// survival record to the survival store when this is set — kept as a flag,
	// not a side effect, so the hook stays free of survival-store deps.
	SurvivalEligible bool `json:"survivalEligible,omitempty"`
	// PartialDelivery is set when the merged PR body has no closing keyword
	// for the issue (#2595). The issue is left open, its board row returned to
	// Ready, and UnmetCriteria names the remainder.
	PartialDelivery bool     `json:"partialDelivery,omitempty"`
	UnmetCriteria   []string `json:"unmetCriteria,omitempty"`
	// RemainderComment reports the comment naming the remainder: "posted",
	// "failed", or empty when no commenter was wired.
	RemainderComment string `json:"remainderComment,omitempty"`
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
	AutoCloseSingle(ctx context.Context, ref gh.EpicRef, projectNumber int) (*gh.AutoCloseSingleResult, error)
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

// splitOwnerRepo splits an "owner/repo" string. ok is false for anything that
// is not exactly two non-empty segments, so a malformed or absent value falls
// back to the caller's default rather than producing a half-formed coordinate.
func splitOwnerRepo(full string) (owner, repo string, ok bool) {
	o, r, found := strings.Cut(full, "/")
	if !found || o == "" || r == "" || strings.Contains(r, "/") {
		return "", "", false
	}
	return o, r, true
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
func EvaluatePostMerge(ctx context.Context, issueSvc IssueFetcher, issueCloser IssueCloser, epicSvc EpicAutoCloser, prVerifier PRVerifier, boardSvc BoardSyncer, input PostMergeInput) (out PostMergeResult) {
	// Guard: verify the PR is actually MERGED before closing the issue.
	// Skipped when PRNumber is 0 (caller does not know the PR number) or
	// prVerifier is nil (no GitHub client available — e.g., some test paths).
	if input.PRNumber > 0 && prVerifier != nil {
		state, err := prVerifier.GetPRState(ctx, input.RepositoryOwner, input.RepositoryName, input.PRNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge hook: could not verify PR #%d state: %v — skipping issue close\n", input.PRNumber, err)
			return PostMergeResult{Reason: "pr_verify_error", Failed: true, Error: err.Error()}
		}
		if !strings.EqualFold(state, "MERGED") {
			fmt.Fprintf(os.Stderr, "Warning: post-merge hook: PR #%d is %q not MERGED — skipping issue close\n", input.PRNumber, state)
			return PostMergeResult{Reason: "pr_not_merged", Failed: true, Error: fmt.Sprintf("PR #%d state=%s", input.PRNumber, state)}
		}
	}

	// (#4133) Capture the post-merge ground-truth breadcrumb: the merge commit
	// SHA + mergedAt. Best-effort and strictly non-blocking — a fetch failure
	// logs a warning and leaves the fields empty, and the issue-close +
	// epic-reconcile path below runs unchanged regardless. Only attempted when
	// the PR number is known and the verifier also exposes merge info.
	var merge gh.PRMergeInfo
	if input.PRNumber > 0 && prVerifier != nil {
		if mf, ok := prVerifier.(PRMergeInfoFetcher); ok {
			if info, infoErr := mf.GetPRMergeInfo(ctx, input.RepositoryOwner, input.RepositoryName, input.PRNumber); infoErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: post-merge hook: could not capture merge commit for PR #%d: %v\n", input.PRNumber, infoErr)
			} else {
				merge = info
			}
		}
	}
	mergedSha, mergedAt := merge.SHA, merge.MergedAt

	// The hook uses the issue's node id, state and parent, and whether it has
	// sub-issues at all, which IsEpic reports from the first page. It uses no
	// relationship list, so it reads none whole: an epic with many children, or
	// an issue that blocks many others, must not fail the close, the board
	// sync and the epic rollup on a later page the hook would discard.
	issue, err := issueSvc.GetIssueWithRelations(ctx, input.RepositoryOwner, input.RepositoryName, input.IssueNumber, gh.NoRelations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge hook: failed to fetch issue #%d: %v\n", input.IssueNumber, err)
		return PostMergeResult{
			Reason: "issue_fetch_error",
			Failed: true,
			Error:  err.Error(),
		}
	}

	// (#2595) A PR that says `Refs #N` instead of `Closes #N` delivered only
	// part of the issue: pr-create found an unmet acceptance criterion. Leave
	// the issue open, return it to Ready and name the remainder. Only a body
	// we actually read can say this; an unread body keeps the close below.
	if strings.TrimSpace(merge.Body) != "" && !ClosesIssue(merge.Body, input.IssueNumber) {
		return partialDelivery(ctx, boardSvc, input, issue.NodeID, merge)
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

	out = PostMergeResult{IssueClosed: issueClosed, MergedCommitSha: mergedSha, MergedAt: mergedAt, BaseRef: merge.BaseRef, MergedBy: merge.MergedBy}
	// (#1249) Observe the base branch AFTER the fast reconciliation below has
	// run, not before: the poll can take minutes, and the issue close, board
	// sync and epic rollup must not wait on CI. Every return path past this
	// point goes through verifyMain.
	//
	// (#2574) The validation marker follows the verification: it is written
	// only on the green verdict, which is `nightgauge ci checks-complete`'s
	// exit 0 for the same commit (both apply github.EvaluateMergedCommit).
	issueNodeID := issue.NodeID
	defer func() {
		out.MainChecks = verifyMain(ctx, input, out)
		out.ValidationMarker = writeValidationMarker(ctx, input, issueNodeID, out)
		if out.ValidationMarker == ValidationMarkerError {
			out.Failed = true
		}
	}()

	// (#4151) Mark this merge eligible to seed a survival record: the issue
	// closed, both breadcrumb fields were captured, and the merged issue is a
	// single issue — NOT an epic-umbrella PR (whose N→1 attribution is ambiguous,
	// so it is skipped, mirroring the orphan-sub close distinction below).
	isEpicMerge := issue.IsEpic
	out.SurvivalEligible = issueClosed && mergedSha != "" && mergedAt != "" && !isEpicMerge

	// (#3981) Sync the merged issue's own board Status to Done. The board
	// auto-close keyword does not touch the project board, so a just-closed
	// issue otherwise lingers as "In progress"/"Ready" on the board.
	out.IssueDoneSync = BoardSyncNotAttempted
	if boardSvc != nil && input.ProjectNumber > 0 && issueClosed {
		out.IssueDoneSync = syncIssueDone(ctx, boardSvc, input)
		// A board left out of date is a failure of what the caller asked for,
		// even when the epic half succeeds (#1025).
		if out.IssueDoneSync == BoardSyncFailed {
			out.Failed = true
		}
	}

	// (#3979) If the merged issue is itself an epic, an umbrella PR may have
	// shipped all the work and closed the epic without enumerating `Closes #sub`
	// for each sub. Close the orphaned open subs so the picker does not re-spawn
	// conflicting work. CloseOrphanSubs enforces the stateReason==COMPLETED guard,
	// closes a sub only on evidence its own work shipped and flags the rest
	// (#2602), and (#4197) skips `type:spike` parents entirely — a spike's native
	// sub-issues are traceability links to independently-scheduled follow-up
	// work, not decomposition children that are "done" when the spike closes.
	if isEpicMerge {
		if oc, ocErr := epicSvc.CloseOrphanSubs(ctx, input.RepositoryOwner, input.RepositoryName, input.IssueNumber, input.ProjectNumber); ocErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge orphan-sub close failed for epic #%d: %v\n", input.IssueNumber, ocErr)
		} else if oc != nil {
			out.OrphanSubsClosed, out.OrphanSubsFlagged = oc.Closed, oc.Flagged
			if oc.Closed > 0 {
				fmt.Fprintf(os.Stderr, "Post-merge: closed %d orphaned sub-issue(s) of epic #%d\n", oc.Closed, input.IssueNumber)
			}
			if oc.Flagged > 0 {
				fmt.Fprintf(os.Stderr, "Post-merge: left %d sub-issue(s) of epic #%d open: no evidence their work shipped\n", oc.Flagged, input.IssueNumber)
			}
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

	// (#1181) Resolve the epic against ITS OWN repository. GitHub's native
	// sub-issue link reports the parent's repository alongside its number
	// (types.Issue.ParentIssueRepo, from the same `repository { nameWithOwner }`
	// selection SubIssues and BlockedBy have always carried) — that is the
	// existing authority, so nothing here parses bodies or infers coordinates.
	//
	// The fallback to the merged issue's repo covers a parent link that reports
	// no repository at all. It is no longer able to be silently wrong: the
	// ExpectSubIssue fields below make the epic prove it actually lists the
	// merged issue, so a mis-resolved epic errors instead of reporting
	// "no_subs" success.
	epicOwner, epicRepo := input.RepositoryOwner, input.RepositoryName
	if o, r, ok := splitOwnerRepo(issue.ParentIssueRepo); ok {
		epicOwner, epicRepo = o, r
	}
	out.EpicRepo = epicOwner + "/" + epicRepo

	result, err := epicSvc.AutoCloseSingle(ctx, gh.EpicRef{
		Owner:  epicOwner,
		Repo:   epicRepo,
		Number: epicNumber,
		// The merged issue is a sub-issue of the intended epic by construction
		// (that is how we got epicNumber), so an epic that does not list it is
		// the wrong issue.
		ExpectSubIssueNumber: input.IssueNumber,
		ExpectSubIssueRepo:   input.RepositoryOwner + "/" + input.RepositoryName,
	}, input.ProjectNumber)

	// BOTH failure shapes land on one reason word (#1025).
	//
	// `err != nil` is dead through *gh.EpicService — AutoCloseSingle swallows
	// every error into result.Status="error" and returns nil — so the only
	// reason value any caller recognised was unreachable, and every real
	// failure arrived as the bare word "error" that nothing branched on. The
	// two are the same event and must not be distinguishable by accident of
	// which implementation is wired.
	if result != nil {
		out.Integration = result.Integration
	}
	if err != nil || (result != nil && result.Status == "error") {
		out.Reason = "auto_close_error"
		out.Failed = true
		if result != nil {
			out.EpicReason = result.Reason
			out.Error = result.Error
		}
		if err != nil {
			out.Error = err.Error()
		}
		fmt.Fprintf(os.Stderr, "Warning: post-merge epic auto-close FAILED for %s#%d (%s): %s\n",
			out.EpicRepo, epicNumber, out.EpicReason, out.Error)
		return out
	}

	out.AutoClosed = result.Status == "closed"
	out.Reason = result.Status
	out.EpicReason = result.Reason
	out.EpicPendingTasks = result.PendingTasks
	reportEpicDecision(os.Stderr, out.EpicRepo, epicNumber, result)
	return out
}

// maxReportedPendingTasks bounds the pending items printed per declined epic.
const maxReportedPendingTasks = 5

// reportEpicDecision tells the operator which epic the hook closed, or that it
// declined to close it and why, naming the work still pending (#1581).
func reportEpicDecision(w io.Writer, epicRepo string, epicNumber int, result *gh.AutoCloseSingleResult) {
	if result.Status == "closed" {
		fmt.Fprintf(w, "Post-merge: closed epic %s#%d\n", epicRepo, epicNumber)
		return
	}
	fmt.Fprintf(w, "Post-merge: not closing epic %s#%d (%s)\n", epicRepo, epicNumber, result.Reason)
	for i, task := range result.PendingTasks {
		if i == maxReportedPendingTasks {
			fmt.Fprintf(w, "  +%d more\n", len(result.PendingTasks)-i)
			break
		}
		fmt.Fprintf(w, "  - %s\n", task)
	}
}

// verifyMain runs the #1249 post-merge observation for a hook that confirmed
// the merge and captured its SHA, and returns nil for one that did not. It is
// the last thing EvaluatePostMerge does because it is the slow thing: the
// reconciliation above is a handful of API calls, this is a bounded wait on CI.
func verifyMain(ctx context.Context, input PostMergeInput, out PostMergeResult) *MainCheckResult {
	if out.MergedCommitSha == "" {
		return nil
	}
	res := VerifyMergeCommit(ctx, input.MainChecks, input.RepositoryOwner, input.RepositoryName, out.BaseRef, out.MergedCommitSha, input.MainCheckWait)
	switch res.Verdict {
	case MainChecksSkipped:
		fmt.Fprintf(os.Stderr, "Post-merge: main verification skipped for %s (no check reader wired)\n", shortSHA(out.MergedCommitSha))
	case MainChecksRed:
		fmt.Fprintf(os.Stderr, "Warning: post-merge: %s is RED at merge commit %s — %d check(s) failed: %s\n",
			out.BaseRef, shortSHA(out.MergedCommitSha), len(res.FailingNames()), strings.Join(res.FailingNames(), ", "))
	case MainChecksGreen:
		if res.TreesMatch != nil && *res.TreesMatch {
			// #2055: the PR run was the gate for this exact tree.
			fmt.Fprintf(os.Stderr, "Post-merge: %s is green at merge commit %s — same tree as PR #%d's head %s, whose required checks passed; %d check(s) on the merge commit (%d polls)\n",
				out.BaseRef, shortSHA(out.MergedCommitSha), res.PRNumber, shortSHA(res.PRHeadSha), res.Total, res.Polls)
			// CodeQL on the merge commit is informational (#2055): say what
			// it is doing without letting it decide the verdict.
			for _, r := range res.Reasons {
				fmt.Fprintf(os.Stderr, "Post-merge: note: %s\n", r)
			}
			break
		}
		fmt.Fprintf(os.Stderr, "Post-merge: %s is green at merge commit %s (%d checks, %d polls)\n",
			out.BaseRef, shortSHA(out.MergedCommitSha), res.Total, res.Polls)
	case MainChecksPending:
		fmt.Fprintf(os.Stderr, "Post-merge: %s verification budget exhausted at merge commit %s — %d of %d checks still pending (not evidence of breakage): %s\n",
			out.BaseRef, shortSHA(out.MergedCommitSha), res.Pending, res.Total, strings.Join(res.Reasons, "; "))
	case MainChecksNone:
		fmt.Fprintf(os.Stderr, "Post-merge: no check runs appeared on merge commit %s within the grace — %s is NOT verified green\n",
			shortSHA(out.MergedCommitSha), out.BaseRef)
	case MainChecksError:
		// A measurement failure, never a verdict (#1691): the same class
		// `nightgauge ci checks-complete` exits 2 for. Say so, so it is not
		// read as a red main.
		fmt.Fprintf(os.Stderr, "Warning: post-merge: could not verify %s at merge commit %s — the checks could not be read: %s. "+
			"This is not a red verdict; %s is NOT verified. Re-run the verification once the forge is readable\n",
			out.BaseRef, shortSHA(out.MergedCommitSha), res.Error, out.BaseRef)
	}
	return &res
}

// writeValidationMarker posts the #2574 validation marker on the merged issue
// when main verified green. It reads the issue's trusted comments first and
// posts nothing when one already carries a marker, so a re-run never
// duplicates it; a failed read posts nothing either.
func writeValidationMarker(ctx context.Context, input PostMergeInput, issueNodeID string, out PostMergeResult) ValidationMarkerOutcome {
	if out.MainChecks == nil || out.MainChecks.Verdict != MainChecksGreen {
		return ValidationMarkerNotGreen
	}
	if input.ValidationMarks == nil {
		return ValidationMarkerNotWired
	}
	owner, repo, num := input.RepositoryOwner, input.RepositoryName, input.IssueNumber
	comments, err := input.ValidationMarks.TrustedComments(ctx, owner, repo, num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge: could not read #%d's comments for the validation marker: %v — not writing it\n", num, err)
		return ValidationMarkerError
	}
	for _, c := range comments {
		if forge.HasValidationMarker(c.Body) {
			fmt.Fprintf(os.Stderr, "Post-merge: #%d already carries the validation marker\n", num)
			return ValidationMarkerPresent
		}
	}
	body := forge.ValidationMarkerComment(out.BaseRef, out.MergedCommitSha)
	if err := input.ValidationMarks.AddComment(ctx, issueNodeID, body); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-merge: could not write the validation marker on #%d: %v\n", num, err)
		return ValidationMarkerError
	}
	fmt.Fprintf(os.Stderr, "Post-merge: wrote the validation marker on #%d (%s green at %s)\n",
		num, out.BaseRef, shortSHA(out.MergedCommitSha))
	return ValidationMarkerWritten
}

// closingKeywordRE matches GitHub's closing keywords followed by an issue
// reference, optionally repo-qualified.
var closingKeywordRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[ \t]*:?[ \t]*(?:[\w.-]+/[\w.-]+)?#(\d+)\b`)

// ClosesIssue reports whether a PR body carries a closing keyword for issue
// number (#2595).
func ClosesIssue(body string, number int) bool {
	want := strconv.Itoa(number)
	for _, m := range closingKeywordRE.FindAllStringSubmatch(body, -1) {
		if m[1] == want {
			return true
		}
	}
	return false
}

// unmetCriteriaHeading must equal stages.UnmetACHeading, which renders the
// section; it is restated here because hooks must not import the orchestrator.
const unmetCriteriaHeading = "## ⚠️ Unmet acceptance criteria"

// UnmetCriteria returns the bullet items under the unmet-criteria heading of
// a PR body, in order.
func UnmetCriteria(body string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			in = t == unmetCriteriaHeading
			continue
		}
		if in && strings.HasPrefix(t, "- ") {
			out = append(out, strings.TrimSpace(t[2:]))
		}
	}
	return out
}

// partialDelivery handles a merged PR that did not close its issue: no close,
// no Done, no epic rollup and no survival record. The board returns to Ready
// so the remainder is scheduled, and a comment names it.
func partialDelivery(ctx context.Context, boardSvc BoardSyncer, input PostMergeInput, issueNodeID string, merge gh.PRMergeInfo) (out PostMergeResult) {
	out = PostMergeResult{
		Reason:          "partial_delivery",
		PartialDelivery: true,
		UnmetCriteria:   UnmetCriteria(merge.Body),
		IssueDoneSync:   BoardSyncNotAttempted,
		MergedCommitSha: merge.SHA,
		MergedAt:        merge.MergedAt,
		BaseRef:         merge.BaseRef,
		MergedBy:        merge.MergedBy,
	}
	fmt.Fprintf(os.Stderr, "Post-merge: PR #%d does not close issue #%d — leaving it open (partial delivery)\n", input.PRNumber, input.IssueNumber)
	defer func() {
		out.MainChecks = verifyMain(ctx, input, out)
		out.ValidationMarker = ValidationMarkerNotClosing
	}()

	if boardSvc != nil && input.ProjectNumber > 0 {
		if err := boardSvc.SyncStatus(ctx, input.RepositoryOwner, input.RepositoryName, input.IssueNumber, "Ready"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge board sync to Ready failed for #%d: %v\n", input.IssueNumber, err)
			out.Failed = true
			out.Error = err.Error()
		}
	}

	if input.Remainder != nil {
		if err := input.Remainder.AddComment(ctx, issueNodeID, remainderComment(input.PRNumber, out.UnmetCriteria)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-merge remainder comment failed for #%d: %v\n", input.IssueNumber, err)
			out.RemainderComment = "failed"
			out.Failed = true
			if out.Error == "" {
				out.Error = err.Error()
			}
		} else {
			out.RemainderComment = "posted"
		}
	}
	return out
}

func remainderComment(prNumber int, unmet []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("PR #%d merged part of this issue and referenced it with `Refs`, so the issue stays open and is back in Ready.\n\n", prNumber))
	if len(unmet) == 0 {
		b.WriteString("The PR did not list the remaining acceptance criteria; check them against the issue body.\n")
		return b.String()
	}
	b.WriteString("Remaining acceptance criteria:\n\n")
	for _, c := range unmet {
		// Escape HTML-comment openers: this comment is posted by the trusted
		// identity, and a criterion carrying the validation marker would
		// release program dependents (forge.HasValidationMarker).
		b.WriteString("- " + strings.ReplaceAll(c, "<!--", "&lt;!--") + "\n")
	}
	return b.String()
}
