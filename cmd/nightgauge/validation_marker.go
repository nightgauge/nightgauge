package main

import (
	"context"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// hookValidationMarks is the post-merge hook's ValidationMarkWriter (#2574):
// comments read through the trust gate, written with the issue service.
type hookValidationMarks struct {
	*trust.Gate
	issues *gh.IssueService
}

func (m hookValidationMarks) AddComment(ctx context.Context, subjectID, body string) error {
	return m.issues.AddComment(ctx, subjectID, body)
}

// markerTrustGate is the gate the validation marker is read through: only a
// comment by a user with write access or a trusted bot (autonomous.trusted_bots
// in workdir's config, else the gate's defaults) carries a marker that counts.
func markerTrustGate(client *gh.Client, workdir string) *trust.Gate {
	var bots []string
	if workdir != "" {
		if cfg, err := config.Load(workdir); err == nil && cfg != nil && cfg.Autonomous != nil {
			bots = cfg.Autonomous.TrustedBots
		}
	}
	return trust.NewGate(gh.NewTrustForge(client), bots)
}
