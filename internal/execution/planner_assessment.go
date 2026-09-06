package execution

import (
	"encoding/json"
	"os"
	"strings"
)

// PlannerAssessment is the size the feature-planning stage assessed for a run,
// read back off its own planning-{N}.json handoff (#1515).
//
// Every run that reaches feature-planning assesses a size — the skill writes
// `complexity_assessment` before it writes anything else — and until #1515 the
// terminal record threw that away and read the issue's `size:*` label instead.
// On 2026-09-06, 12 of 14 completed runs had no such label, so `predictedSize`
// was "" in the outcome corpus and the calibration loop learned nothing from
// the overwhelming majority of runs. The information existed at run time; only
// the reader was missing.
//
// SizeLabel is the planner's own bucket, normalized to upper case, or "" when
// the plan carried none. Score is `computed_score` — the router's Fibonacci
// scale (1/2/3/5/8), NOT the estimator's 1-10 — or 0 when absent.
type PlannerAssessment struct {
	SizeLabel string
	Score     int
}

// Assessed reports whether the plan said anything about size at all.
func (a PlannerAssessment) Assessed() bool {
	return a.SizeLabel != "" || a.Score > 0
}

// LoadPlannerAssessment reads the run's planning-{N}.json and projects its
// `complexity_assessment` onto the two fields the size resolution needs.
//
// Best-effort, exactly like LoadIssueClassification's reader: a missing or
// unparseable plan yields the zero value, which leaves the record's size
// resolution to fall through to its next source rather than inventing one.
//
// Two spellings of the score are accepted because both exist on disk. The
// schema defines `computed_score`, and that is what the current skill writes;
// real plans in this workspace's history also carry `fibonacci_score` for the
// same quantity on the same scale. Reading one spelling would report "the
// planner assessed nothing" for a plan that assessed a size, which is the
// defect this loader exists to remove — so both are read, `computed_score`
// first.
func LoadPlannerAssessment(repoRoot, worktreeDir, repo string, issueNumber int) PlannerAssessment {
	for _, path := range PlanningContextCandidates(repoRoot, worktreeDir, repo, issueNumber) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var plan struct {
			ComplexityAssessment struct {
				SizeLabel      string `json:"size_label"`
				ComputedScore  int    `json:"computed_score"`
				FibonacciScore int    `json:"fibonacci_score"`
			} `json:"complexity_assessment"`
		}
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}
		ca := plan.ComplexityAssessment
		score := ca.ComputedScore
		if score <= 0 {
			score = ca.FibonacciScore
		}
		return PlannerAssessment{
			SizeLabel: normalizeSizeBucket(ca.SizeLabel),
			Score:     score,
		}
	}
	return PlannerAssessment{}
}

// normalizeSizeBucket upper-cases and trims a size bucket without validating
// it — validation against the recognized set belongs to the resolver, which is
// the single place that decides what counts as a real size input.
func normalizeSizeBucket(size string) string {
	return strings.ToUpper(strings.TrimSpace(size))
}
