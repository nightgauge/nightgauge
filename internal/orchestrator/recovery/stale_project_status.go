package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// StaleProjectStatus consolidates today's scattered handling of the "issue is
// closed on GitHub but the project board still says in-progress" pattern.
// On match the action shells `nightgauge project move-status <N> done`
// and reports Recovered=true (the work was effectively complete; only the
// board fell out of sync).
type StaleProjectStatus struct{}

// NewStaleProjectStatus is a tiny factory for symmetry.
func NewStaleProjectStatus() *StaleProjectStatus { return &StaleProjectStatus{} }

// Name implements RecoveryAction.
func (a *StaleProjectStatus) Name() string { return "stale-project-status" }

// Description implements RecoveryAction.
func (a *StaleProjectStatus) Description() string {
	return "issue is CLOSED on GitHub but the project board status still reads in-progress — sync to done."
}

// Matches implements RecoveryAction. Pure: only inspects typed fields.
// Issue closure is verified inside Execute via gh — keeping Matches pure
// avoids spurious shell-outs when the predicate would obviously fail.
func (a *StaleProjectStatus) Matches(failure StageFailure) bool {
	if failure.IssueNumber == 0 {
		return false
	}
	// Reason / evidence must hint at a stale-status flavour. Conservative —
	// we only fire when the gate explicitly named the mismatch. The scheduler
	// surfaces "stale-project-status" in evidence when it constructs the
	// StageFailure for this case (#3268).
	combined := strings.ToLower(failure.Reason + " " + strings.Join(failure.Evidence, " "))
	return strings.Contains(combined, "stale-project-status") ||
		strings.Contains(combined, "stale project status") ||
		strings.Contains(combined, "issue closed")
}

// Execute implements RecoveryAction.
func (a *StaleProjectStatus) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	// 1. Verify the issue is actually CLOSED on GitHub.
	out, err := execGh(ctx, "issue", "view", fmt.Sprint(failure.IssueNumber), "--json", "state")
	if err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh issue view failed: %s", truncate(err.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}
	var resp struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(out, &resp); jsonErr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("gh issue view returned unparseable JSON: %s", truncate(jsonErr.Error(), 200)),
			FollowUp: FollowUpNoAction,
		}
	}
	if !strings.EqualFold(resp.State, "CLOSED") {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("issue #%d is %s on GitHub — not stale", failure.IssueNumber, resp.State),
			FollowUp: FollowUpNoAction,
		}
	}

	// 2. Move the project board to done.
	if _, err := execNightgauge(ctx, "project", "move-status", fmt.Sprint(failure.IssueNumber), "done"); err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("project move-status failed: %s", truncate(err.Error(), 200)),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	return RecoveryResult{
		Recovered: true,
		Action:    a.Name(),
		Reason:    fmt.Sprintf("issue #%d is CLOSED — synced project board to done", failure.IssueNumber),
		Evidence:  []string{fmt.Sprintf("issue=%d", failure.IssueNumber), "github_state=CLOSED"},
		FollowUp:  FollowUpStageCanResume,
	}
}
