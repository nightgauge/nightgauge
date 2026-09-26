package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ctxCapturingPRCreateRunner records the route-skip signal the scheduler
// hands the deterministic arm and replays DecideCreate on a snapshot shaped
// like the run: dev context present, and a validate context only when
// feature-validate actually ran.
type ctxCapturingPRCreateRunner struct {
	skipped []string
}

func (f *ctxCapturingPRCreateRunner) Run(ctx context.Context, n int, _, _ string) (pmstages.PRCreateResult, error) {
	f.skipped = pmstages.RouteSkippedStages(ctx)
	snap := pmstages.PRCreateSnapshot{
		IssueNumber:   n,
		IssueType:     "docs",
		Branch:        "docs/1-x",
		BaseBranch:    "main",
		HasDev:        true,
		FilesModified: []string{"docs/a.md"},
	}
	validateSkipped := false
	for _, s := range f.skipped {
		if s == string(state.StageFeatureValidate) {
			validateSkipped = true
		}
	}
	snap.ValidateSkippedByRoute = validateSkipped
	if !validateSkipped {
		snap.HasValidate, snap.ValidationStatus = true, "passed"
	}
	d := pmstages.DecideCreate(snap)
	if d.ShouldCreate {
		return pmstages.PRCreateResult{Path: pmstages.CreatePathCreated, PRNumber: 1, Reason: d.Reason}, nil
	}
	return pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: d.Reason}, nil
}

// skipListsUnderTest is every skip list a routing class can produce: each
// built-in change rule's list plus every subset the scheduler honours (a user
// change_rule may name any of them).
func skipListsUnderTest() map[string][]string {
	out := map[string][]string{
		"none":          {},
		"planning":      {"feature-planning"},
		"validate":      {"feature-validate"},
		"planning+val":  {"feature-planning", "feature-validate"},
		"all-skippable": {"feature-planning", "feature-validate", "pr-create", "pr-merge"},
	}
	for _, r := range routing.DefaultChangeRules() {
		out["rule:"+r.Name] = r.SkipStages
	}
	return out
}

// TestRoutingSkipLists_CompatibleWithDeterministicArms (#1968 ask b) — no
// skip list a routing class can carry may make the pr-create deterministic arm
// punt on a missing-*-context precondition. pr-merge's deterministic arm needs
// only pr-{N}.json, which pr-create (never skippable) writes.
func TestRoutingSkipLists_CompatibleWithDeterministicArms(t *testing.T) {
	for name, skip := range skipListsUnderTest() {
		t.Run(name, func(t *testing.T) {
			s := newSchedulerForDeterministicTest()
			det := &ctxCapturingPRCreateRunner{}
			s.WithPRCreateRunner(det)
			rs := state.NewRuntimeState("owner/repo", 1, "item", testRunID())
			for st := range schedulerSkippableStages(skip) {
				rs.SkipStage(st)
			}
			if skips := schedulerSkippableStages(skip); skips[state.StagePRCreate] || skips[state.StagePRMerge] {
				t.Fatalf("scheduler honours a skip of pr-create/pr-merge: %v", skips)
			}
			rs.BeginStage(state.StagePRCreate)
			created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs,
				types.BoardItem{Number: 1, Repo: "owner/repo"}, "/tmp")
			if reason := rs.StagePuntReasons[string(state.StagePRCreate)]; strings.HasPrefix(reason, "missing-") {
				t.Fatalf("skip list %v punts pr-create with %q", skip, reason)
			}
			if !created {
				t.Fatalf("skip list %v: pr-create did not take its deterministic path", skip)
			}
		})
	}
}

// TestDocsOnlyRoute_ReachesDeterministicPRCreate (#1968) — an issue the
// routing seam classifies docs-only skips feature-validate, and pr-create
// still takes its deterministic path, recording that validate was skipped.
func TestDocsOnlyRoute_ReachesDeterministicPRCreate(t *testing.T) {
	d := routing.Derive(routing.DeriveInput{
		Title:  "docs: fix a typo in the README",
		Labels: []string{"type:docs", "component:docs", "size:XS"},
	})
	if d.MatchedChangeRule != "docs-only" {
		t.Fatalf("matched rule = %q, want docs-only (decision %+v)", d.MatchedChangeRule, d)
	}
	s := newSchedulerForDeterministicTest()
	det := &ctxCapturingPRCreateRunner{}
	s.WithPRCreateRunner(det)
	rs := state.NewRuntimeState("owner/repo", 1, "item", testRunID())
	for st := range schedulerSkippableStages(d.SkipStages) {
		rs.SkipStage(st)
	}
	rs.BeginStage(state.StagePRCreate)
	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs,
		types.BoardItem{Number: 1, Repo: "owner/repo"}, "/tmp")
	if !created {
		t.Fatalf("docs-only route punted pr-create: %q", rs.StagePuntReasons[string(state.StagePRCreate)])
	}
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "deterministic" {
		t.Errorf("execution path = %q, want deterministic", got)
	}
}
