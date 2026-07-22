package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

// KnowledgeAggregate is the additive `knowledge` block emitted by
// `pipeline aggregate --json`. Counts come from one streaming pass over
// knowledge-events.jsonl. ADR-006 (issue #3592) treats this as an additive
// schema extension — no SchemaVersion bump because no existing field is
// renamed or removed.
type KnowledgeAggregate struct {
	EventsTotal   int                       `json:"events_total"`
	ByType        map[string]int            `json:"by_type"`
	ByStage       map[string]map[string]int `json:"by_stage"`
	ByScope       map[string]int            `json:"by_scope"`
	RecallHitRate float64                   `json:"recall_hit_rate"`
}

// emptyKnowledgeAggregate returns a zero-valued aggregate with non-nil maps
// so JSON consumers see `{}` rather than `null` for the breakdown fields.
// Matches the convention used elsewhere in this package
// (StageAgg.Status, ModelUsage maps).
func emptyKnowledgeAggregate() KnowledgeAggregate {
	return KnowledgeAggregate{
		ByType:  map[string]int{},
		ByStage: map[string]map[string]int{},
		ByScope: map[string]int{},
	}
}

// AggregateKnowledge streams the knowledge-events.jsonl file under
// workspaceRoot and returns a KnowledgeAggregate. A missing file is NOT an
// error — it returns the zero-valued aggregate (consistent with how the run
// aggregator treats a missing history directory).
//
// The function intentionally lives outside Aggregate() because the run
// aggregator is pure-in-memory (takes []V2RunRecord); knowledge events live
// in a separate JSONL stream and need their own file IO. Callers chain both
// when building the full pipeline aggregate result.
func AggregateKnowledge(workspaceRoot string) (KnowledgeAggregate, error) {
	agg := emptyKnowledgeAggregate()

	path := telemetry.Path(workspaceRoot)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return agg, nil
		}
		return agg, fmt.Errorf("open knowledge events: %w", err)
	}
	defer f.Close()

	var recalls, hits int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			// Skip malformed lines defensively — telemetry is best-effort
			// and a single corrupt line must not block aggregation.
			continue
		}
		agg.EventsTotal++
		t := string(ev.Type)
		if t == "" {
			t = "unknown"
		}
		agg.ByType[t]++

		stage := ev.Stage
		if stage == "" {
			stage = "unknown"
		}
		if _, ok := agg.ByStage[stage]; !ok {
			agg.ByStage[stage] = map[string]int{}
		}
		agg.ByStage[stage][t]++

		if ev.Scope != "" {
			agg.ByScope[ev.Scope]++
		}

		switch ev.Type {
		case telemetry.EventRecall:
			recalls++
		case telemetry.EventRecallHit:
			hits++
		}
	}
	if err := scanner.Err(); err != nil {
		return agg, fmt.Errorf("scan knowledge events: %w", err)
	}

	if recalls > 0 {
		agg.RecallHitRate = round4(float64(hits) / float64(recalls))
	}
	return agg, nil
}

// LoadKnowledgeEvents streams knowledge-events.jsonl and returns the raw
// events. Used by `knowledge stats --stale` to compute per-path last-read
// timestamps without re-deriving the aggregate counters.
//
// Missing file returns nil, nil — same convention as AggregateKnowledge.
// Malformed lines are skipped silently.
func LoadKnowledgeEvents(workspaceRoot string) ([]telemetry.Event, error) {
	path := telemetry.Path(workspaceRoot)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open knowledge events: %w", err)
	}
	defer f.Close()

	var events []telemetry.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("scan knowledge events: %w", err)
	}
	return events, nil
}

// knowledgeEventsPathFor builds the knowledge-events.jsonl path. Exported via
// telemetry.Path; this internal helper exists for tests that want to write a
// fixture without depending on telemetry.
func knowledgeEventsPathFor(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history", "knowledge-events.jsonl")
}
