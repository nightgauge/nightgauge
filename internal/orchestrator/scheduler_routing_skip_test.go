package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestEffectivePrereqContextType(t *testing.T) {
	skipped := func(stages ...state.PipelineStage) *state.RuntimeState {
		rs := &state.RuntimeState{}
		for _, s := range stages {
			rs.SkipStage(s)
		}
		return rs
	}

	t.Run("issue-pickup has no prerequisite", func(t *testing.T) {
		if _, ok := effectivePrereqContextType(state.StageIssuePickup, &state.RuntimeState{}); ok {
			t.Error("issue-pickup should have no prerequisite")
		}
	})

	t.Run("no skips: immediate prereq", func(t *testing.T) {
		ct, ok := effectivePrereqContextType(state.StageFeatureDev, &state.RuntimeState{})
		if !ok || ct != "planning" {
			t.Errorf("feature-dev prereq = %q (ok=%v), want planning", ct, ok)
		}
	})

	t.Run("docs-only: feature-dev walks past skipped planning to issue", func(t *testing.T) {
		rs := skipped(state.StageFeaturePlanning, state.StageFeatureValidate)
		ct, ok := effectivePrereqContextType(state.StageFeatureDev, rs)
		if !ok || ct != "issue" {
			t.Errorf("feature-dev prereq (planning skipped) = %q, want issue", ct)
		}
	})

	t.Run("docs-only: pr-create walks past skipped validate to dev", func(t *testing.T) {
		rs := skipped(state.StageFeaturePlanning, state.StageFeatureValidate)
		ct, ok := effectivePrereqContextType(state.StagePRCreate, rs)
		if !ok || ct != "dev" {
			t.Errorf("pr-create prereq (validate skipped) = %q, want dev", ct)
		}
	})

	t.Run("pr-merge prereq is pr (pr-create not skipped)", func(t *testing.T) {
		rs := skipped(state.StageFeaturePlanning, state.StageFeatureValidate)
		ct, ok := effectivePrereqContextType(state.StagePRMerge, rs)
		if !ok || ct != "pr" {
			t.Errorf("pr-merge prereq = %q, want pr", ct)
		}
	})
}

func TestSchedulerSkippableStages(t *testing.T) {
	t.Run("maps planning and validate", func(t *testing.T) {
		got := schedulerSkippableStages([]string{"feature-planning", "feature-validate"})
		if !got[state.StageFeaturePlanning] || !got[state.StageFeatureValidate] {
			t.Errorf("planning/validate not mapped: %v", got)
		}
	})
	t.Run("never skips dev/pr-create/pr-merge even if listed", func(t *testing.T) {
		got := schedulerSkippableStages([]string{"feature-dev", "pr-create", "pr-merge"})
		if len(got) != 0 {
			t.Errorf("unsafe stages mapped as skippable: %v", got)
		}
	})
	t.Run("ignores unknown stage names", func(t *testing.T) {
		got := schedulerSkippableStages([]string{"not-a-stage"})
		if len(got) != 0 {
			t.Errorf("unknown stage mapped: %v", got)
		}
	})
}

// routingWorkspace writes a minimal project config.yaml (optionally with a
// routing block) under a temp dir and returns the dir for deriveRoutingDecision.
// Delegates to the package's writeWorkspaceConfig helper (ship_notify_test.go).
func routingWorkspace(t *testing.T, routingBlock string) string {
	t.Helper()
	return writeWorkspaceConfig(t, "owner: nightgauge\nproject:\n  number: 1\n  repo: nightgauge\n"+routingBlock)
}

func TestDeriveRoutingDecision_DocsSkips(t *testing.T) {
	// No routing block → built-in defaults apply; a type:docs item matches the
	// docs-only default rule and skips planning + validate.
	dir := routingWorkspace(t, "")
	d := deriveRoutingDecision(dir, types.BoardItem{
		Number: 1,
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs"},
		Size:   "S",
	})
	if d.MatchedChangeRule != "docs-only" {
		t.Errorf("MatchedChangeRule = %q, want docs-only", d.MatchedChangeRule)
	}
	skips := schedulerSkippableStages(d.SkipStages)
	if !skips[state.StageFeaturePlanning] || !skips[state.StageFeatureValidate] {
		t.Errorf("docs-only should skip planning+validate, got %v", d.SkipStages)
	}
}

func TestDeriveRoutingDecision_ConfigOnlySkips(t *testing.T) {
	dir := routingWorkspace(t, "")
	d := deriveRoutingDecision(dir, types.BoardItem{
		Number: 2,
		Title:  "tweak the .yaml file",
		Size:   "S",
	})
	if d.ChangeType != "config" {
		t.Fatalf("precondition: ChangeType = %q, want config", d.ChangeType)
	}
	if d.MatchedChangeRule != "config-only" {
		t.Errorf("MatchedChangeRule = %q, want config-only", d.MatchedChangeRule)
	}
	skips := schedulerSkippableStages(d.SkipStages)
	if skips[state.StageFeaturePlanning] {
		t.Errorf("config-only should NOT skip planning, got %v", d.SkipStages)
	}
	if !skips[state.StageFeatureValidate] {
		t.Errorf("config-only should skip validate, got %v", d.SkipStages)
	}
}

func TestDeriveRoutingDecision_ForceFullPipeline(t *testing.T) {
	// force_full_pipeline disables all skipping even for a docs change.
	dir := routingWorkspace(t, "routing:\n  force_full_pipeline: true\n")
	d := deriveRoutingDecision(dir, types.BoardItem{
		Number: 3,
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs"},
		Size:   "S",
	})
	if len(schedulerSkippableStages(d.SkipStages)) != 0 {
		t.Errorf("force_full_pipeline must skip nothing, got %v", d.SkipStages)
	}
}

func TestDeriveRoutingDecision_RiskFloor(t *testing.T) {
	// A high-risk label floors the route and forces the full pipeline.
	dir := routingWorkspace(t, "")
	d := deriveRoutingDecision(dir, types.BoardItem{
		Number: 4,
		Title:  "update auth docs",
		Labels: []string{"type:docs", "component:security"},
		Size:   "S",
	})
	if !d.RiskHigh {
		t.Fatalf("precondition: RiskHigh = false, want true")
	}
	if len(schedulerSkippableStages(d.SkipStages)) != 0 {
		t.Errorf("risk floor must skip nothing, got %v", d.SkipStages)
	}
}

func TestDeriveRoutingDecision_CodeNoSkip(t *testing.T) {
	// A standard code feature matches no rule and skips nothing.
	dir := routingWorkspace(t, "")
	d := deriveRoutingDecision(dir, types.BoardItem{
		Number:   5,
		Title:    "implement parser",
		Labels:   []string{"type:feature"},
		Size:     "M",
		Priority: "P1",
	})
	if len(schedulerSkippableStages(d.SkipStages)) != 0 {
		t.Errorf("standard code change must skip nothing, got %v", d.SkipStages)
	}
}
