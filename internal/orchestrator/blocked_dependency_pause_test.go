package orchestrator

import (
	"sync"
	"testing"
)

// TestPauseDeferred_BlockedDependency_RoundTrip verifies a blocked_dependency
// pause is added with its blockers named, listed by kind, and resumed by issue
// number — the deps-gate check → promote lifecycle (Issue #231). Mirrors the
// baseline-CI pause round-trip (baseline_pause_test.go).
func TestPauseDeferred_BlockedDependency_RoundTrip(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}

	reason := QueuePausedReason{
		Kind:    "blocked_dependency",
		Summary: "blocked by open dependency #123 (PR not merged)",
		BlockingIssues: []QueueBlockingRef{
			{Number: 123, Title: "PlatformApiClient", State: "OPEN"},
		},
	}
	s.PauseDeferred(QueueItem{Repo: "nightgauge/nightgauge", IssueNumber: 42, Title: "Downstream"}, reason)

	if len(s.queue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(s.queue))
	}
	item := s.queue[0]
	if item.Status != "paused" {
		t.Errorf("Status = %q, want paused", item.Status)
	}
	if item.PausedReason == nil || item.PausedReason.Kind != "blocked_dependency" {
		t.Fatalf("PausedReason missing or wrong kind: %+v", item.PausedReason)
	}
	if len(item.PausedReason.BlockingIssues) != 1 || item.PausedReason.BlockingIssues[0].Number != 123 {
		t.Errorf("BlockingIssues did not name blocker #123: %+v", item.PausedReason.BlockingIssues)
	}

	// ListPausedByKind is kind-agnostic — it must surface the new kind unchanged.
	got := s.ListPausedByKind("blocked_dependency")
	if len(got) != 1 || got[0].IssueNumber != 42 {
		t.Fatalf("ListPausedByKind(blocked_dependency) = %+v, want #42", got)
	}
	// The baseline kind must NOT match a blocked_dependency item.
	if len(s.ListPausedByKind("baseline_ci_red")) != 0 {
		t.Error("blocked_dependency item leaked into baseline_ci_red filter")
	}

	// Resume clears the pause.
	if !s.ResumeByIssueNumber(42) {
		t.Fatal("expected ResumeByIssueNumber(42) to return true")
	}
	if s.queue[0].Status != "pending" {
		t.Errorf("Status = %q, want pending after resume", s.queue[0].Status)
	}
	if s.queue[0].PausedReason != nil {
		t.Errorf("PausedReason should be cleared, got %+v", s.queue[0].PausedReason)
	}
}

// TestPauseDeferred_BlockedDependency_Idempotent verifies re-pausing the same
// issue updates the reason without duplicating the queue entry.
func TestPauseDeferred_BlockedDependency_Idempotent(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.queue = []QueueItem{{IssueNumber: 7, Status: "pending", Title: "existing"}}

	s.PauseDeferred(QueueItem{IssueNumber: 7}, QueuePausedReason{
		Kind:           "blocked_dependency",
		BlockingIssues: []QueueBlockingRef{{Number: 99, State: "OPEN"}},
	})
	if len(s.queue) != 1 {
		t.Fatalf("len = %d, want 1 (idempotent)", len(s.queue))
	}
	if s.queue[0].Status != "paused" {
		t.Errorf("Status = %q, want paused", s.queue[0].Status)
	}
	if s.queue[0].Title != "existing" {
		t.Errorf("Title was overwritten on existing item: %q", s.queue[0].Title)
	}
	if s.queue[0].PausedReason == nil || s.queue[0].PausedReason.Kind != "blocked_dependency" {
		t.Errorf("PausedReason not updated: %+v", s.queue[0].PausedReason)
	}
}
