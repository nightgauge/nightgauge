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
//	R3 (#3979) — a CLOSED epic that still has OPEN sub-issues (shipped via an
//	             epic-umbrella PR that did not enumerate `Closes #sub`) has those
//	             orphaned subs auto-closed — but ONLY when the epic was closed as
//	             COMPLETED, never when it was closed as NOT_PLANNED (cancelled).
//
// The same per-epic primitives (CloseOrphanSubs) run on the post-merge hot path
// so the common case self-heals at merge time; ReconcileBoard is the board-wide
// backstop sweep (the `nightgauge project reconcile` command) that catches
// anything the hooks missed.

// OrphanSubAction records what the reconciler did with one orphaned sub-issue.
type OrphanSubAction struct {
	SubNumber int    `json:"subNumber"`
	Repo      string `json:"repo,omitempty"`
	Action    string `json:"action"` // "closed" | "error"
	Error     string `json:"error,omitempty"`
}

// OrphanCloseResult is the outcome of CloseOrphanSubs for a single epic.
type OrphanCloseResult struct {
	EpicNumber int               `json:"epicNumber"`
	Guard      string            `json:"guard"` // "completed" | "not_completed" | "epic_open" | "no_orphans" | "spike"
	Closed     int               `json:"closed"`
	Subs       []OrphanSubAction `json:"subs,omitempty"`
}

// CloseOrphanSubs closes the still-open sub-issues of a CLOSED epic that was
// closed as completed (an epic-umbrella PR shipped the work but did not
// enumerate `Closes #sub` for each sub). Each closed sub gets a back-reference
// comment and is moved to "Done" on the board so the autonomous picker does not
// re-spawn conflicting work (#3979).
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
// The Guard field reports which branch was taken. Errors closing an individual
// sub are recorded per-sub and do not abort the remaining subs.
func (e *EpicService) CloseOrphanSubs(ctx context.Context, owner, repo string, epicNumber, projectNumber int, ownerType ...OwnerType) (*OrphanCloseResult, error) {
	issueSvc := NewIssueService(e.client)
	epic, err := issueSvc.GetIssue(ctx, owner, repo, epicNumber)
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
	res.Guard = "completed"

	var projSvc *ProjectService
	if projectNumber > 0 {
		projSvc = NewProjectService(e.client, owner, projectNumber, ownerType...)
	}

	comment := fmt.Sprintf(
		"Auto-closed: this sub-issue's work shipped as part of epic #%d, which merged and closed as completed. "+
			"Closing the orphaned sub-issue so the autonomous picker does not re-spawn conflicting work. "+
			"Reopen if scope remains.", epicNumber)

	for _, si := range openSubs {
		act := OrphanSubAction{SubNumber: si.Number, Repo: si.Repo}

		if err := issueSvc.CloseIssue(ctx, si.NodeID); err != nil {
			act.Action = "error"
			act.Error = err.Error()
			res.Subs = append(res.Subs, act)
			fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to close orphan sub #%d (epic #%d): %v\n", si.Number, epicNumber, err)
			continue
		}
		if err := issueSvc.AddComment(ctx, si.NodeID, comment); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to comment on orphan sub #%d: %v\n", si.Number, err)
		}
		if projSvc != nil {
			siOwner, siRepo := owner, repo
			if si.Repo != "" {
				siOwner, siRepo = splitOwnerRepo(si.Repo)
			}
			if err := projSvc.SyncStatus(ctx, siOwner, siRepo, si.Number, "Done"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: reconcile: failed to move orphan sub #%d to Done: %v\n", si.Number, err)
			}
		}

		act.Action = "closed"
		res.Closed++
		res.Subs = append(res.Subs, act)
		fmt.Fprintf(os.Stderr, "Reconcile: closed orphan sub #%d (epic #%d completed)\n", si.Number, epicNumber)
	}

	return res, nil
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
	boardSvc := NewBoardService(e.client, owner, projectNumber, ownerType...)
	items, err := boardSvc.ListItems(ctx, "")
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
			status, reason, cerr := e.closeOneEpic(ctx, iOwner, iRepo, it.Number, projectNumber)
			if cerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: auto-close failed: %v", it.Number, cerr))
			} else if status == "closed" {
				res.EpicsClosed++
				res.Actions = append(res.Actions, ReconcileAction{Kind: "epic_closed", Number: it.Number, Repo: it.Repo, Detail: reason})
				continue
			}
		}

		// R3 (#3979): closed epic with open sub-issues → close the orphans
		// (CloseOrphanSubs enforces the stateReason==COMPLETED guard).
		if it.IsEpic && closed && anySubOpen(it.SubIssues) {
			oc, oerr := e.CloseOrphanSubs(ctx, iOwner, iRepo, it.Number, projectNumber, ownerType...)
			if oerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("epic #%d: orphan-sub reconcile failed: %v", it.Number, oerr))
			} else {
				for _, s := range oc.Subs {
					switch s.Action {
					case "closed":
						res.OrphanSubsClosed++
						res.Actions = append(res.Actions, ReconcileAction{Kind: "orphan_sub_closed", Number: s.SubNumber, Repo: s.Repo, Epic: it.Number})
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
