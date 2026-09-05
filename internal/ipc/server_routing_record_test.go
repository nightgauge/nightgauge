// #1484: the interactive/HeadlessOrchestrator terminal funnel must record the
// route the run was actually picked up under and the stages that route actually
// removed.
//
// It used to hard-code `RoutingPath: "standard"` with no SkipStages, so every
// record this path wrote said "standard route, nothing skipped" — even for a
// run whose own trace carried `stage_skip {"source":"routing"}` lines. That is
// the self-contradictory record #1482 observed: `skip_stages` declared next to
// a stage that ran. Note which half was broken — the TS orchestrator's
// EXECUTION was correct (it consults its routing decision before every stage
// and reports each skip over `pipeline.notifyStageTransition` with status
// "skipped"); only the RECORD lied.
package ipc

import (
	"testing"
)

// A trivial-route run whose planning and validate stages were skipped must
// record the trivial route AND both skipped stages.
func TestNotifyComplete_RecordsRoutingPathAndSkipStages(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	writeIssueContext(t, dir, 1484, `{
	  "type": "bug",
	  "labels": ["type:bug", "size:XS"],
	  "routing": {
	    "complexity_score": 1,
	    "suggested_route": "trivial",
	    "skip_stages": ["feature-planning", "feature-validate"]
	  }
	}`)

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]
	const runID = "019001c1-1484-7000-8000-000000001484"

	// Exactly what HeadlessOrchestrator does when shouldSkipStage() fires:
	// PipelineStateService.skipStage sends status "skipped", which the Go
	// handler folds into rt.SkipStage.
	for _, stage := range []string{"feature-planning", "feature-validate"} {
		if _, err := transition(t.Context(), []byte(
			`{"repo":"nightgauge/acmeapp","issueNumber":1484,"stage":"`+stage+
				`","status":"skipped","runId":"`+runID+`"}`)); err != nil {
			t.Fatalf("notifyStageTransition(skipped, %s): %v", stage, err)
		}
	}
	if _, err := transition(t.Context(), []byte(
		`{"repo":"nightgauge/acmeapp","issueNumber":1484,"stage":"feature-dev","status":"running","branch":"fix/1484","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(
		`{"repo":"nightgauge/acmeapp","issueNumber":1484,"success":true,"totalDurationMs":1000,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	rec := records[0]

	if rec.Routing.Path != "trivial" {
		t.Errorf("routing.path = %q, want %q — the record must carry the route from the issue context's suggested_route, not the hard-coded default",
			rec.Routing.Path, "trivial")
	}
	// Order follows the transitions, which follow the stage order.
	want := []string{"feature-planning", "feature-validate"}
	if len(rec.Routing.SkipStages) != len(want) {
		t.Fatalf("routing.skip_stages = %v, want %v", rec.Routing.SkipStages, want)
	}
	for i, w := range want {
		if rec.Routing.SkipStages[i] != w {
			t.Errorf("routing.skip_stages[%d] = %q, want %q (full %v)", i, rec.Routing.SkipStages[i], w, rec.Routing.SkipStages)
		}
	}
	// The stages the record says were skipped must also be absent from the
	// stages it says ran — the exact contradiction #1482 saw.
	for _, w := range want {
		if st, ok := rec.Stages[w]; ok && st.Status == "complete" {
			t.Errorf("stage %q is listed in skip_stages AND recorded complete — the record contradicts itself", w)
		}
	}
	if rec.Routing.ComplexityScore != 1 {
		t.Errorf("routing.complexity_score = %d, want 1", rec.Routing.ComplexityScore)
	}
}

// A run with no routing skips keeps the unknown-route spelling that
// BuildV2Record owns, and an empty (never nil) skip list — so the fix above
// cannot be mistaken for "always write trivial".
func TestNotifyComplete_RecordsStandardWhenNothingSkipped(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	writeIssueContext(t, dir, 1485, `{"type":"feature","labels":["size:M"],"routing":{"complexity_score":5}}`)

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]
	const runID = "019001c1-1485-7000-8000-000000001485"

	if _, err := transition(t.Context(), []byte(
		`{"repo":"nightgauge/acmeapp","issueNumber":1485,"stage":"feature-dev","status":"running","branch":"feat/1485","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(
		`{"repo":"nightgauge/acmeapp","issueNumber":1485,"success":true,"totalDurationMs":1000,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	if got := records[0].Routing.Path; got != "standard" {
		t.Errorf("routing.path = %q, want %q for a context naming no route", got, "standard")
	}
	if records[0].Routing.SkipStages == nil {
		t.Error("routing.skip_stages must serialize as [], never null")
	}
	if len(records[0].Routing.SkipStages) != 0 {
		t.Errorf("routing.skip_stages = %v, want empty", records[0].Routing.SkipStages)
	}
}

// The route comes from `suggested_route`. `path` on an issue context is the run
// record's spelling leaking upstream and must not be honoured (#1484).
func TestLoadIssueClassification_RouteKeyIsSuggestedRoute(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 1484, `{"routing":{"suggested_route":"trivial","path":"extensive"}}`)

	if got := loadIssueClassification(root, "", 1484).SuggestedRoute; got != "trivial" {
		t.Errorf("SuggestedRoute = %q, want trivial — `path` is not a key on an issue context", got)
	}
}
