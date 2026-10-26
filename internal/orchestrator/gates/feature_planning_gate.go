package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/deliverable"
)

// FeaturePlanningGate verifies the post-conditions of feature-planning:
//
//  1. pipeline/planning-{N}.json exists and parses
//  2. The context references a plan_file path that exists on disk, inside the
//     clone's plans directory (ResolvePlanFile), and is non-empty (a zero-byte
//     plan is a skill-no-op masquerading as success)
//  3. No planned NEW path is rejected by the publication boundary (#2562): a
//     denied or unclassified path fails as not_pipeline_actionable, parking the
//     issue for an owner publication decision instead of spending dev and
//     validate to reach the same verdict in ci-local.
type FeaturePlanningGate struct{}

// Name implements StageGate.
func (FeaturePlanningGate) Name() string { return "feature-planning" }

// Verify implements StageGate.
func (FeaturePlanningGate) Verify(_ context.Context, issueNumber int, workspace string) GateResult {
	return timedKindTerminal("feature-planning", func() (bool, string, []string, Kind, string) {
		ctxPath := contextFilePath(workspace, "planning", issueNumber)
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "planning context file missing", []string{
					fmt.Sprintf("expected %s", ctxPath),
				}, KindNoOp, ""
			}
			return false, "failed to read planning context file", []string{err.Error()}, KindFail, TerminalKindStageContextUnreadable
		}

		var planCtx struct {
			PlanFile      string   `json:"plan_file"`
			FilesToCreate []string `json:"files_to_create"`
			FilesToModify []string `json:"files_to_modify"`
		}
		if err := json.Unmarshal(data, &planCtx); err != nil {
			reason, ev := describeDecodeFailure("planning context", err)
			return false, reason, ev, KindFail, TerminalKindValidationError
		}
		if ok, reason, ev, kind, tk := checkPlannedPublication(workspace, planCtx.FilesToCreate, planCtx.FilesToModify); !ok {
			return ok, reason, ev, kind, tk
		}
		if planCtx.PlanFile == "" {
			return false, "planning context missing plan_file", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindNoOp, ""
		}

		planAbs, err := PlanFilePath(workspace, planCtx.PlanFile)
		if err != nil {
			return false, "failed to resolve plan_file", []string{err.Error()}, KindFail, TerminalKindStageContextUnreadable
		}
		stat, err := os.Stat(planAbs)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "plan_file does not exist", []string{
					fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
				}, KindNoOp, ""
			}
			return false, "failed to stat plan_file", []string{err.Error()}, KindFail, TerminalKindStageContextUnreadable
		}
		// feature-dev reads the plan only from the plans directory
		// (ResolvePlanFile), so a plan anywhere else is not a plan it can use.
		if _, err := ResolvePlanFile(workspace, planCtx.PlanFile); err != nil {
			return false, "plan_file is not a regular file inside the plans directory", []string{
				fmt.Sprintf("plan_file=%s", planCtx.PlanFile), err.Error(),
			}, KindFail, TerminalKindValidationError
		}
		if stat.Size() == 0 {
			return false, "plan_file is empty", []string{
				fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			}, KindNoOp, ""
		}
		return true, "planning context references non-empty plan_file", []string{
			fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			fmt.Sprintf("size=%d", stat.Size()),
		}, KindOK, ""
	})
}

// checkPlannedPublication runs the publication boundary over every path the
// plan would add: files_to_create, plus any files_to_modify entry that does not
// exist yet. An existing tracked file already passed the boundary, so it is not
// re-asked. A checker that cannot run fails the gate rather than passing it.
func checkPlannedPublication(workspace string, create, modify []string) (bool, string, []string, Kind, string) {
	paths := append([]string{}, create...)
	for _, p := range modify {
		if _, err := os.Stat(filepath.Join(workspace, p)); os.IsNotExist(err) {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return true, "", nil, KindOK, ""
	}
	res, err := deliverable.CheckPublication(workspace, paths)
	if err != nil {
		return false, "publication boundary check could not run", []string{err.Error()}, KindFail, TerminalKindStageContextUnreadable
	}
	rejected := res.Rejected()
	if len(rejected) == 0 {
		return true, "", nil, KindOK, ""
	}
	ev := make([]string, 0, len(rejected)+1)
	for _, v := range rejected {
		if v.Verdict == "deny" {
			ev = append(ev, fmt.Sprintf("%s: denied by publication rule %s (class %s)", v.Path, v.Rule, v.Class))
		} else {
			ev = append(ev, fmt.Sprintf("%s: unclassified — no publication allow rule matches it", v.Path))
		}
	}
	ev = append(ev, "an owner must choose an allowed location or change .github/publication-boundary.yaml")
	return false, "plan targets a path the publication boundary rejects", ev, KindFail, TerminalKindNotPipelineActionable
}
