package orchestrator

// Regression tests for the Dependabot fast-track route (#345).
//
// These call applyDependabotFastTrack — the function runPipeline calls, with the
// arguments runPipeline passes — rather than the pure helpers underneath it, and
// they assert the SIDE EFFECTS, not just the returned stage list. That shape is
// the point.
//
// An earlier revision extracted only the pure helpers and asserted over those.
// Three separate mutations of the real routing seam left the entire
// orchestrator package green: reverting the predicate to the pre-#345
// board-item-label test, deleting runtime.SkipStage, and deleting the
// trace.KindStageSkip emission. A test that proves a property of a filter
// proves nothing about whether the run consults the filter, marks what it
// removed, or records why. So: one function does the decision AND the
// bookkeeping, the tests call that function, and each mutation is red.
//
// The defect the route itself pins was never a wrong stage list — the stage
// list was always right. It was that the route was keyed on
// `gh.IsDependabotIssue(item.Labels)` against a project BOARD ITEM, and board
// items are issues. Dependabot opens PULL REQUESTS and opens no issues at all,
// so the branch could only ever fire for an issue a human hand-created and
// hand-labelled. Every case below therefore states its artifact explicitly.

import (
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
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

// fastTrackHarness is one run's observable state: the runtime the skip
// bookkeeping lands on and the trace file the rationale lands in. Both are the
// REAL types runPipeline uses — no fakes — so an assertion here is an assertion
// about production behaviour.
type fastTrackHarness struct {
	rt     *state.RuntimeState
	tracer *trace.Writer
	root   string
	runID  string
}

func newFastTrackHarness(t *testing.T) *fastTrackHarness {
	t.Helper()
	root := t.TempDir()
	// Satisfies trace's run-id pattern (^[A-Za-z0-9_-]{8,128}$); a rejected id
	// yields a nil writer whose Emit is a silent no-op, which would make the
	// trace assertions below vacuous.
	runID := "test-run-dependabot-fasttrack"
	tracer := trace.NewWriter(root, runID, "acme/widgets", 101)
	if tracer == nil {
		t.Fatalf("trace.NewWriter returned nil for root=%q runID=%q — trace assertions would be vacuous", root, runID)
	}
	return &fastTrackHarness{
		rt:     state.NewRuntimeState("acme/widgets", 101, "item-101", runID),
		tracer: tracer,
		root:   root,
		runID:  runID,
	}
}

// skipEvents returns the stage_skip events actually written to the run's trace
// file, read back through the same reader the trace CLI uses.
func (h *fastTrackHarness) skipEvents(t *testing.T) []trace.Event {
	t.Helper()
	events, err := trace.ReadRun(h.root, h.runID)
	if err != nil {
		t.Fatalf("trace.ReadRun(%q, %q): %v", h.root, h.runID, err)
	}
	var out []trace.Event
	for _, ev := range events {
		if ev.Kind == trace.KindStageSkip {
			out = append(out, ev)
		}
	}
	return out
}

func stageNames(stages []state.PipelineStage) []string {
	out := make([]string, len(stages))
	for i, st := range stages {
		out[i] = string(st)
	}
	return out
}

func equalStages(got, want []state.PipelineStage) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestDependabotFastTrackAppliesOnlyToRemediationPullRequests is the core #345
// assertion, made at the seam: the route fires for the artifact Dependabot
// actually produces and does NOT fire for the artifact it never produces —
// and "does not fire" is asserted as three separate observable facts (route
// untouched, nothing marked skipped, nothing traced), not as one boolean.
//
// The Dependabot-labelled ISSUE case is the one that fails against the unfixed
// source. It is precisely what the old predicate matched, and matching it is
// the bug — not a harmless extra: it hands a hand-written issue straight past
// planning and implementation with no diff written for it.
func TestDependabotFastTrackAppliesOnlyToRemediationPullRequests(t *testing.T) {
	fastTracked := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	skippedWhenFastTracked := []state.PipelineStage{
		state.StageFeaturePlanning,
		state.StageFeatureDev,
	}

	tests := []struct {
		name        string
		item        types.BoardItem
		wantRoute   []state.PipelineStage
		wantSkipped []state.PipelineStage
	}{
		{
			name:        "Dependabot remediation PR — the artifact Dependabot produces",
			item:        types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}},
			wantRoute:   fastTracked,
			wantSkipped: skippedWhenFastTracked,
		},
		{
			name:        "Dependabot security remediation PR",
			item:        types.BoardItem{Number: 102, IsPR: true, Labels: []string{"dependencies", "security", "go"}},
			wantRoute:   fastTracked,
			wantSkipped: skippedWhenFastTracked,
		},
		{
			name:        "Dependabot-labelled ISSUE — the artifact Dependabot never produces",
			item:        types.BoardItem{Number: 103, IsPR: false, Labels: []string{"dependencies", "security"}},
			wantRoute:   fullStageOrder(),
			wantSkipped: nil,
		},
		{
			name:        "hand-created issue carrying an ecosystem label",
			item:        types.BoardItem{Number: 104, IsPR: false, Labels: []string{"javascript"}},
			wantRoute:   fullStageOrder(),
			wantSkipped: nil,
		},
		{
			name:        "ordinary feature PR — not Dependabot's",
			item:        types.BoardItem{Number: 105, IsPR: true, Labels: []string{"type:feature"}},
			wantRoute:   fullStageOrder(),
			wantSkipped: nil,
		},
		{
			name:        "unlabelled PR",
			item:        types.BoardItem{Number: 106, IsPR: true},
			wantRoute:   fullStageOrder(),
			wantSkipped: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFastTrackHarness(t)

			got := applyDependabotFastTrack(tt.item, fullStageOrder(), h.rt, h.tracer)

			if !equalStages(got, tt.wantRoute) {
				t.Errorf("route for #%d (isPR=%v labels=%v) = %v, want %v",
					tt.item.Number, tt.item.IsPR, tt.item.Labels,
					stageNames(got), stageNames(tt.wantRoute))
			}

			// The bookkeeping half. A run reports success on
			// completed + skipped == STAGE_ORDER, so removing a stage from the
			// route without recording it here turns a COMPLETED fast-tracked run
			// into a reported FAILURE.
			wantSkipNames := stageNames(tt.wantSkipped)
			if len(h.rt.SkippedStages) != len(wantSkipNames) {
				t.Fatalf("runtime.SkippedStages for #%d = %v, want %v",
					tt.item.Number, h.rt.SkippedStages, wantSkipNames)
			}
			for i, want := range wantSkipNames {
				if h.rt.SkippedStages[i] != want {
					t.Errorf("runtime.SkippedStages[%d] = %q, want %q (full %v)",
						i, h.rt.SkippedStages[i], want, h.rt.SkippedStages)
				}
			}

			// The rationale half (#179): an operator reading the run's trace has
			// to be able to learn why planning and dev never ran.
			events := h.skipEvents(t)
			if len(events) != len(wantSkipNames) {
				t.Fatalf("stage_skip trace events for #%d = %d, want %d",
					tt.item.Number, len(events), len(wantSkipNames))
			}
			for i, want := range wantSkipNames {
				if events[i].Stage != want {
					t.Errorf("stage_skip event[%d].Stage = %q, want %q", i, events[i].Stage, want)
				}
			}
		})
	}
}

