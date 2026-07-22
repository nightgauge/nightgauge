package gitlab

import (
	"context"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// EpicService is the GitLab counterpart to internal/github/epic.go's
// EpicService. It mirrors the GitHub surface used by the CLI's
// `epic plan-waves` command so callers that go through a forge factory
// receive identical wave assignments regardless of which forge backs the
// fetch.
type EpicService struct {
	client *Client
}

// NewEpicService binds an EpicService to the given GitLab REST client.
func NewEpicService(client *Client) *EpicService {
	return &EpicService{client: client}
}

// EpicWaveResult is a type alias for the shared teams.EpicWaveResult so
// downstream callers can use a single result type across forges.
type EpicWaveResult = teams.EpicWaveResult

// PlanWaves fetches each issue in issueNumbers (skipping fetch failures
// non-fatally, matching the GitHub adapter's behaviour) and delegates the
// wave-planning computation to the shared teams.PlanWavesFromIssues
// helper. The result is byte-identical to the GitHub adapter's output for
// equivalent dependency graphs — pinned by the parity contract tests in
// internal/forge/parity_test.go.
func (e *EpicService) PlanWaves(ctx context.Context, owner, repo string, issueNumbers []int) (*EpicWaveResult, error) {
	issueSvc := NewIssueService(e.client)

	issues := make([]pkgtypes.Issue, 0, len(issueNumbers))
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
