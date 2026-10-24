package main

import (
	"context"
	"errors"
	"strings"

	"github.com/nightgauge/nightgauge/internal/trust"
)

// eligibilityAsker is the scheduler's manual-dispatch question (#2130).
type eligibilityAsker interface {
	Eligibility(ctx context.Context, owner, repo string, number int) trust.Decision
}

// manualDispatchGate refuses `nightgauge run <issue>` for an issue no user
// with write access vouched for. The error is the gate's reason: it names the
// author, their permission level and how to approve the issue.
func manualDispatchGate(ctx context.Context, asker eligibilityAsker, fullRepo string, number int) error {
	owner, repo, ok := strings.Cut(fullRepo, "/")
	if !ok || owner == "" || repo == "" {
		return errors.New("cannot check eligibility: repository must be owner/name, got " + fullRepo)
	}
	if d := asker.Eligibility(ctx, owner, repo, number); !d.Eligible {
		return errors.New(d.Reason)
	}
	return nil
}
