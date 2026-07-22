package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// depsMockFetcher implements hooks.IssueFetcher for testing without a network.
type depsMockFetcher struct {
	issues map[string]*types.Issue
}

func (m *depsMockFetcher) GetIssue(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if issue, ok := m.issues[key]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue not found: %s", key)
}

func newTestScheduler(t *testing.T) *orchestrator.Scheduler {
	t.Helper()
	return orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: t.TempDir()})
}

func TestDepsGateCmd_HasCheckAndPromote(t *testing.T) {
	cmd := depsGateCmd()
	if cmd.Use != "deps-gate" {
		t.Errorf("Use = %q, want deps-gate", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	if !subs["check"] {
		t.Error("missing 'check' subcommand")
	}
	if !subs["promote"] {
		t.Error("missing 'promote' subcommand")
	}
}

func TestEvaluateDepsGate_DefersOnOpenBlocker(t *testing.T) {
	mock := &depsMockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1459": {
			Number: 1459,
			BlockedBy: []types.BlockingRef{
				{Number: 1457, Title: "PlatformApiClient", State: "OPEN", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	res, err := evaluateDepsGate(context.Background(), mock, "nightgauge", "nightgauge", 1459)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != depsDecisionDeferred {
		t.Errorf("Decision = %q, want deferred", res.Decision)
	}
	if res.OpenCount != 1 {
		t.Errorf("OpenCount = %d, want 1", res.OpenCount)
	}
	if len(res.OpenDependencies) != 1 || res.OpenDependencies[0].Number != 1457 {
		t.Errorf("open_dependencies did not name blocker #1457: %+v", res.OpenDependencies)
	}
	if !strings.Contains(res.Reason, "#1457") {
		t.Errorf("Reason should name the blocker, got %q", res.Reason)
	}
}

func TestEvaluateDepsGate_AllowsWhenNoOpenBlockers(t *testing.T) {
	mock := &depsMockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			Number: 100,
			BlockedBy: []types.BlockingRef{
				{Number: 99, Title: "Done", State: "CLOSED", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	res, err := evaluateDepsGate(context.Background(), mock, "nightgauge", "nightgauge", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != depsDecisionAllow {
		t.Errorf("Decision = %q, want allow", res.Decision)
	}
	if res.OpenCount != 0 {
		t.Errorf("OpenCount = %d, want 0", res.OpenCount)
	}
}

func deferredResult(issueNum, blocker int, title string) depsGateCheckResult {
	return depsGateCheckResult{
		IssueNumber: issueNum,
		Decision:    depsDecisionDeferred,
		OpenDependencies: []hooks.OpenDependency{
			{Number: blocker, Title: title, State: "OPEN", Repo: "nightgauge/nightgauge"},
		},
		OpenCount: 1,
		Reason:    fmt.Sprintf("blocked by open dependency #%d (PR not merged)", blocker),
	}
}

// TestPauseBlockedDependencyItem_NamesBlockers verifies the pause records the
// blocked_dependency kind with the open blockers named.
func TestPauseBlockedDependencyItem_NamesBlockers(t *testing.T) {
	s := newTestScheduler(t)

	pauseBlockedDependencyItem(s, "nightgauge", "nightgauge", 1459, "Downstream feature", deferredResult(1459, 1457, "PlatformApiClient"))

	paused := s.ListPausedByKind("blocked_dependency")
	if len(paused) != 1 {
		t.Fatalf("ListPausedByKind returned %d items, want 1", len(paused))
	}
	pr := paused[0].PausedReason
	if pr == nil || pr.Kind != "blocked_dependency" {
		t.Fatalf("PausedReason missing/wrong kind: %+v", pr)
	}
	if len(pr.BlockingIssues) != 1 || pr.BlockingIssues[0].Number != 1457 {
		t.Errorf("BlockingIssues did not name blocker #1457: %+v", pr.BlockingIssues)
	}
}

// TestDepsGatePromoteSweep_ResumesWhenBlockerClosed verifies the promote sweep
// resumes a paused blocked_dependency item once its blocker is CLOSED.
func TestDepsGatePromoteSweep_ResumesWhenBlockerClosed(t *testing.T) {
	s := newTestScheduler(t)

	pauseBlockedDependencyItem(s, "nightgauge", "nightgauge", 1459, "Downstream", deferredResult(1459, 1457, "PlatformApiClient"))

	// Blocker #1457 is now CLOSED, so #1459 has no open blockers.
	mock := &depsMockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1459": {
			Number: 1459,
			BlockedBy: []types.BlockingRef{
				{Number: 1457, Title: "PlatformApiClient", State: "CLOSED", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	summary := depsGatePromoteSweep(context.Background(), s, mock, "nightgauge")
	if len(summary.Promoted) != 1 || summary.Promoted[0].IssueNumber != 1459 {
		t.Fatalf("expected #1459 promoted, got %+v", summary.Promoted)
	}
	if len(s.ListPausedByKind("blocked_dependency")) != 0 {
		t.Error("expected no remaining blocked_dependency paused items after promote")
	}
}

// TestDepsGatePromoteSweep_KeepsPausedWhenBlockerOpen verifies the promote sweep
// leaves an item paused while its blocker remains OPEN.
func TestDepsGatePromoteSweep_KeepsPausedWhenBlockerOpen(t *testing.T) {
	s := newTestScheduler(t)

	pauseBlockedDependencyItem(s, "nightgauge", "nightgauge", 1459, "Downstream", deferredResult(1459, 1457, "PlatformApiClient"))

	mock := &depsMockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#1459": {
			Number: 1459,
			BlockedBy: []types.BlockingRef{
				{Number: 1457, Title: "PlatformApiClient", State: "OPEN", Repo: "nightgauge/nightgauge"},
			},
		},
	}}

	summary := depsGatePromoteSweep(context.Background(), s, mock, "nightgauge")
	if len(summary.Promoted) != 0 {
		t.Errorf("expected nothing promoted while blocker open, got %+v", summary.Promoted)
	}
	if len(summary.StillPaused) != 1 {
		t.Errorf("expected #1459 still paused, got %+v", summary.StillPaused)
	}
	if len(s.ListPausedByKind("blocked_dependency")) != 1 {
		t.Error("expected item to remain paused")
	}
}

func TestOwnerRepoForItem(t *testing.T) {
	o, r := ownerRepoForItem("acme/platform", "fallback")
	if o != "acme" || r != "platform" {
		t.Errorf("got %q/%q, want acme/platform", o, r)
	}
	o, r = ownerRepoForItem("bare-repo", "fallbackOwner")
	if o != "fallbackOwner" || r != "bare-repo" {
		t.Errorf("got %q/%q, want fallbackOwner/bare-repo", o, r)
	}
}