// TestDependabotFastTrackTracesSourceAndRationale asserts the CONTENT of the
// emission, not merely that one happened: `source: dependabot` is what
// attributes the skip to this route rather than to the change-class fast-track,
// and the reason is the only place the rationale is recorded.
func TestDependabotFastTrackTracesSourceAndRationale(t *testing.T) {
	h := newFastTrackHarness(t)
	item := types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies", "security"}}

	applyDependabotFastTrack(item, fullStageOrder(), h.rt, h.tracer)

	events := h.skipEvents(t)
	if len(events) != 2 {
		t.Fatalf("stage_skip trace events = %d, want 2 (%s)",
			len(events), filepath.Join(h.root, ".nightgauge"))
	}
	for _, ev := range events {
		// Payload round-trips through JSONL as map[string]any, which is exactly
		// what every trace reader sees.
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("stage_skip payload for %q is %T, want a decoded object", ev.Stage, ev.Payload)
		}
		if got := payload["source"]; got != "dependabot" {
			t.Errorf("stage_skip[%s].source = %v, want %q", ev.Stage, got, "dependabot")
		}
		if got := payload["reason"]; got != dependabotFastTrackReason {
			t.Errorf("stage_skip[%s].reason = %v, want %q", ev.Stage, got, dependabotFastTrackReason)
		}
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
	skips := dependabotFastTrackSkipSet()

	want := []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev}
	if !equalStages(skips, want) {
		t.Fatalf("skip set = %v, want exactly %v", stageNames(skips), stageNames(want))
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

// TestDependabotFastTrackPartitionsWhatItRecords is the "skipped stages count
// toward success" assertion, tied to what the run ACTUALLY recorded rather than
// to a property of two pure helpers.
//
// A fast-tracked run reports success on `completed + skipped == STAGE_ORDER`.
// The stages the route keeps plus the stages the runtime was told about must
// therefore account for the full order exactly once: no stage lost, no stage
// counted twice. Reading the skipped half off runtime.SkippedStages — not off
// the skip-set helper — is what makes deleting the SkipStage call fail here.
func TestDependabotFastTrackPartitionsWhatItRecords(t *testing.T) {
	h := newFastTrackHarness(t)
	full := fullStageOrder()

	kept := applyDependabotFastTrack(
		types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}}, full, h.rt, h.tracer)

	seen := make(map[string]int, len(full))
	for _, st := range kept {
		seen[string(st)]++
	}
	for _, st := range h.rt.SkippedStages {
		seen[st]++
	}

	if len(kept)+len(h.rt.SkippedStages) != len(full) {
		t.Fatalf("kept (%d) + recorded-skipped (%d) = %d, want %d — a completed "+
			"fast-tracked run would report failure\n  kept=%v\n  skipped=%v",
			len(kept), len(h.rt.SkippedStages), len(kept)+len(h.rt.SkippedStages), len(full),
			stageNames(kept), h.rt.SkippedStages)
	}
	for _, st := range full {
		switch seen[string(st)] {
		case 1: // accounted for exactly once
		case 0:
			t.Errorf("%q is neither run nor recorded as skipped — it cannot count toward success", st)
		default:
			t.Errorf("%q is accounted for %d times (both run and skipped)", st, seen[string(st)])
		}
	}
	for name := range seen {
		found := false
		for _, f := range full {
			if string(f) == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is accounted for but is not in the stage order", name)
		}
	}
}

