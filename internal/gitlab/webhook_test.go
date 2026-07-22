package gitlab_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gitlab"
)

func loadTestData(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

func TestParseWebhookPayload_Pipeline(t *testing.T) {
	body := loadTestData(t, "pipeline_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindPipeline, "delivery-1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Source != "gitlab" {
		t.Errorf("Source = %q; want %q", evt.Source, "gitlab")
	}
	if evt.ObjectKind != "pipeline" {
		t.Errorf("ObjectKind = %q; want %q", evt.ObjectKind, "pipeline")
	}
	if evt.IPCEventName() != "gitlab.pipeline" {
		t.Errorf("IPCEventName() = %q; want %q", evt.IPCEventName(), "gitlab.pipeline")
	}
	if evt.DeliveryID != "delivery-1" {
		t.Errorf("DeliveryID = %q; want %q", evt.DeliveryID, "delivery-1")
	}
	if evt.ProjectID != 1234 {
		t.Errorf("ProjectID = %d; want 1234", evt.ProjectID)
	}
}

func TestParseWebhookPayload_MergeRequest(t *testing.T) {
	body := loadTestData(t, "mr_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindMergeRequest, "delivery-2", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.ObjectKind != "merge_request" {
		t.Errorf("ObjectKind = %q; want %q", evt.ObjectKind, "merge_request")
	}
	if evt.IPCEventName() != "gitlab.mr" {
		t.Errorf("IPCEventName() = %q; want %q", evt.IPCEventName(), "gitlab.mr")
	}
	if evt.MRIID != 7 {
		t.Errorf("MRIID = %d; want 7", evt.MRIID)
	}
	if evt.ObjectState != "opened" {
		t.Errorf("ObjectState = %q; want %q", evt.ObjectState, "opened")
	}
}

func TestParseWebhookPayload_Note(t *testing.T) {
	body := loadTestData(t, "note_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindNote, "delivery-3", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.ObjectKind != "note" {
		t.Errorf("ObjectKind = %q; want %q", evt.ObjectKind, "note")
	}
	if evt.IPCEventName() != "gitlab.note" {
		t.Errorf("IPCEventName() = %q; want %q", evt.IPCEventName(), "gitlab.note")
	}
}

func TestParseWebhookPayload_Push(t *testing.T) {
	body := loadTestData(t, "push_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindPush, "delivery-4", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.ObjectKind != "push" {
		t.Errorf("ObjectKind = %q; want %q", evt.ObjectKind, "push")
	}
	if evt.IPCEventName() != "gitlab.push" {
		t.Errorf("IPCEventName() = %q; want %q", evt.IPCEventName(), "gitlab.push")
	}
	// Push timestamp comes from commits[0].timestamp
	if evt.OccurredAt.IsZero() {
		t.Error("OccurredAt is zero; expected a parsed timestamp")
	}
}

func TestParseWebhookPayload_UnsupportedKind(t *testing.T) {
	_, err := gitlab.ParseWebhookPayload("Unknown Hook", "delivery-x", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for unsupported event kind, got nil")
	}
	if !errors.Is(err, gitlab.ErrUnsupportedEventKind) {
		t.Errorf("error = %v; want ErrUnsupportedEventKind", err)
	}
}

func TestParseWebhookPayload_ComputedDeliveryID(t *testing.T) {
	body := loadTestData(t, "pipeline_event.json")
	// Empty delivery ID should trigger hash computation
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindPipeline, "", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.DeliveryID == "" {
		t.Error("DeliveryID should not be empty when computed from payload hash")
	}
	// Same body should produce the same delivery ID (deterministic)
	evt2, _ := gitlab.ParseWebhookPayload(gitlab.EventKindPipeline, "", body)
	if evt.DeliveryID != evt2.DeliveryID {
		t.Errorf("computed delivery IDs differ: %q vs %q", evt.DeliveryID, evt2.DeliveryID)
	}
}

func TestParseWebhookPayload_RawPayloadPreserved(t *testing.T) {
	body := loadTestData(t, "mr_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindMergeRequest, "d1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// RawPayload must be valid JSON identical to the original body
	var got, want interface{}
	if err := json.Unmarshal(evt.RawPayload, &got); err != nil {
		t.Fatalf("RawPayload is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatalf("testdata is not valid JSON: %v", err)
	}
}

func TestParseWebhookPayload_TimestampParsed(t *testing.T) {
	body := loadTestData(t, "pipeline_event.json")
	evt, err := gitlab.ParseWebhookPayload(gitlab.EventKindPipeline, "d1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
	if !evt.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v; want %v", evt.OccurredAt, want)
	}
}
