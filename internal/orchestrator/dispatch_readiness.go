package orchestrator

import (
	"fmt"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// Dispatch readiness (#3278).
//
// The scheduler only dispatches an issue that is on a polled board, in a
// dispatchable Status, free of human-only labels, and — when it would be a
// tier-3 refinement candidate — refined or allowed through the backlog sweep.
// Three CLI paths used to answer "will this run?" without asking those rules:
// `issue create` filed issues off the board, `autonomous eligibility` reported
// only the trust gate, and `queue add` accepted anything. They now all ask
// DispatchSkipReasons, which is built from the scheduler's own predicates and
// skip-class names, so the CLI cannot say "eligible" for an issue the scan
// will skip.

// Skip codes reported by DispatchSkipReasons. The three shared with the
// refinement scan are its own constants, so a log line and a CLI warning name
// the same class.
const (
	SkipNotOnBoard           = "not-on-board"
	SkipBoardStatus          = refinementSkipBoardStatus
	SkipExcludedLabel        = refinementSkipExcludedLabel
	SkipBacklogRefinementOff = refinementSkipBacklogOff
	SkipEpic                 = "epic"
)

// ReadinessInput is what one issue looks like to the dispatch scan.
type ReadinessInput struct {
	// OnBoard is true when the issue has an item on the repository's board.
	OnBoard bool
	// BoardStatus and Priority are the item's single-select values; empty
	// when OnBoard is false.
	BoardStatus string
	Priority    string
	Labels      []string
	// ExcludeLabels is autonomous.exclude_labels; empty resolves to the
	// scheduler's defaults.
	ExcludeLabels []string
	// PickupBacklog is autonomous.pickup_backlog.
	PickupBacklog bool
	// RefinementBacklog is autonomous.refinement_backlog.
	RefinementBacklog bool
}

// ReadinessTarget names the issue and board the remedies refer to.
type ReadinessTarget struct {
	Repo    string // owner/name
	Number  int
	Project int
}

// SkipReason is one reason the scheduler will not dispatch an issue, with the
// command that clears it.
type SkipReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy"`
}

// String renders "code: detail".
func (r SkipReason) String() string {
	return r.Code + ": " + r.Detail
}

// DispatchSkipReasons returns every reason the autonomous scheduler would skip
// the issue, in the order the scan checks them. An empty result means the
// board-level gates pass; dependency, concurrency and PR-state gates are
// runtime conditions this function does not judge.
func DispatchSkipReasons(in ReadinessInput, t ReadinessTarget) []SkipReason {
	var out []SkipReason
	issueRef := fmt.Sprintf("%d --repo %s", t.Number, t.Repo)

	if labelSetHas(in.Labels, gh.LabelEpic) {
		out = append(out, SkipReason{
			Code:   SkipEpic,
			Detail: "type:epic issues are never dispatched; queueing the epic queues its sub-issues instead",
			Remedy: fmt.Sprintf("nightgauge queue add %s", issueRef),
		})
	}
	if label, excluded := excludedLabelMatch(in.Labels, resolvedExcludeLabels(in.ExcludeLabels)); excluded {
		out = append(out, SkipReason{
			Code:   SkipExcludedLabel,
			Detail: fmt.Sprintf("carries human-only label %q (autonomous.exclude_labels)", label),
			Remedy: fmt.Sprintf("gh issue edit %d --repo %s --remove-label %q", t.Number, t.Repo, label),
		})
	}

	projectFlag := ""
	if t.Project > 0 {
		projectFlag = fmt.Sprintf(" --project %d", t.Project)
	}
	if !in.OnBoard {
		out = append(out, SkipReason{
			Code:   SkipNotOnBoard,
			Detail: "not on the repository's project board; the scheduler only dispatches board items",
			Remedy: fmt.Sprintf("nightgauge project add %s%s --status Ready", issueRef, projectFlag),
		})
	} else if !isDispatchableStatus(in.BoardStatus, in.PickupBacklog) {
		status := strings.TrimSpace(in.BoardStatus)
		detail := fmt.Sprintf("board Status %q is not dispatched (only Ready", status)
		if status == "" {
			detail = "board Status is unset; only Ready is dispatched ("
		} else {
			detail += "; "
		}
		if in.PickupBacklog {
			detail += "Backlog too, with autonomous.pickup_backlog)"
		} else {
			detail += "Backlog only with autonomous.pickup_backlog)"
		}
		out = append(out, SkipReason{
			Code:   SkipBoardStatus,
			Detail: detail,
			Remedy: fmt.Sprintf("nightgauge project move-status %s ready%s", issueRef, projectFlag),
		})
	}

	// Refinement tier 3 with the backlog sweep off: refinement never picks the
	// issue up, so it is never promoted. A Ready issue is tier 1 and is refined
	// before dispatch, so it is not reported. auto-process is exempt, exactly
	// as in skipReason.
	node := refinementNode{boardStatus: in.BoardStatus, priority: in.Priority}
	if !labelSetHas(in.Labels, gh.LabelRefined) && !labelSetHas(in.Labels, gh.LabelEpic) &&
		!labelSetHas(in.Labels, gh.LabelAutoProcess) && !in.RefinementBacklog &&
		refinementTierFor(node, in.OnBoard) == refinementTierBacklog {
		out = append(out, SkipReason{
			Code: SkipBacklogRefinementOff,
			Detail: "not refined, and as an off-board or unprioritized Backlog issue it is only refined " +
				"with autonomous.refinement_backlog",
			Remedy: fmt.Sprintf("nightgauge issue mark-refined %s", issueRef),
		})
	}
	return out
}
