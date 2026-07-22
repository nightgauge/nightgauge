package orchestrator

import (
	"sync"
	"testing"
)

// TestPauseDeferred_AddsNewItem verifies that PauseDeferred adds a new queue
// item when none exists for the issue number. (Issue #3004)
func TestPauseDeferred_AddsNewItem(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}

	reason := QueuePausedReason{
		Kind:         "baseline_ci_red",
		Summary:      "baseline-ci red: ci.yml failed 3/5",
		Workflow:     "ci.yml",
		Job:          "Integration & E2E Tests",
		FailedRuns:   3,
		LookbackRuns: 5,
	}
	s.PauseDeferred(QueueItem{Repo: "o/r", IssueNumber: 42, Title: "Test"}, reason)

	if len(s.queue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(s.queue))
	}
	item := s.queue[0]
	if item.Status != "paused" {
		t.Errorf("Status = %q, want paused", item.Status)
	}
	if item.PausedReason == nil || item.PausedReason.Kind != "baseline_ci_red" {
		t.Errorf("PausedReason missing or wrong kind: %+v", item.PausedReason)
	}
	if item.PausedReason.Workflow != "ci.yml" {
		t.Errorf("Workflow = %q, want ci.yml", item.PausedReason.Workflow)
	}
}

// TestPauseDeferred_UpdatesExistingItem ensures idempotency — calling twice
// with the same issue number replaces the PausedReason without duplicating
// the entry.
func TestPauseDeferred_UpdatesExistingItem(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.queue = []QueueItem{{IssueNumber: 7, Status: "pending", Title: "existing"}}

	s.PauseDeferred(QueueItem{IssueNumber: 7}, QueuePausedReason{Kind: "baseline_ci_red", Workflow: "ci.yml"})
	if len(s.queue) != 1 {
		t.Fatalf("len = %d, want 1 (idempotent)", len(s.queue))
	}
	if s.queue[0].Status != "paused" {
		t.Errorf("Status = %q, want paused", s.queue[0].Status)
	}
	if s.queue[0].Title != "existing" {
		t.Errorf("Title was overwritten on existing item: %q", s.queue[0].Title)
	}
}

func TestListPausedByKind_FiltersByKind(t *testing.T) {
	s := &Scheduler{repoRunning: make(map[string]int), mergeLocks: make(map[string]*sync.Mutex)}
	upstream := QueuePausedReason{Kind: "upstream_failure", FailedRunID: "1"}
	baseline := QueuePausedReason{Kind: "baseline_ci_red", Workflow: "ci.yml"}
	s.queue = []QueueItem{
		{IssueNumber: 1, Status: "paused", PausedReason: &upstream},
		{IssueNumber: 2, Status: "paused", PausedReason: &baseline},
		{IssueNumber: 3, Status: "pending"},
		{IssueNumber: 4, Status: "paused", PausedReason: &baseline},
	}

	got := s.ListPausedByKind("baseline_ci_red")
	if len(got) != 2 {
		t.Errorf("got %d items, want 2 (#2 and #4)", len(got))
	}
	for _, item := range got {
		if item.PausedReason.Kind != "baseline_ci_red" {
			t.Errorf("returned item kind = %q, want baseline_ci_red", item.PausedReason.Kind)
		}
	}
}

func TestResumeByIssueNumber_ResumesPaused(t *testing.T) {
	s := &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	reason := QueuePausedReason{Kind: "baseline_ci_red", Workflow: "ci.yml"}
	s.queue = []QueueItem{{IssueNumber: 99, Status: "paused", PausedReason: &reason}}

	if !s.ResumeByIssueNumber(99) {
		t.Fatal("expected ResumeByIssueNumber to return true")
	}
	if s.queue[0].Status != "pending" {
		t.Errorf("Status = %q, want pending", s.queue[0].Status)
	}
	if s.queue[0].PausedReason != nil {
		t.Errorf("PausedReason should be cleared, got %+v", s.queue[0].PausedReason)
	}
}

func TestResumeByIssueNumber_NotPausedReturnsFalse(t *testing.T) {
	s := &Scheduler{repoRunning: make(map[string]int), mergeLocks: make(map[string]*sync.Mutex)}
	s.queue = []QueueItem{{IssueNumber: 5, Status: "pending"}}
	if s.ResumeByIssueNumber(5) {
		t.Error("expected false when item is not paused")
	}
}

func TestResumeByIssueNumber_MissingReturnsFalse(t *testing.T) {
	s := &Scheduler{repoRunning: make(map[string]int), mergeLocks: make(map[string]*sync.Mutex)}
	if s.ResumeByIssueNumber(404) {
		t.Error("expected false for missing issue")
	}
}
