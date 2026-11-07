package orchestrator

import (
	"context"
	"log"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// NoDiffCommenter posts the no-diff criteria comment on the issue.
type NoDiffCommenter interface {
	AddComment(ctx context.Context, subjectID, body string) error
}

// NoDiffCloser closes the issue when every criterion is met.
type NoDiffCloser interface {
	CloseIssue(ctx context.Context, issueID string) error
}

// SettleNoDiff ends a pr-create whose branch is level with its base (#2941,
// #3012). It comments the criteria on the issue and closes it when every one is
// met; otherwise the comment names what is unmet and the returned detail parks
// the issue in Backlog. A comment that fails prevents the close, and a close
// that fails leaves the issue open and parked: closing on an unrecorded verdict
// is the worse error. A nil commenter or an empty nodeID records nothing and
// returns the open-issue detail.
//
// It is shared by the scheduler and the `pr-stage create` verb the extension
// drives, so both paths reach the same verdict.
func SettleNoDiff(ctx context.Context, commenter NoDiffCommenter, closer NoDiffCloser,
	nodeID string, issueNumber int, res pmstages.PRCreateResult) (detail string, closed bool) {
	closeIt, comment := pmstages.NoDiffOutcome(issueNumber, res.Branch, res.BaseBranch, res.ACVerdicts)
	if commenter != nil && nodeID != "" {
		if err := commenter.AddComment(ctx, nodeID, comment); err != nil {
			log.Printf("#%d: pr-create no-diff: could not comment the criteria: %v", issueNumber, err)
			closeIt = false
		}
		if closeIt && closer != nil {
			if err := closer.CloseIssue(ctx, nodeID); err != nil {
				log.Printf("#%d: pr-create no-diff: could not close the issue: %v", issueNumber, err)
			} else {
				closed = true
			}
		}
	}
	detail = pmstages.NoDiffTerminalDetail(issueNumber, res.Branch, res.BaseBranch, closed,
		len(pmstages.OpenACs(res.ACVerdicts)))
	return detail, closed
}
