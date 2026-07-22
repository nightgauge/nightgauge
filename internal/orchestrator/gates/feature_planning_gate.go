package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FeaturePlanningGate verifies the post-conditions of feature-planning:
//
//  1. pipeline/planning-{N}.json exists and parses
//  2. The context references a plan_file path that exists on disk and is
//     non-empty (a zero-byte plan is a skill-no-op masquerading as success)
type FeaturePlanningGate struct{}

// Name implements StageGate.
func (FeaturePlanningGate) Name() string { return "feature-planning" }

// Verify implements StageGate.
func (FeaturePlanningGate) Verify(_ context.Context, issueNumber int, workspace string) GateResult {
	return timedKind("feature-planning", func() (bool, string, []string, Kind) {
		ctxPath := contextFilePath(workspace, "planning", issueNumber)
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "planning context file missing", []string{
					fmt.Sprintf("expected %s", ctxPath),
				}, KindNoOp
			}
			return false, "failed to read planning context file", []string{err.Error()}, KindFail
		}

		var planCtx struct {
			PlanFile string `json:"plan_file"`
		}
		if err := json.Unmarshal(data, &planCtx); err != nil {
			return false, "planning context is not valid JSON", []string{err.Error()}, KindFail
		}
		if planCtx.PlanFile == "" {
			return false, "planning context missing plan_file", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindNoOp
		}

		planAbs := planCtx.PlanFile
		if !filepath.IsAbs(planAbs) {
			planAbs = filepath.Join(workspace, planCtx.PlanFile)
		}
		stat, err := os.Stat(planAbs)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "plan_file does not exist", []string{
					fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
				}, KindNoOp
			}
			return false, "failed to stat plan_file", []string{err.Error()}, KindFail
		}
		if stat.Size() == 0 {
			return false, "plan_file is empty", []string{
				fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			}, KindNoOp
		}
		return true, "planning context references non-empty plan_file", []string{
			fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			fmt.Sprintf("size=%d", stat.Size()),
		}, KindOK
	})
}
