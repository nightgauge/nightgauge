package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/nightgauge/nightgauge/internal/intelligence/sizeGate"
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
//  4. The plan names at least one file to create or modify (#3354). A plan
//     with neither, other than verify-and-close (#708), dispatches feature-dev
//     to implement nothing; it fails as not_pipeline_actionable, quoting the
//     approach, so the issue parks for its owner instead of halting the queue.
//  5. The plan is not oversized for its issue (#3684): sizeGate.EvaluatePlan
//     over the planned file count, the plan's size_label and the issue's
//     size: label. An oversized plan fails as needs_decomposition, parking
//     the issue before feature-dev and review rounds are paid for.
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
			Approach      string   `json:"approach"`
			FilesToCreate []string `json:"files_to_create"`
			FilesToModify []string `json:"files_to_modify"`
			Complexity    struct {
				SizeLabel string `json:"size_label"`
			} `json:"complexity_assessment"`
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
		if len(planCtx.FilesToCreate) == 0 && len(planCtx.FilesToModify) == 0 &&
			planCtx.Approach != verifyAndCloseApproach {
			return false, emptyPlanReason, []string{
				"approach: " + clipLine(planCtx.Approach, maxApproachEvidence),
				fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			}, KindFail, TerminalKindNotPipelineActionable
		}
		if planCtx.Approach != verifyAndCloseApproach {
			if ok, reason, ev := checkPlanSize(workspace, issueNumber,
				len(planCtx.FilesToCreate)+len(planCtx.FilesToModify), planCtx.Complexity.SizeLabel); !ok {
				return false, reason, ev, KindFail, TerminalKindNeedsDecomposition
			}
		}
		return true, "planning context references non-empty plan_file", []string{
			fmt.Sprintf("plan_file=%s", planCtx.PlanFile),
			fmt.Sprintf("size=%d", stat.Size()),
		}, KindOK, ""
	})
}

const (
	// verifyAndCloseApproach plans legitimately change no file (#708).
	verifyAndCloseApproach = "verify-and-close"
	// emptyPlanReason is the gate reason for a plan naming no file (#3354).
	emptyPlanReason     = "[not-pipeline-actionable] plan lists no files to create or modify"
	maxApproachEvidence = 300
)

// NeedsDecompositionMarker opens the gate reason of an oversized plan; the
// terminalkind table maps it to needs_decomposition.
const NeedsDecompositionMarker = "[needs-decomposition]"

// Evidence keys of an oversized-plan verdict, written as key=value lines.
// The autonomous needs-decomposition action parses them back (#3684).
const (
	EvidencePlannedFiles    = "planned_files"
	EvidenceMaxPlannedFiles = "max_planned_files"
	EvidencePlanSize        = "plan_size"
	EvidenceIssueSize       = "issue_size"
)

// checkPlanSize runs sizeGate.EvaluatePlan over the plan (#3684). The config
// is the workspace's .nightgauge/config.yaml, read by the same loader as
// `nightgauge size-gate`; the issue size is the size: label in issue-{N}.json.
// A missing issue context leaves the issue size unknown, which only disables
// the mismatch arm; one that exists but cannot be read or parsed is logged. The evidence lines are key=value pairs the
// autonomous needs-decomposition action parses.
func checkPlanSize(workspace string, issueNumber, plannedFiles int, planSize string) (bool, string, []string) {
	cfg := sizeGate.LoadGateConfigFromYAML(filepath.Join(workspace, ".nightgauge", "config.yaml"))
	res := sizeGate.EvaluatePlan(cfg, sizeGate.PlanInput{
		PlannedFiles: plannedFiles,
		PlanSize:     planSize,
		IssueSize:    issueSizeLabel(contextFilePath(workspace, "issue", issueNumber)),
	})
	if res.Allowed {
		return true, "", nil
	}
	return false, NeedsDecompositionMarker + " " + res.Reason, []string{
		fmt.Sprintf("%s=%d", EvidencePlannedFiles, plannedFiles),
		fmt.Sprintf("%s=%d", EvidenceMaxPlannedFiles, cfg.MaxPlannedFiles),
		EvidencePlanSize + "=" + res.PlanSize,
		EvidenceIssueSize + "=" + res.IssueSize,
	}
}

// issueSizeLabel is the size: label recorded in issue-{N}.json, or "" when
// the file is unreadable or carries none. Labels are strings or {name}
// objects.
func issueSizeLabel(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("feature-planning gate: cannot read %s, plan-size mismatch check off: %v", path, err)
		}
		return ""
	}
	var issueCtx struct {
		Labels []json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(data, &issueCtx); err != nil {
		log.Printf("feature-planning gate: cannot parse %s, plan-size mismatch check off: %v", path, err)
		return ""
	}
	names := make([]string, 0, len(issueCtx.Labels))
	for _, raw := range issueCtx.Labels {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			var obj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &obj) != nil {
				continue
			}
			name = obj.Name
		}
		names = append(names, name)
	}
	return sizeGate.SizeFromLabels(names)
}

// clipLine is s on one line, at most n runes.
func clipLine(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
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
