package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// CIService is the forge-agnostic surface for CI / pipeline check
// inspection. GitHub Checks, GitLab CI/CD, and Bitbucket Pipelines all map
// to the same status-roll-up shape; per-check details (Status, Conclusion)
// follow GitHub's vocabulary as the lowest-common denominator.
type CIService interface {
	GetCheckStatus(ctx context.Context, owner, repo string, prNumber int) (*forgetypes.CheckStatus, error)
	GetRequiredCheckNames(ctx context.Context, owner, repo, branch string) ([]string, error)
	GetIndividualCheckRuns(ctx context.Context, owner, repo, ref string) ([]forgetypes.CheckDetail, error)
	WaitForChecks(ctx context.Context, owner, repo string, prNumber int, cfg forgetypes.WaitConfig) (*forgetypes.CheckStatus, error)
	GetRunLogs(ctx context.Context, owner, repo string, runID int64) (*forgetypes.CIRunLog, error)
}
