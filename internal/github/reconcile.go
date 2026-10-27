package github

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// This file implements the post-merge state reconciler that keeps the
// epic ↔ sub-issue ↔ project-board triad consistent after merges. It closes
// three gaps observed in production (nightgauge/nightgauge#3979, #3980,
// #3981):
//
//	R1 (#3981) — a CLOSED issue whose board Status is not "Done" is synced to Done.
//	R2 (#3980) — an OPEN epic whose every sub-issue is closed is auto-closed.
//	R3 (#3979, #2602) — a CLOSED epic that still has OPEN sub-issues has each
//	             of them closed only on evidence its work shipped: the epic was
//	             closed by merging its own epic/<N>- branch, or a merged PR
//	             references the sub-issue. A sub-issue without evidence is
//	             flagged, never closed. Nothing is touched when the epic was
//	             closed as NOT_PLANNED (cancelled).
//
// The same per-epic primitives (CloseOrphanSubs) run on the post-merge hot path
// so the common case self-heals at merge time; ReconcileBoard is the board-wide
// backstop sweep that catches anything the hooks missed — above all the merges
// the pipeline did not perform, since manual `gh pr merge --squash` is the
// routine path.
//
// ReconcileBoard has TWO production triggers (#656):
//
//   - automatic — AutonomousScheduler.reconcileEpicRollup runs it once per
//     board on the autonomous cycle, paced to a 30-minute interval (see
//     internal/orchestrator/autonomous_epic_rollup_reconcile.go), and
//   - manual — the `nightgauge project reconcile` command.
//
// Both call THIS function. R1/R2/R3 are implemented here and nowhere else;
// anything that needs rollup should schedule this sweep rather than grow a
// parallel implementation.

