package sweep

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Three producers read a repository's alerts in one pass; the pass reads them
// once per repository.
func TestSharedReads_OneAlertReadPerRepoPerPass(t *testing.T) {
	sec := &covSecurity{answers: map[string]covAnswer{
		"acme/web": {res: &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsEnabled}},
		"acme/api": {res: &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsDisabled}},
	}}
	shared := NewSharedReads()
	perRepo := shared.Wrap(&covForge{sec: sec})
	workspace := shared.Wrap(&covForge{sec: sec})
	ctx := context.Background()
	for _, c := range []forge.ForgeClient{perRepo, perRepo, workspace} {
		for _, repo := range []string{"web", "api"} {
			if _, err := c.Security().ListOpenAlerts(ctx, "acme", repo); err != nil {
				t.Fatal(err)
			}
		}
	}
	if sec.calls["acme/web"] != 1 || sec.calls["acme/api"] != 1 {
		t.Fatalf("alert reads = %v, want one per repository", sec.calls)
	}
	// A new pass reads again: the memo is per pass, not a cache.
	if _, err := NewSharedReads().Wrap(&covForge{sec: sec}).Security().ListOpenAlerts(ctx, "acme", "web"); err != nil {
		t.Fatal(err)
	}
	if sec.calls["acme/web"] != 2 {
		t.Fatalf("a second pass was served the first pass's answer")
	}
	// No security service stays no security service.
	if NewSharedReads().Wrap(&covForge{}).Security() != nil {
		t.Fatal("wrapping a client with no security service invented one")
	}
}

// probedPRs is gatePRs plus the cheap "any open PR?" probe.
type probedPRs struct {
	*gatePRs
	has       bool
	listCalls int
}

func (p *probedPRs) HasOpenPRs(context.Context, string, string) (bool, error) { return p.has, nil }

func (p *probedPRs) ListPRs(ctx context.Context, owner, repo, state, head string) ([]types.PullRequest, error) {
	p.listCalls++
	return p.gatePRs.ListPRs(ctx, owner, repo, state, head)
}

type probedGateForge struct {
	gateForge
	prs *probedPRs
}

func (f *probedGateForge) PRs() forge.PRService { return f.prs }

// With no open PR the GraphQL list (review decision, merge state, checks) is
// not read at all, and the observation is a positive empty one.
func TestHumanGate_SkipsTheGraphQLListWithoutOpenPRs(t *testing.T) {
	prs := &probedPRs{gatePRs: &gatePRs{}, has: false}
	in := gateInput(prs.gatePRs)
	in.Forge = &probedGateForge{prs: prs}
	got, err := (&HumanGate{}).Evaluate(context.Background(), in)
	if err != nil || got != nil {
		t.Fatalf("Evaluate = %v, %v; want a positive empty observation", got, err)
	}
	if prs.listCalls != 0 {
		t.Fatalf("ListPRs called %d times with no open PR", prs.listCalls)
	}
	prs.has = true
	if _, err := (&HumanGate{}).Evaluate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if prs.listCalls != 1 {
		t.Fatalf("ListPRs called %d times with an open PR, want 1", prs.listCalls)
	}
}

// deadlineSecurity fails the first call with the caller's own deadline, then
// answers.
type deadlineSecurity struct{ calls int }

func (d *deadlineSecurity) ListOpenAlerts(ctx context.Context, _, _ string) (*forgetypes.SecurityAlerts, error) {
	d.calls++
	if d.calls == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsEnabled}, nil
}

// One repo's sweep running out of time must not become another producer's
// answer: the next caller reads again under its own deadline.
func TestSharedReads_ALeadersDeadlineIsNotShared(t *testing.T) {
	sec := &deadlineSecurity{}
	shared := NewSharedReads()
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := shared.Wrap(&covForge{sec: sec}).Security().ListOpenAlerts(short, "acme", "web"); err == nil {
		t.Fatal("the leader's own deadline was not reported to it")
	}
	res, err := shared.Wrap(&covForge{sec: sec}).Security().ListOpenAlerts(context.Background(), "acme", "web")
	if err != nil || res == nil || !res.Enabled() {
		t.Fatalf("a later caller got %+v, %v; want its own successful read", res, err)
	}
	if sec.calls != 2 {
		t.Fatalf("reads = %d, want 2", sec.calls)
	}
}
