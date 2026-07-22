package github

import (
	"context"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// EpicWaveResult is a type alias for the shared teams.EpicWaveResult so
// existing CLI callers in cmd/nightgauge that reference
// github.EpicWaveResult continue to compile unchanged. The canonical
// definition lives in internal/intelligence/teams/waves_planner.go.
type EpicWaveResult = teams.EpicWaveResult

// PlanWaves fetches each issue in issueNumbers, then delegates the
// wave-planning computation to teams.PlanWavesFromIssues so the GitHub and
// GitLab adapters share a single source of truth for the algorithm.
func (e *EpicService) PlanWaves(ctx context.Context, owner, repo string, issueNumbers []int) (*EpicWaveResult, error) {
	issueSvc := NewIssueService(e.client)

	issues := make([]types.Issue, 0, len(issueNumbers))
	for _, num := range issueNumbers {
		issue, err := issueSvc.GetIssue(ctx, owner, repo, num)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: fetch issue #%d: %v\n", num, err)
			continue
		}
		issues = append(issues, *issue)
	}

	return teams.PlanWavesFromIssues(issues), nil
}

// planWavesFromIssues is retained as a thin shim for the existing GitHub
// epic_waves_test.go so the refactor lands without churn in the test suite.
// New callers should use teams.PlanWavesFromIssues directly.
func planWavesFromIssues(issues []types.Issue) *EpicWaveResult {
	return teams.PlanWavesFromIssues(issues)
}
