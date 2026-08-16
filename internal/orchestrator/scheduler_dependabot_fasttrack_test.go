package orchestrator

// Regression tests for the Dependabot fast-track route (#345).
//
// The defect these pin was not a wrong stage list — the stage list was always
// right. It was that the route was keyed on `gh.IsDependabotIssue(item.Labels)`
// against a project BOARD ITEM, and board items are issues. Dependabot opens
// PULL REQUESTS and opens no issues at all, so the branch could only ever fire
// for an issue a human hand-created and hand-labelled. A test that only asserts
// "Dependabot work skips planning and dev" passes against that bug; the
// assertion that has to hold is about WHICH ARTIFACT the route fires for, so
// every case below states the artifact explicitly.

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// fullStageOrder mirrors the stage list runPipeline builds before any
// fast-track adjustment. Declared here rather than imported so a change to the
// pipeline's stage order shows up as a failure in the partition test below
// instead of silently redefining what the test proves.
func fullStageOrder() []state.PipelineStage {
	return []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
}

// TestDependabotFastTrackRoutesOnThePullRequest is the core #345 assertion: the
// route fires for the artifact Dependabot actually produces and does NOT fire
// for the artifact it never produces.
//
// The second case is the one that fails against the unfixed source. A
// Dependabot-LABELLED ISSUE is precisely what the old predicate matched, and
// matching it is the bug — not a harmless extra: it hands a hand-written issue
// straight past planning and implementation with no diff written for it.
func TestDependabotFastTrackRoutesOnThePullRequest(t *testing.T) {
	tests := []struct {
		name string
		item types.BoardItem
		want bool
	}{
		{
			name: "Dependabot remediation PR — the artifact Dependabot produces",
			item: types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}},
			want: true,
		},
		{
			name: "Dependabot security remediation PR",
			item: types.BoardItem{Number: 102, IsPR: true, Labels: []string{"dependencies", "security", "go"}},
			want: true,
		},
		{
			name: "Dependabot-labelled ISSUE — the artifact Dependabot never produces",
			item: types.BoardItem{Number: 103, IsPR: false, Labels: []string{"dependencies", "security"}},
			want: false,
		},
		{
			name: "hand-created issue carrying an ecosystem label",
			item: types.BoardItem{Number: 104, IsPR: false, Labels: []string{"javascript"}},
			want: false,
		},
		{
			name: "ordinary feature PR — not Dependabot's",
			item: types.BoardItem{Number: 105, IsPR: true, Labels: []string{"type:feature"}},
			want: false,
		},
		{
			name: "unlabelled PR",
			item: types.BoardItem{Number: 106, IsPR: true},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDependabotRemediationPR(tt.item); got != tt.want {
				t.Errorf("isDependabotRemediationPR(#%d isPR=%v labels=%v) = %v, want %v",
					tt.item.Number, tt.item.IsPR, tt.item.Labels, got, tt.want)
			}
		})
	}
}

// TestDependabotFastTrackSkipSetIsExactlyPlanningAndDev pins the skip set
// membership in both directions.
//
// The "must not contain" half is the load-bearing half. Fast-tracking skips
// planning and implementation, which add nothing to a version bump; it must
// never skip feature-validate, pr-create or pr-merge, because a dependency bump
// that breaks the build is a worse outcome than one that lands late. Anything
// that adds a stage here silently weakens the merge gate.
func TestDependabotFastTrackSkipSetIsExactlyPlanningAndDev(t *testing.T) {
	skips := dependabotFastTrackSkips()

	want := []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev}
	if len(skips) != len(want) {
		t.Fatalf("skip set = %v (%d stages), want exactly %v", skips, len(skips), want)
	}
	for i, st := range want {
		if skips[i] != st {
			t.Errorf("skip set[%d] = %q, want %q (full set %v)", i, skips[i], st, skips)
		}
	}

	inSkipSet := make(map[state.PipelineStage]bool, len(skips))
	for _, st := range skips {
		inSkipSet[st] = true
	}
	for _, mustRun := range []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	} {
		if inSkipSet[mustRun] {
			t.Errorf("%q is in the Dependabot skip set — the fast-track must not skip it; "+
				"merge stays gated on green CI", mustRun)
		}
	}
}

// TestDependabotFastTrackKeepsValidateCreateAndMerge asserts the surviving
// route, in order: issue-pickup → feature-validate → pr-create → pr-merge.
func TestDependabotFastTrackKeepsValidateCreateAndMerge(t *testing.T) {
	got := dependabotFastTrackStages(fullStageOrder())

	want := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	if len(got) != len(want) {
		t.Fatalf("fast-track route = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fast-track route = %v, want %v (differs at index %d)", got, want, i)
		}
	}
}

// TestDependabotFastTrackPartitionsTheStageOrder is the "skipped stages count
// toward success" assertion, expressed as the property that actually guarantees
// it.
//
// A fast-tracked run reports success on `completed + skipped == STAGE_ORDER`. A
// stage that is neither run nor marked skipped therefore makes a SUCCESSFUL run
// report FAILURE. The two halves must partition the full order exactly: no
// stage lost, no stage counted twice.
func TestDependabotFastTrackPartitionsTheStageOrder(t *testing.T) {
	full := fullStageOrder()
	kept := dependabotFastTrackStages(full)
	skipped := dependabotFastTrackSkips()

	if len(kept)+len(skipped) != len(full) {
		t.Fatalf("kept (%d) + skipped (%d) = %d, want %d — a fast-tracked run would "+
			"report failure on a complete run\n  kept=%v\n  skipped=%v",
			len(kept), len(skipped), len(kept)+len(skipped), len(full), kept, skipped)
	}

	seen := make(map[state.PipelineStage]int, len(full))
	for _, st := range kept {
		seen[st]++
	}
	for _, st := range skipped {
		seen[st]++
	}
	for _, st := range full {
		switch seen[st] {
		case 1: // accounted for exactly once
		case 0:
			t.Errorf("%q is neither run nor marked skipped — it cannot count toward success", st)
		default:
			t.Errorf("%q is accounted for %d times (both run and skipped)", st, seen[st])
		}
	}
	for st := range seen {
		found := false
		for _, f := range full {
			if f == st {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is accounted for but is not in the stage order", st)
		}
	}
}

// TestDependabotFastTrackStagesLeavesOtherRoutesAlone guards the filter against
// the hard-coded-list failure it was written to avoid: a spike run appends
// spike-materialize after pr-merge, and any stage the filter does not recognise
// must survive untouched rather than being dropped on the floor.
func TestDependabotFastTrackStagesLeavesOtherRoutesAlone(t *testing.T) {
	full := append(fullStageOrder(), state.StageSpikeMaterialize)
	got := dependabotFastTrackStages(full)

	if len(got) == 0 || got[len(got)-1] != state.StageSpikeMaterialize {
		t.Errorf("fast-track dropped a stage it does not own: got %v, want spike-materialize last", got)
	}
}
