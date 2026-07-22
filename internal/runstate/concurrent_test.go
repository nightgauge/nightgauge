package runstate

import (
	"errors"
	"os"
	"testing"
)

// TestDetectConcurrent_FreshDir reports no concurrent run when the dir is empty.
func TestDetectConcurrent_FreshDir(t *testing.T) {
	dir := t.TempDir()
	concurrent, err := DetectConcurrent(dir)
	if concurrent || err != nil {
		t.Errorf("expected no concurrent run; got concurrent=%v err=%v", concurrent, err)
	}
}

// TestDetectConcurrent_StaleWriter accepts a record whose writer PID is dead.
// We craft one with a PID that is guaranteed to be unused (PID 1 may be alive
// — use math.MaxInt32 instead).
func TestDetectConcurrent_StaleWriter(t *testing.T) {
	dir := t.TempDir()
	stage := StageIssuePickup
	pid := 0x7fffffff // very unlikely to be alive
	host := "test-host"
	rs := &RunState{
		SchemaVersion:   SchemaVersion,
		IssueNumber:     1,
		State:           StateRunning,
		RunID:           "00000000-0000-7000-8000-000000000000",
		AttemptNumber:   1,
		CompletedStages: []Stage{},
		ResumeFromStage: &stage,
		Branch:          "feat/x",
		CreatedAt:       "2026-05-06T00:00:00Z",
		UpdatedAt:       "2026-05-06T00:00:00Z",
		Attempts: []Attempt{{
			RunID:         "00000000-0000-7000-8000-000000000000",
			AttemptNumber: 1,
			StartedAt:     "2026-05-06T00:00:00Z",
			PID:           &pid,
			HostID:        &host,
			LastStage:     &stage,
		}},
	}
	if err := Save(dir, rs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	concurrent, err := DetectConcurrent(dir)
	if concurrent {
		t.Errorf("stale writer treated as concurrent (PID was %d)", pid)
	}
	if err != nil {
		t.Errorf("expected nil err on stale writer; got %v", err)
	}
}

// TestDetectConcurrent_LiveWriter is positive: own PID is alive → concurrent.
func TestDetectConcurrent_LiveWriter(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{
		IssueNumber: 1,
		Branch:      "feat/x",
	}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	concurrent, refusal := DetectConcurrent(dir)
	if !concurrent || refusal == nil {
		t.Errorf("expected concurrent (own PID alive); got concurrent=%v refusal=%v", concurrent, refusal)
	}
	var typed *ConcurrentRunRefusedError
	if !errors.As(refusal, &typed) {
		t.Errorf("refusal is not a *ConcurrentRunRefusedError: %T", refusal)
	}
	if typed.HolderPID != os.Getpid() {
		t.Errorf("HolderPID = %d; want %d", typed.HolderPID, os.Getpid())
	}
}
