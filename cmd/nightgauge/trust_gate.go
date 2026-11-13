package main

import (
	"context"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// stageIdentitiesResolver resolves every login a stage on this machine can act
// as; tests replace it.
var stageIdentitiesResolver = gh.StageIdentities

// newConfiguredTrustGate builds the eligibility gate from the working
// config the way the scheduler's newSchedulerTrustGate does: trusted bots,
// the App owners where label approval counts, and the stage identities a
// label applied by the pipeline itself is told apart by (#2130). A bare
// trust.NewGate refuses every label-approved issue, so a command that vouches
// for a body must build its gate here (#3242).
func newConfiguredTrustGate(src trust.Forge, cfg *config.Config) *trust.Gate {
	var bots, appOwners, logins []string
	if cfg != nil {
		appOwners = cfg.AppOwners()
		logins = cfg.ConfiguredGitHubLogins()
		if cfg.Autonomous != nil {
			bots = cfg.Autonomous.TrustedBots
		}
	}
	return trust.NewGate(src, bots).WithAppOwners(appOwners...).
		WithStageIdentities(func(ctx context.Context) ([]string, error) { return stageIdentitiesResolver(ctx, logins) })
}
