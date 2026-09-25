package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A remote run request's pin (#1656) is set once and survives the snapshot,
// the runtime's JSON projection and the V2 run record, next to the served
// stage adapter, so a hop shows as the difference between the two.
func TestRequestedPinSurvivesIntoTheRunRecord(t *testing.T) {
	rs := NewRuntimeState("o/r", 1656, "item", testRunID())
	rs.SetRequestedPin("opencode", "lmstudio/qwen/qwen3.8-27b")
	rs.SetRequestedPin("claude", "claude-sonnet-5") // set-once: a later call never rewrites it
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageAdapter(StageIssuePickup, "claude") // what served after a hop
	rs.CompleteStageWithCost(0, 10, 10, 0, 0)

	snap := rs.Snapshot()
	if snap.RequestedAdapter != "opencode" || snap.RequestedModel != "lmstudio/qwen/qwen3.8-27b" {
		t.Fatalf("snapshot pin = %q %q", snap.RequestedAdapter, snap.RequestedModel)
	}
	raw, _ := json.Marshal(snap)
	if !strings.Contains(string(raw), `"requestedAdapter":"opencode"`) || !strings.Contains(string(raw), `"requestedModel":"lmstudio/qwen/qwen3.8-27b"`) {
		t.Fatalf("runtime JSON lacks the pin: %s", raw)
	}

	rec := NewHistoryWriter(t.TempDir()).BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())
	if rec.RequestedAdapter != "opencode" || rec.RequestedModel != "lmstudio/qwen/qwen3.8-27b" {
		t.Fatalf("record pin = %q %q", rec.RequestedAdapter, rec.RequestedModel)
	}
	rj, _ := json.Marshal(rec)
	if !strings.Contains(string(rj), `"requested_adapter":"opencode"`) || !strings.Contains(string(rj), `"requested_model":"lmstudio/qwen/qwen3.8-27b"`) {
		t.Fatalf("record JSON lacks the pin: %s", rj)
	}
}

func TestNoRequestedPinLeavesTheRecordUnchanged(t *testing.T) {
	rs := NewRuntimeState("o/r", 1, "item", testRunID())
	rec := NewHistoryWriter(t.TempDir()).BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())
	rj, _ := json.Marshal(rec)
	if strings.Contains(string(rj), "requested_") {
		t.Fatalf("an unpinned run's record names a request: %s", rj)
	}
}
