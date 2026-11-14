package orchestrator

import (
	"context"
	"log"

	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/pkg/types"
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

// StopNoDiffBeforePRCreate ends a run before pr-create when its branch is
// level with its base and the issue body still has unticked criteria (#3269).
// Nothing is left to open a PR for, so the run ends as a no-op: the comment
// names the criteria still open, and the returned detail books it
// not_pipeline_actionable, which leaves the failure counter unchanged.
//
// stopped is false — and pr-create runs as before — when the branch has a
// diff, the comparison or the issue read fails, or every criterion is ticked
// (pr-create's own no-diff settlement then closes the issue with evidence).
// fetch is called only once the branch is known to be level. Shared by the
// scheduler and the `pr-stage no-diff-stop` verb the extension drives.
func StopNoDiffBeforePRCreate(ctx context.Context, workdir string, issueNumber int,
	fetch func(context.Context) (*types.Issue, error), commenter NoDiffCommenter) (detail string, stopped bool) {
	branch, base, level, err := pmstages.BranchNoDiff(ctx, workdir, issueNumber)
	if err != nil {
		log.Printf("#%d: no-diff check before pr-create: could not compare the branch with its base (%v) — continuing to pr-create", issueNumber, err)
		return "", false
	}
	if !level || fetch == nil {
		return "", false
	}
	issue, err := fetch(ctx)
	if err != nil || issue == nil {
		log.Printf("#%d: no-diff check before pr-create: could not read the issue (%v) — continuing to pr-create", issueNumber, err)
		return "", false
	}
	var open []string
	for _, it := range acparse.List(issue.Body) {
		if !it.Checked {
			open = append(open, it.Text)
		}
	}
	if len(open) == 0 {
		return "", false
	}
	if commenter != nil && issue.NodeID != "" {
		if err := commenter.AddComment(ctx, issue.NodeID, pmstages.NoDiffBeforePRCreateComment(issueNumber, branch, base, open)); err != nil {
			log.Printf("#%d: no-diff check before pr-create: could not comment the open criteria: %v", issueNumber, err)
		}
	}
	detail = pmstages.NoDiffBeforePRCreateDetail(issueNumber, branch, base, len(open))
	log.Printf("#%d: %s", issueNumber, detail)
	return detail, true
}
