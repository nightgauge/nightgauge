package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// IssuePickupGate verifies the post-conditions of the issue-pickup stage:
//
//  1. Skill output context exists at pipeline/issue-{N}.json
//  2. The context parses as JSON
//  3. The context names a feature branch
//
// Project-board status is intentionally NOT inspected here. Board sync is
// best-effort in the scheduler today (failures are logged, not fatal) so
// gating on it would block the pipeline on transient GitHub-side noise.
type IssuePickupGate struct{}

// Name implements StageGate.
func (IssuePickupGate) Name() string { return "issue-pickup" }

// Verify implements StageGate.
func (IssuePickupGate) Verify(_ context.Context, issueNumber int, workspace string) GateResult {
	return timedKind("issue-pickup", func() (bool, string, []string, Kind) {
		path := contextFilePath(workspace, "issue", issueNumber)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// Skill said success but produced no context file — no-op.
				return false, "issue context file missing", []string{
					fmt.Sprintf("expected %s", path),
				}, KindNoOp
			}
			return false, "failed to read issue context file", []string{err.Error()}, KindFail
		}

		var ctx struct {
			Branch      string `json:"branch"`
			IssueNumber int    `json:"issue_number"`
		}
		if err := json.Unmarshal(data, &ctx); err != nil {
			return false, "issue context file is not valid JSON", []string{err.Error()}, KindFail
		}
		if ctx.Branch == "" {
			// Context exists but the actionable post-state (the branch) is
			// not present — classifier treats this as skill-no-op.
			return false, "issue context missing branch", []string{
				fmt.Sprintf("file: %s", path),
			}, KindNoOp
		}
		return true, "issue context exists with branch", []string{
			fmt.Sprintf("branch=%s", ctx.Branch),
		}, KindOK
	})
}
