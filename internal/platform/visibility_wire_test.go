package platform

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Every event type a private run sends names its visibility (#2400); a team
// run's events omit the field, which the service reads as team.
func TestBuildEventWire_Visibility(t *testing.T) {
	success := true
	for _, eventType := range []string{"stage_started", "stage_progress", "stage_completed", "stage_error", "pipeline_done"} {
		base := PipelineEvent{
			RunID: "01900309-0000-7000-8000-000000000001", IssueNumber: 1, EventType: eventType,
			Stage: "feature-dev", Timestamp: time.Now(), Success: &success,
		}
		private := base
		private.Visibility = state.VisibilityPrivate
		if got := buildEventWire(private)["visibility"]; got != state.VisibilityPrivate {
			t.Errorf("%s: private run wire visibility = %v, want private", eventType, got)
		}
		if _, ok := buildEventWire(base)["visibility"]; ok {
			t.Errorf("%s: team run wire carries visibility; want it omitted", eventType)
		}
		malformed := base
		malformed.Visibility = "public"
		if _, ok := buildEventWire(malformed)["visibility"]; ok {
			t.Errorf("%s: a malformed visibility reached the wire", eventType)
		}
	}
}

// The completion record carries a private run's visibility and omits a team
// run's.
func TestExecutionHistoryRunRecord_Visibility(t *testing.T) {
	rec := state.V2RunRecord{IssueNumber: 7, StartedAt: "2026-04-01T10:00:00Z", Outcome: "complete", Visibility: state.VisibilityPrivate}
	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"visibility":"private"`) {
		t.Errorf("private record wire lacks visibility: %s", b)
	}

	rec.Visibility = ""
	got, err = V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	b, _ = json.Marshal(got)
	if strings.Contains(string(b), `"visibility"`) {
		t.Errorf("team record wire carries visibility: %s", b)
	}
}

// The queue snapshot names a private item's visibility per item.
func TestQueueSyncItem_VisibilityWire(t *testing.T) {
	b, _ := json.Marshal(QueueSyncPayload{MachineID: "m", Origin: "local_cli", Items: []QueueSyncItem{
		{IssueNumber: 1, Position: 1, Status: "pending", Visibility: state.VisibilityPrivate},
		{IssueNumber: 2, Position: 2, Status: "pending"},
	}})
	var payload struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Items[0]["visibility"] != "private" {
		t.Errorf("private item wire = %v, want visibility private", payload.Items[0])
	}
	if _, ok := payload.Items[1]["visibility"]; ok {
		t.Errorf("team item wire carries visibility: %v", payload.Items[1])
	}
}
