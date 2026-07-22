package teams

import (
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// EpicWaveResult is the forge-agnostic output of PlanWavesFromIssues. Both
// the GitHub and GitLab adapters return this shape so downstream callers
// (CLI, tests, parity contract) see byte-identical wave assignments for the
// same dependency graph regardless of which forge backed the fetch.
type EpicWaveResult struct {
	SubIssueCount int              `json:"subIssueCount"`
	Waves         []WaveAssignment `json:"waves"`
	Conflicts     []FileConflict   `json:"conflicts,omitempty"`
}

// PlanWavesFromIssues converts a slice of pkgtypes.Issue into the shared
// SubIssue / deps shape and runs CalculateWaves to group issues into
// parallel execution waves. External blockers (BlockedBy refs whose Number
// is not in the input slice) are silently dropped — this matches the
// historical GitHub-side behaviour.
func PlanWavesFromIssues(issues []pkgtypes.Issue) *EpicWaveResult {
	if len(issues) == 0 {
		return &EpicWaveResult{SubIssueCount: 0, Waves: nil}
	}

	numToIndex := make(map[int]int, len(issues))
	for i, issue := range issues {
		numToIndex[issue.Number] = i
	}

	subIssues := make([]SubIssue, len(issues))
	for i, issue := range issues {
		subIssues[i] = SubIssue{
			Number: issue.Number,
			Title:  issue.Title,
			Files:  ExtractTargetFiles(issue.Body),
		}
	}

	deps := make(map[int][]int)
	for i, issue := range issues {
		for _, blocker := range issue.BlockedBy {
			blockerIdx, ok := numToIndex[blocker.Number]
			if !ok {
				continue
			}
			deps[i] = append(deps[i], blockerIdx)
		}
	}

	// Deterministically serialize same-wave sub-issues that share a top-level
	// target file by injecting a blockedBy edge (later number depends on
	// earlier). This is the authoring-side root-cause fix for shared-file
	// collisions — it runs before CalculateWaves so the injected edges shape
	// the wave assignment, and the resulting conflicts are surfaced to callers.
	deps, conflicts := SerializeFileOverlaps(subIssues, deps)

	waves, _ := CalculateWaves(subIssues, deps)

	return &EpicWaveResult{
		SubIssueCount: len(issues),
		Waves:         waves,
		Conflicts:     conflicts,
	}
}
