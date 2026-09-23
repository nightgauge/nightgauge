package sweep

import (
	"context"
	"testing"

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

// countingIssues records which of the two "open work" readers was asked.
type countingIssues struct {
	strandedIssues
	listCalls, countCalls int
	count                 int
}

func (i *countingIssues) ListIssues(ctx context.Context, o, r string, l []string) ([]forgetypes.Issue, error) {
	i.listCalls++
	return i.strandedIssues.ListIssues(ctx, o, r, l)
}

func (i *countingIssues) CountOpenIssues(context.Context, string, string) (int, error) {
	i.countCalls++
	return i.count, nil
}

// The board answers reachability on its own when it holds any of the repo's
// items; the repo's open issues are only counted when it holds none, and then
// through the cheap counter.
func TestStrandedReady_BoardFirstThenCountsOpenIssues(t *testing.T) {
	issues := &countingIssues{count: 3}
	in := baseStrandedInput()
	in.Forge = &issuesOverride{
		ForgeClient: &strandedBoardForge{board: &strandedBoard{openItems: []forgetypes.BoardItem{{Number: 1, Repo: "nightgauge/nightgauge"}}}},
		issues:      issues,
	}
	cov, err := (&StrandedReadyItems{}).boardUnreachable(context.Background(), in)
	if err != nil || cov != nil {
		t.Fatalf("reachable board = %+v, %v", cov, err)
	}
	if issues.listCalls+issues.countCalls != 0 {
		t.Fatalf("a reachable board still read the repo's issues (list=%d count=%d)", issues.listCalls, issues.countCalls)
	}

	in.Forge = &issuesOverride{ForgeClient: &strandedBoardForge{board: &strandedBoard{}}, issues: issues}
	cov, err = (&StrandedReadyItems{}).boardUnreachable(context.Background(), in)
	if err != nil || cov == nil || cov.OpenIssues != 3 {
		t.Fatalf("unreachable board = %+v, %v; want 3 open issues", cov, err)
	}
	if issues.countCalls != 1 || issues.listCalls != 0 {
		t.Fatalf("open work read via list=%d count=%d, want the counter once", issues.listCalls, issues.countCalls)
	}
}

type issuesOverride struct {
	forge.ForgeClient
	issues forge.IssueService
}

func (o *issuesOverride) Issues() forge.IssueService { return o.issues }