// OrphanSubAction records what the reconciler did with one orphaned sub-issue.
type OrphanSubAction struct {
	SubNumber int    `json:"subNumber"`
	Repo      string `json:"repo,omitempty"`
	Action    string `json:"action"` // "closed" | "flagged" | "error"
	// Evidence names what a close rested on ("epic-branch:#PR" or "pr:#PR").
	Evidence string `json:"evidence,omitempty"`
	// Detail says why a flagged sub-issue was left open.
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// OrphanCloseResult is the outcome of CloseOrphanSubs for a single epic.
type OrphanCloseResult struct {
	EpicNumber int               `json:"epicNumber"`
	Guard      string            `json:"guard"` // "completed" | "no_evidence" | "not_completed" | "epic_open" | "no_orphans" | "spike"
	Closed     int               `json:"closed"`
	Flagged    int               `json:"flagged"`
	Subs       []OrphanSubAction `json:"subs,omitempty"`
}

// CloseOrphanSubs closes the still-open sub-issues of a CLOSED epic whose work
// is proven to have shipped. Each closed sub gets a back-reference comment
// naming its evidence and is moved to "Done" on the board so the autonomous
// picker does not re-spawn conflicting work (#3979).
//
// Safety guards (all must hold before any sub is touched):
//   - the epic must be CLOSED (an open epic's subs follow the normal flow),
//   - the epic's stateReason must be COMPLETED — an epic closed as NOT_PLANNED
//     (cancelled) leaves its open sub-issues untouched, they may be genuine,
//     unstarted work, and
//   - the parent must not carry the `type:spike` label (#4197). `spike
//     materialize` deliberately links adopted follow-up recommendations as
//     native sub-issues of the originating spike for traceability (see
//     internal/cmd/spike/materialize.go), but those follow-ups are meant to be
//     implemented independently, later — the spike's own design-decision PR
//     merging does not mean the follow-up work is done.
//
// COMPLETED alone is not evidence (#2602): a slice PR that says
// `Closes #<epic>` sets it while other sub-issues are unstarted. So a sub is
// closed only when the epic was closed by merging its own epic/<N>- branch
// (the #2560 integration path), or when a merged PR references the sub
// itself. Every other sub is flagged and left open for a human; so is one
// whose evidence could not be read.
//
// The Guard field reports which branch was taken: "completed" when at least
// one sub closed, "no_evidence" when every open sub was flagged. Errors closing
// an individual sub are recorded per-sub and do not abort the remaining subs.
func (e *EpicService) CloseOrphanSubs(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) (*OrphanCloseResult, error) {
	issueSvc := NewIssueService(e.client)
	epic, err := issueSvc.GetIssueWithRelations(ctx, owner, repo, epicNumber, RelationSubIssues)
	if err != nil {
		return nil, fmt.Errorf("fetch epic #%d: %w", epicNumber, err)
	}

	res := &OrphanCloseResult{EpicNumber: epicNumber}

	if !strings.EqualFold(epic.State, "CLOSED") {
		res.Guard = "epic_open"
		return res, nil
	}
	// Only act on epics closed as completed. NOT_PLANNED / cancelled epics must
	// leave their open sub-issues alone.
	if !strings.EqualFold(epic.StateReason, "COMPLETED") {
		res.Guard = "not_completed"
		return res, nil
	}
	// A `type:spike` parent's native sub-issues are traceability links to
	// independently-scheduled adopt-recommendation follow-ups, not required
	// decomposition children — never auto-close them here (#4197).
	if IsSpikeIssue(epic.Labels) {
		res.Guard = "spike"
		return res, nil
	}

	var openSubs []types.SubIssueRef
	for _, si := range epic.SubIssues {
		if !strings.EqualFold(si.State, "CLOSED") {
			openSubs = append(openSubs, si)
		}
	}
	if len(openSubs) == 0 {
		res.Guard = "no_orphans"
		return res, nil
	}

	// An epic closed by merging its own branch shipped every sub-issue's work.
	// Otherwise the PR that closed it is a slice PR, whose mentions of other
	// sub-issues prove nothing about them.
	epicBranchPR, epicCloserPR, epicBranchErr := 0, 0, error(nil)
	if epicEv, err := issueSvc.ShipEvidence(ctx, owner, repo, epicNumber); err != nil {
		epicBranchErr = err
		fmt.Fprintf(os.Stderr, "Warning: reconcile: could not read how epic #%d was closed: %v\n", epicNumber, err)
	} else if pr, ok := ClosedByEpicBranch(epicEv, epicNumber); ok {
		epicBranchPR = pr
	} else if epicEv.Closer != nil {
		epicCloserPR = epicEv.Closer.Number
	}

	var projSvc *ProjectService
	if projectNumber > 0 {
		projSvc = NewProjectService(e.client, owner, projectNumber, ownerType...)
	}

	for _, si := range openSubs {
		act := OrphanSubAction{SubNumber: si.Number, Repo: si.Repo}
		siOwner, siRepo := owner, repo
		if si.Repo != "" {
			siOwner, siRepo = splitOwnerRepo(si.Repo)
		}

		var kind string
		var pr int
		switch {
		case epicBranchPR > 0:
			kind, pr = "epic-branch", epicBranchPR
		default:
			ev, err := issueSvc.ShipEvidence(ctx, siOwner, siRepo, si.Number)
			shipped, ok := ShippedBy(ev, epicCloserPR)
			switch {
			case err != nil:
				act.Detail = fmt.Sprintf("could not read whether its work shipped: %v", err)
			case ok:
				kind, pr = "pr", shipped
			case epicBranchErr != nil:
				act.Detail = fmt.Sprintf("no merged PR references it, and how epic #%d was closed could not be read", epicNumber)
			default:
				act.Detail = fmt.Sprintf("no merged PR references it, and epic #%d was not closed by merging its epic branch", epicNumber)
			}
		}
		if kind == "" {
			act.Action = "flagged"
			res.Flagged++
			res.Subs = append(res.Subs, act)
			fmt.Fprintf(os.Stderr, "Reconcile: left orphan sub #%d open (epic #%d): %s\n", si.Number, epicNumber, act.Detail)
			continue
		}
		act.Evidence = fmt.Sprintf("%s:#%d", kind, pr)

		if err := issueSvc.CloseIssue(ctx, si.NodeID); err != nil {
			act.Action = "error"
			act.Error = err.Error()
			res.Subs = append(res.Subs, act)
			fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to close orphan sub #%d (epic #%d): %v\n", si.Number, epicNumber, err)
			continue
		}
		if err := issueSvc.AddComment(ctx, si.NodeID, orphanCloseComment(epicNumber, kind, pr)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to comment on orphan sub #%d: %v\n", si.Number, err)
		}
		if projSvc != nil {
			if err := projSvc.SyncStatus(ctx, siOwner, siRepo, si.Number, "Done"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to move orphan sub #%d to Done: %v\n", si.Number, err)
			}
		}

		act.Action = "closed"
		res.Closed++
		res.Subs = append(res.Subs, act)
		fmt.Fprintf(os.Stderr, "Reconcile: closed orphan sub #%d (epic #%d, evidence %s)\n", si.Number, epicNumber, act.Evidence)
	}

	res.Guard = "completed"
	if res.Closed == 0 && res.Flagged > 0 {
		res.Guard = "no_evidence"
	}
	return res, nil
}

// orphanCloseComment is the comment on a sub-issue the orphan closer closed,
// naming the evidence and carrying the marker the epic rollup reads.
func orphanCloseComment(epicNumber int, kind string, pr int) string {
	var why string
	if kind == "epic-branch" {
		why = fmt.Sprintf("as part of epic #%d, whose epic branch merged in #%d", epicNumber, pr)
	} else {
		why = fmt.Sprintf("in #%d, which merged and references this sub-issue (epic #%d is closed)", pr, epicNumber)
	}
	return orphanCloseCommentPrefix + " " + why + ". " +
		"Closing the orphaned sub-issue so the autonomous picker does not re-spawn conflicting work. " +
		"Reopen if scope remains.\n\n" + orphanCloseMarker(kind, pr)
}

// ReconcileAction is a single state change the board reconciler made (or
// flagged). Kind is one of: "issue_done", "epic_closed", "orphan_sub_closed",
// "orphan_sub_flagged".
type ReconcileAction struct {
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	Repo   string `json:"repo,omitempty"`
	Epic   int    `json:"epic,omitempty"` // parent epic for orphan_sub_* actions
	Detail string `json:"detail,omitempty"`
}

// ReconcileResult is the aggregate outcome of a board-wide reconcile sweep.
type ReconcileResult struct {
	Checked            int               `json:"checked"`
	IssuesSyncedToDone int               `json:"issuesSyncedToDone"`
	EpicsClosed        int               `json:"epicsClosed"`
	OrphanSubsClosed   int               `json:"orphanSubsClosed"`
	OrphanSubsFlagged  int               `json:"orphanSubsFlagged"`
	Actions            []ReconcileAction `json:"actions,omitempty"`
	Warnings           []string          `json:"warnings,omitempty"`
}

// ReconcileBoard performs a single pass over every item on a project board and
// repairs post-merge state drift across the epic ↔ sub ↔ board triad (R1/R2/R3
// above). It is the deterministic backstop for the post-merge hook — safe to
// run repeatedly (every action is idempotent) and on a schedule.
//
// Cross-repo boards are handled: each item carries its own owner/repo, and node
// IDs are global, so sub-issues and project items resolve regardless of which
// repo they live in.
func (e *EpicService) ReconcileBoard(ctx context.Context, owner string, projectNumber int, ownerType ...OwnerType) (*ReconcileResult, error) {
	// The repairs decide from each item's sub-issue list and no other list.
	boardSvc := NewBoardService(e.client, owner, projectNumber, ownerType...)
	items, err := boardSvc.ListItemsWithRelations(ctx, "", RelationSubIssues)
	if err != nil {
		return nil, fmt.Errorf("list board items: %w", err)
	}

	projSvc := NewProjectService(e.client, owner, projectNumber, ownerType...)
	res := &ReconcileResult{Checked: len(items)}

	for _, it := range items {
		if it.IsPR {
			continue
		}

		iOwner, iRepo := owner, ""
		if it.Repo != "" {
			iOwner, iRepo = splitOwnerRepo(it.Repo)
		}
		closed := strings.EqualFold(it.State, "CLOSED")

		// R2 (#3980): open epic whose sub-issues are all closed → close it.
		// closeOneEpic re-checks completion (with EC retry), closes the epic,
		// comments, and syncs it to Done — so we skip R1 for it on success.
		if it.IsEpic && !closed && len(it.SubIssues) > 0 && allSubsClosed(it.SubIssues) {
			// The board item carries the epic's OWN owner/repo, so this path was
			// already cross-repo correct; EpicRef makes that visible (#1181).
			status, reason, _, cerr := e.closeOneEpic(ctx, EpicRef{
				Owner:  iOwner,
				Repo:   iRepo,
				Number: it.Number,
			}, projectNumber)
			if cerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: auto-close failed: %v", it.Number, cerr))
			} else if status == "closed" {
				res.EpicsClosed++
				res.Actions = append(res.Actions, ReconcileAction{Kind: "epic_closed", Number: it.Number, Repo: it.Repo, Detail: reason})
				continue
			} else if reason == reasonOrphanClosedWithoutEvidence {
				// A human reopened an epic whose sub-issues the pre-#2602
				// orphan closer closed on the epic's state alone (#2602).
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: left open, a sub-issue was orphan-closed without evidence its work shipped", it.Number))
				res.Actions = append(res.Actions, ReconcileAction{
					Kind: "orphan_sub_flagged", Number: it.Number, Repo: it.Repo,
					Detail: "a sub-issue was auto-closed without evidence its work shipped; reopen it or close the epic by hand",
				})
			} else if strings.HasPrefix(reason, "integration_") {
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: left open, epic branch not on main (%s)", it.Number, reason))
			}
		}

		// R3 (#3979): closed epic with open sub-issues → close the orphans
		// whose work shipped and flag the rest (CloseOrphanSubs enforces the
		// stateReason==COMPLETED guard and the ship-evidence check, #2602).
		if it.IsEpic && closed && anySubOpen(it.SubIssues) {
			oc, oerr := e.CloseOrphanSubs(ctx, iOwner, iRepo, it.Number, projectNumber, ownerType...)
			if oerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: orphan-sub reconcile failed: %v", it.Number, oerr))
			} else {
				for _, s := range oc.Subs {
					switch s.Action {
					case "closed":
						res.OrphanSubsClosed++
						res.Actions = append(res.Actions, ReconcileAction{Kind: "orphan_sub_closed", Number: s.SubNumber, Repo: s.Repo, Epic: it.Number, Detail: s.Evidence})
					case "flagged":
						res.OrphanSubsFlagged++
						res.Actions = append(res.Actions, ReconcileAction{Kind: "orphan_sub_flagged", Number: s.SubNumber, Repo: s.Repo, Epic: it.Number, Detail: s.Detail})
					case "error":
						res.Warnings = append(res.Warnings, fmt.Sprintf("orphan sub #%d (epic #%d): %s", s.SubNumber, it.Number, s.Error))
					}
				}
				switch oc.Guard {
				case "not_completed":
					res.Actions = append(res.Actions, ReconcileAction{
						Kind: "orphan_sub_flagged", Number: it.Number, Repo: it.Repo,
						Detail: "epic closed as not-planned; open sub-issues left untouched",
					})
				case "spike":
					res.Actions = append(res.Actions, ReconcileAction{
						Kind: "orphan_sub_flagged", Number: it.Number, Repo: it.Repo,
						Detail: "parent is a type:spike issue; adopted follow-up sub-issues left untouched",
					})
				}
			}
		}

		// R1 (#3981): any closed issue whose board Status is set but not "Done"
		// is stale → sync to Done. Runs for closed epics (after R3) and normal
		// closed sub-issues alike.
		if closed && it.Status != "" && !strings.EqualFold(it.Status, "Done") {
			if err := projSvc.SyncStatus(ctx, iOwner, iRepo, it.Number, "Done"); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("issue #%d: sync to Done failed: %v", it.Number, err))
			} else {
				res.IssuesSyncedToDone++
				res.Actions = append(res.Actions, ReconcileAction{Kind: "issue_done", Number: it.Number, Repo: it.Repo, Detail: it.Status})
			}
		}
	}

	return res, nil
}

// allSubsClosed reports whether every sub-issue in the slice is CLOSED.
// Callers must guard against an empty slice separately (an epic with zero
// sub-issues is not "complete").
func allSubsClosed(subs []types.SubIssueRef) bool {
	for _, si := range subs {
		if !strings.EqualFold(si.State, "CLOSED") {
			return false
		}
	}
	return len(subs) > 0
}

// anySubOpen reports whether at least one sub-issue in the slice is not CLOSED.
func anySubOpen(subs []types.SubIssueRef) bool {
	for _, si := range subs {
		if !strings.EqualFold(si.State, "CLOSED") {
			return true
		}
	}
	return false
}