// TestDependabotFastTrackKeepsValidateCreateAndMerge asserts the surviving
// route, in order: issue-pickup → feature-validate → pr-create → pr-merge.
func TestDependabotFastTrackKeepsValidateCreateAndMerge(t *testing.T) {
	h := newFastTrackHarness(t)

	got := applyDependabotFastTrack(
		types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}},
		fullStageOrder(), h.rt, h.tracer)

	want := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}
	if !equalStages(got, want) {
		t.Fatalf("fast-track route = %v, want %v", stageNames(got), stageNames(want))
	}
}

// TestDependabotFastTrackLeavesStagesItDoesNotOwnAlone guards the partition
// against the hard-coded-list failure it was written to avoid: a spike run
// appends spike-materialize after pr-merge, and any stage the route does not
// recognise must survive untouched rather than being dropped on the floor.
func TestDependabotFastTrackLeavesStagesItDoesNotOwnAlone(t *testing.T) {
	h := newFastTrackHarness(t)
	full := append(fullStageOrder(), state.StageSpikeMaterialize)

	got := applyDependabotFastTrack(
		types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}}, full, h.rt, h.tracer)

	if len(got) == 0 || got[len(got)-1] != state.StageSpikeMaterialize {
		t.Errorf("fast-track dropped a stage it does not own: got %v, want spike-materialize last",
			stageNames(got))
	}
}

// TestDependabotFastTrackRecordsOnlyStagesTheOrderContained pins the half of the
// partition a skip-set literal would get wrong: `skipped` must name stages that
// were really in the order, or a run whose order never contained feature-dev
// reports two skips for one removal and its success arithmetic over-counts.
func TestDependabotFastTrackRecordsOnlyStagesTheOrderContained(t *testing.T) {
	h := newFastTrackHarness(t)
	// An order that already omits feature-dev — e.g. a route the change-class
	// fast-track narrowed first.
	partial := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureValidate,
		state.StagePRMerge,
	}

	kept := applyDependabotFastTrack(
		types.BoardItem{Number: 101, IsPR: true, Labels: []string{"dependencies"}}, partial, h.rt, h.tracer)

	want := []string{string(state.StageFeaturePlanning)}
	if len(h.rt.SkippedStages) != len(want) || h.rt.SkippedStages[0] != want[0] {
		t.Fatalf("runtime.SkippedStages = %v, want %v — feature-dev was never in the order",
			h.rt.SkippedStages, want)
	}
	if len(kept)+len(h.rt.SkippedStages) != len(partial) {
		t.Errorf("kept (%d) + recorded-skipped (%d) = %d, want %d",
			len(kept), len(h.rt.SkippedStages), len(kept)+len(h.rt.SkippedStages), len(partial))
	}
}
