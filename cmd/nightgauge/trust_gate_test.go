package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/trust"
)

type labelApprovalForge struct{}

func (labelApprovalForge) RepoPermission(_ context.Context, _, _, login string) (trust.Level, error) {
	switch login {
	case "maintainer":
		return trust.Level("write"), nil
	case "reporter":
		return trust.Level("read"), nil
	}
	return "", fmt.Errorf("REST 404: %s is not a collaborator", login)
}

func (labelApprovalForge) IssueSignals(context.Context, string, string, int) (trust.Signals, error) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return trust.Signals{Author: "reporter", Labels: []string{trust.ApprovedLabel}, BodyEditedAt: at,
		Events: []trust.Event{{Kind: trust.EventLabeled, Label: trust.ApprovedLabel, Actor: "maintainer", At: at.Add(time.Hour)}}}, nil
}

func (labelApprovalForge) IssueComments(context.Context, string, string, int) ([]trust.Comment, error) {
	return nil, nil
}

// TestConfiguredTrustGateAcceptsLabelApprovalInAppOwner: an issue vouched for
// only by pipeline:approved in a GitHub App owner is eligible through the
// gate `issue partial-delivery` vouches its body with, as on the scheduler.
// A bare trust.NewGate refuses it (#3242).
func TestConfiguredTrustGateAcceptsLabelApprovalInAppOwner(t *testing.T) {
	prev := stageIdentitiesResolver
	stageIdentitiesResolver = func(context.Context, []string) ([]string, error) { return []string{"pipeline-bot"}, nil }
	t.Cleanup(func() { stageIdentitiesResolver = prev })

	cfg := &config.Config{GitHubAuth: &config.GitHubAuthConfig{App: &config.GitHubAppConfig{
		ID: "1", Installations: map[string]int64{"acme": 7}}}}
	subject := trust.Subject{Owner: "acme", Repo: "widgets", Number: 5}

	if d := newConfiguredTrustGate(labelApprovalForge{}, cfg).Decide(context.Background(), subject); !d.Eligible {
		t.Fatalf("configured gate refused a label-approved issue in an App owner: %s", d.Reason)
	}
	if d := trust.NewGate(labelApprovalForge{}, nil).Decide(context.Background(), subject); d.Eligible {
		t.Fatalf("bare gate accepted label approval; the test no longer distinguishes the two gates")
	}
}
