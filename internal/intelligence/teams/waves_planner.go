package teams

import (
	"strconv"
	"strings"

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
	// ExternalBlockers lists every blocker that is not in the planned batch,
	// usually an issue in another repository. It creates no wave edge — the
	// planner cannot place an issue it was not given — but an issue it blocks
	// is not dispatchable until the blocker closes, whatever wave it is in.
	ExternalBlockers []ExternalBlocker `json:"externalBlockers,omitempty"`
}

// ExternalBlocker is one blocker edge that leaves the planned batch.
type ExternalBlocker struct {
	// Issue is the blocked issue, as "owner/repo#N" ("#N" when the issue
	// carries no repository).
	Issue string `json:"issue"`
	// Blocker is the blocking issue, as "owner/repo#N". A blocker with no
	// repository of its own is in the blocked issue's repository.
	Blocker string `json:"blocker"`
	// State is the blocker's state as the forge reported it on the edge
	// ("OPEN", "CLOSED"); empty when the forge did not say.
	State string `json:"state,omitempty"`
}

// PlanWavesFromIssues converts a slice of pkgtypes.Issue into the shared
// SubIssue / deps shape and runs CalculateWaves to group issues into
// parallel execution waves. Issues and blockers are keyed by
// "owner/repo#N", so same-numbered issues from two repositories never
// collide; a blocker outside the input slice is reported in
// ExternalBlockers rather than dropped.
func PlanWavesFromIssues(issues []pkgtypes.Issue) *EpicWaveResult {
	if len(issues) == 0 {
		return &EpicWaveResult{SubIssueCount: 0, Waves: nil}
	}

	keyToIndex := make(map[string]int, len(issues))
	for i, issue := range issues {
		keyToIndex[issueKey(issue.Repo, issue.Number)] = i
	}

	subIssues := make([]SubIssue, len(issues))
	for i, issue := range issues {
		files, source := ExtractTargetFilesDetailed(issue.Body)
		subIssues[i] = SubIssue{
			Number:       issue.Number,
			Title:        issue.Title,
			Files:        files,
			TargetSource: source,
		}
	}

	deps := make(map[int][]int)
	var external []ExternalBlocker
	for i, issue := range issues {
		for _, blocker := range issue.BlockedBy {
			repo := blocker.Repo
			if repo == "" {
				repo = issue.Repo
			}
			blockerIdx, ok := keyToIndex[issueKey(repo, blocker.Number)]
			if !ok {
				external = append(external, ExternalBlocker{
					Issue:   issueRef(issue.Repo, issue.Number),
					Blocker: issueRef(repo, blocker.Number),
					State:   blocker.State,
				})
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

		ExternalBlockers: external,
	}
}

// issueKey is the planner's identity for an issue: "owner/repo#N", lowercased
// because GitHub repository names are case-insensitive. It mirrors the
// orchestrator's queue-blocker key, which this package cannot import.
func issueKey(repo string, number int) string {
	return strings.ToLower(issueRef(repo, number))
}

// issueRef renders an issue for display: "owner/repo#N", or "#N" with no repo.
func issueRef(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}
