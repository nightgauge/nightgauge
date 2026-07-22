package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// RulesetService is the forge-agnostic surface for branch protection /
// merge ruleset evaluation. GitHub branch rulesets and GitLab push rules
// map to the same precheck-and-resolve flow.
type RulesetService interface {
	CheckRulesets(ctx context.Context, owner, repo string, prNumber int) (*forgetypes.RulesetCheckResult, error)
	SatisfyRulesets(ctx context.Context, owner, repo string, prNumber int, blockers []string) ([]string, error)
}
