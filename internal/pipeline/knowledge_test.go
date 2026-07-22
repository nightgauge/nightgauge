package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

func writeEventsFixture(t *testing.T, root string, events []telemetry.Event) {
	t.Helper()
	path := knowledgeEventsPathFor(root)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestAggregateKnowledge_MissingFile(t *testing.T) {
	root := t.TempDir()
	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agg.EventsTotal != 0 {
		t.Fatalf("expected 0 events, got %d", agg.EventsTotal)
	}
	if agg.ByType == nil || agg.ByStage == nil || agg.ByScope == nil {
		t.Fatalf("maps must be non-nil for stable JSON shape")
	}
}

func TestAggregateKnowledge_CountsByTypeStageScope(t *testing.T) {
	root := t.TempDir()
	writeEventsFixture(t, root, []telemetry.Event{
		{Type: telemetry.EventScaffold, Stage: "issue-pickup", Scope: "issue:42"},
		{Type: telemetry.EventRead, Stage: "feature-dev", Scope: "issue:42"},
		{Type: telemetry.EventRead, Stage: "feature-dev", Scope: "issue:42"},
		{Type: telemetry.EventGraduate, Stage: "feature-validate", Scope: "issue:42"},
		{Type: telemetry.EventRecall, Stage: "feature-planning", Scope: "workspace"},
	})

	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	if got, want := agg.EventsTotal, 5; got != want {
		t.Fatalf("total: got %d want %d", got, want)
	}
	if got, want := agg.ByType["read"], 2; got != want {
		t.Fatalf("by_type.read: got %d want %d", got, want)
	}
	if got, want := agg.ByType["scaffold"], 1; got != want {
		t.Fatalf("by_type.scaffold: got %d want %d", got, want)
	}
	if got, want := agg.ByStage["feature-dev"]["read"], 2; got != want {
		t.Fatalf("by_stage.feature-dev.read: got %d want %d", got, want)
	}
	if got, want := agg.ByScope["issue:42"], 4; got != want {
		t.Fatalf("by_scope.issue:42: got %d want %d", got, want)
	}
	if got, want := agg.ByScope["workspace"], 1; got != want {
		t.Fatalf("by_scope.workspace: got %d want %d", got, want)
	}
}

func TestAggregateKnowledge_RecallHitRate(t *testing.T) {
	root := t.TempDir()
	writeEventsFixture(t, root, []telemetry.Event{
		{Type: telemetry.EventRecall, Stage: "feature-dev"},
		{Type: telemetry.EventRecall, Stage: "feature-dev"},
		{Type: telemetry.EventRecall, Stage: "feature-dev"},
		{Type: telemetry.EventRecall, Stage: "feature-dev"},
		{Type: telemetry.EventRecallHit, Stage: "feature-dev"},
	})

	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got, want := agg.RecallHitRate, 0.25; got != want {
		t.Fatalf("recall_hit_rate: got %v want %v", got, want)
	}
}

func TestAggregateKnowledge_RecallHitRateZeroWhenNoRecall(t *testing.T) {
	root := t.TempDir()
	writeEventsFixture(t, root, []telemetry.Event{
		{Type: telemetry.EventScaffold, Stage: "issue-pickup"},
	})
	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if agg.RecallHitRate != 0 {
		t.Fatalf("expected 0 when no recalls, got %v", agg.RecallHitRate)
	}
}

func TestAggregateKnowledge_SkipsMalformedLines(t *testing.T) {
	root := t.TempDir()
	path := knowledgeEventsPathFor(root)
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	// Mix valid + malformed lines.
	data := []byte(`{"type":"read","stage":"feature-dev","scope":"issue:1"}
this is not json
{"type":"scaffold","stage":"issue-pickup","scope":"issue:1"}
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got, want := agg.EventsTotal, 2; got != want {
		t.Fatalf("events_total: got %d want %d (malformed should be skipped)", got, want)
	}
}

func TestAggregateKnowledge_UnknownStageBucket(t *testing.T) {
	root := t.TempDir()
	writeEventsFixture(t, root, []telemetry.Event{
		{Type: telemetry.EventRead, Stage: "", Scope: "issue:7"},
	})
	agg, err := AggregateKnowledge(root)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got, want := agg.ByStage["unknown"]["read"], 1; got != want {
		t.Fatalf("unknown stage bucket: got %d want %d", got, want)
	}
}

func TestAggregateIntegration_KnowledgeInResult(t *testing.T) {
	// Aggregate() should produce a Result with Knowledge zero-valued (the
	// pure path does not read files); the CLI layers AggregateKnowledge in.
	res, _ := Aggregate(nil, Options{})
	if res.Knowledge.EventsTotal != 0 {
		t.Fatalf("Aggregate must leave Knowledge zero-valued")
	}
	if res.Knowledge.ByType == nil {
		t.Fatalf("Knowledge maps must be initialized for stable JSON shape")
	}
}
