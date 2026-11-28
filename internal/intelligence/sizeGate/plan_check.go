package sizeGate

import "fmt"

// Heuristic names a PlanResult reports.
const (
	// HeuristicPlannedFiles fires when the plan lists more files than
	// GateConfig.MaxPlannedFiles.
	HeuristicPlannedFiles = "planned-files"
	// HeuristicPlanSizeMismatch fires when the plan sizes itself L/XL for an
	// issue labelled XS/S/M.
	HeuristicPlanSizeMismatch = "plan-size-mismatch"
)

// The mismatch arm's boundaries: a plan at or above planOversizeMin for an
// issue at or below issueSmallMax.
const (
	planOversizeMin = "L"
	issueSmallMax   = "M"
)

// PlanInput is the finished plan a post-planning check judges (#3684).
type PlanInput struct {
	// PlannedFiles is len(files_to_create) + len(files_to_modify).
	PlannedFiles int
	// PlanSize is the plan's complexity_assessment.size_label.
	PlanSize string
	// IssueSize is the issue's size: label (bare bucket or "size:M").
	IssueSize string
}

// PlanResult is the verdict of EvaluatePlan.
type PlanResult struct {
	// Allowed is true when the plan may proceed to feature-dev.
	Allowed bool
	// Reason describes every arm that fired; empty when Allowed.
	Reason string
	// Heuristics lists the arms that fired, in evaluation order.
	Heuristics []string
	// PlanSize and IssueSize are the normalised sizes ("" when unrecognised).
	PlanSize  string
	IssueSize string
}

// EvaluatePlan decides whether a finished plan is too large for the issue
// it was written for. It is pure: the caller supplies the counts and labels.
//
// The file-count arm fires when MaxPlannedFiles > 0 and PlannedFiles exceeds
// it. The mismatch arm fires when the plan is L/XL and the issue XS/S/M; an
// unrecognised size on either side never fires it. PlanCheckEnabled false
// (pipeline.size_gate.enabled: false) allows every plan.
func EvaluatePlan(cfg GateConfig, in PlanInput) PlanResult {
	res := PlanResult{
		Allowed:    true,
		Heuristics: []string{},
		PlanSize:   NormalizeSize(in.PlanSize),
		IssueSize:  NormalizeSize(in.IssueSize),
	}
	if !cfg.PlanCheckEnabled {
		return res
	}
	var reasons []string
	if cfg.MaxPlannedFiles > 0 && in.PlannedFiles > cfg.MaxPlannedFiles {
		res.Heuristics = append(res.Heuristics, HeuristicPlannedFiles)
		reasons = append(reasons, fmt.Sprintf("plan lists %d files, over max_planned_files=%d",
			in.PlannedFiles, cfg.MaxPlannedFiles))
	}
	if sizeRank[res.PlanSize] >= sizeRank[planOversizeMin] &&
		res.IssueSize != "" && sizeRank[res.IssueSize] <= sizeRank[issueSmallMax] {
		res.Heuristics = append(res.Heuristics, HeuristicPlanSizeMismatch)
		reasons = append(reasons, fmt.Sprintf("plan is size %s but the issue is labelled size:%s",
			res.PlanSize, res.IssueSize))
	}
	if len(reasons) > 0 {
		res.Allowed = false
		for i, r := range reasons {
			if i > 0 {
				res.Reason += "; "
			}
			res.Reason += r
		}
	}
	return res
}
