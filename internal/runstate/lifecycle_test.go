package runstate

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestTransitionAllowed(t *testing.T) {
	cases := []struct {
		from, to Lifecycle
		want     bool
	}{
		{StateRunning, StatePaused, true},
		{StateRunning, StateCompleted, true},
		{StateRunning, StateAborted, true},
		{StateRunning, StateDiscarded, false},
		{StatePaused, StateRunning, true},
		{StatePaused, StateDiscarded, true},
		{StatePaused, StateCompleted, false},
		{StateAborted, StateDiscarded, true},
		{StateAborted, StateRunning, false},
		{StateCompleted, StateRunning, false},
		{StateCompleted, StateDiscarded, false},
		{StateDiscarded, StateRunning, false},
	}
	for _, c := range cases {
		if got := TransitionAllowed(c.from, c.to); got != c.want {
			t.Errorf("TransitionAllowed(%s, %s) = %v; want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestMarkRunning_Fresh(t *testing.T) {
	dir := t.TempDir()
	rs, err := MarkRunning(dir, MarkRunningOptions{
		IssueNumber: 42,
		Branch:      "feat/test",
	})
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if rs.State != StateRunning {
		t.Errorf("state = %s; want running", rs.State)
	}
	if rs.RunID == "" {
		t.Error("expected run_id")
	}
	if rs.AttemptNumber != 1 {
		t.Errorf("attempt = %d; want 1", rs.AttemptNumber)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RunID != rs.RunID {
		t.Errorf("loaded run_id = %s; want %s", loaded.RunID, rs.RunID)
	}
}

func TestMarkRunning_RefusesConcurrent(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{
		IssueNumber: 1,
		Branch:      "feat/x",
	}); err != nil {
		t.Fatalf("first MarkRunning: %v", err)
	}
	// Same PID is alive — second start must refuse.
	_, err := MarkRunning(dir, MarkRunningOptions{
		IssueNumber: 1,
		Branch:      "feat/x",
	})
	var concErr *ConcurrentRunRefusedError
	if !errors.As(err, &concErr) {
		t.Fatalf("err = %v; want ConcurrentRunRefusedError", err)
	}
	// Force = true overrides
	_, err = MarkRunning(dir, MarkRunningOptions{
		IssueNumber: 1,
		Branch:      "feat/x",
		Force:       true,
	})
	if err != nil {
		t.Fatalf("MarkRunning with Force: %v", err)
	}
}

func TestMarkPaused_StopPreservesEverything(t *testing.T) {
	dir := t.TempDir()
	rs, _ := MarkRunning(dir, MarkRunningOptions{
		IssueNumber:  7,
		Branch:       "feat/preserve",
		WorktreePath: "/tmp/wt",
	})
	stage := StageFeatureDev
	paused, err := MarkPaused(dir, "user clicked stop", &stage)
	if err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	if paused.State != StatePaused {
		t.Errorf("state = %s; want paused", paused.State)
	}
	if paused.RunID != rs.RunID {
		t.Error("RunID changed across stop — must be preserved")
	}
	if paused.WorktreePath == nil || *paused.WorktreePath != "/tmp/wt" {
		t.Error("worktree_path lost — stop must preserve it")
	}
	if paused.Branch != rs.Branch {
		t.Error("branch changed across stop")
	}
	if paused.Reason == nil || *paused.Reason != "user clicked stop" {
		t.Error("reason not recorded")
	}
	if len(paused.RecoveryActions) != 3 {
		t.Errorf("recovery_actions = %v; want 3 entries", paused.RecoveryActions)
	}
}

func TestMarkPaused_RejectsIllegalTransition(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{IssueNumber: 1, Branch: "b"}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := MarkCompleted(dir); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	stage := StageIssuePickup
	_, err := MarkPaused(dir, "", &stage)
	var ill *IllegalTransitionError
	if !errors.As(err, &ill) {
		t.Errorf("err = %v; want IllegalTransitionError", err)
	}
}

func TestResume_FromPausedAddsAttempt(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{IssueNumber: 9, Branch: "b"}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	stage := StageFeatureDev
	if _, err := MarkPaused(dir, "stop", &stage); err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	resumed, err := Resume(dir)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != StateRunning {
		t.Errorf("state = %s; want running", resumed.State)
	}
	if resumed.AttemptNumber != 2 {
		t.Errorf("attempt = %d; want 2", resumed.AttemptNumber)
	}
	if len(resumed.Attempts) != 2 {
		t.Errorf("attempts len = %d; want 2", len(resumed.Attempts))
	}
}

func TestMarkStageComplete_AdvancesResumeFrom(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{IssueNumber: 3, Branch: "b"}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	rs, err := MarkStageComplete(dir, StageIssuePickup)
	if err != nil {
		t.Fatalf("MarkStageComplete: %v", err)
	}
	if len(rs.CompletedStages) != 1 || rs.CompletedStages[0] != StageIssuePickup {
		t.Errorf("completed = %v; want [issue-pickup]", rs.CompletedStages)
	}
	if rs.ResumeFromStage == nil || *rs.ResumeFromStage != StageFeaturePlan {
		t.Errorf("resume_from = %v; want feature-planning", rs.ResumeFromStage)
	}
	// Idempotent
	rs2, err := MarkStageComplete(dir, StageIssuePickup)
	if err != nil {
		t.Fatalf("idempotent MarkStageComplete: %v", err)
	}
	if len(rs2.CompletedStages) != 1 {
		t.Errorf("expected idempotent; got %v", rs2.CompletedStages)
	}
}

func TestDetectResume_NoState_Orphaned(t *testing.T) {
	dir := t.TempDir()
	det, err := DetectResume(dir, "feat/orphan", false)
	if err != nil {
		t.Fatalf("DetectResume: %v", err)
	}
	if det.Kind != ResumeOrphaned {
		t.Errorf("kind = %s; want orphaned", det.Kind)
	}
	if !contains(det.Choices, "restart") || !contains(det.Choices, "manual-pickup") {
		t.Errorf("choices = %v; want restart+manual-pickup", det.Choices)
	}
}

func TestDetectResume_PausedSurfacesChoice(t *testing.T) {
	dir := t.TempDir()
	if _, err := MarkRunning(dir, MarkRunningOptions{IssueNumber: 1, Branch: "b"}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	stage := StageFeatureDev
	if _, err := MarkPaused(dir, "stop", &stage); err != nil {
		t.Fatalf("MarkPaused: %v", err)
	}
	det, err := DetectResume(dir, "b", true)
	if err != nil {
		t.Fatalf("DetectResume: %v", err)
	}
	if det.Kind != ResumePaused {
		t.Errorf("kind = %s; want paused", det.Kind)
	}
	if !contains(det.Choices, "resume") || !contains(det.Choices, "discard") {
		t.Errorf("choices = %v", det.Choices)
	}
}

func TestArchiveRun_MovesContextFiles(t *testing.T) {
	dir := t.TempDir()
	rs, err := MarkRunning(dir, MarkRunningOptions{IssueNumber: 11, Branch: "b"})
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	// Drop a fake context file
	if err := AtomicWriteFile(dir+"/issue-11.json", []byte(`{}`), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	archive, err := ArchiveRun(dir, rs)
	if err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}
	if !strings.Contains(archive, "history") || !strings.Contains(archive, rs.RunID) {
		t.Errorf("archive dir = %s; missing history/<runId>", archive)
	}
	// Live file removed
	if _, err := readFileExists(dir + "/issue-11.json"); err == nil {
		t.Error("expected issue-11.json removed from live dir")
	}
	// Archived file present
	if _, err := readFileExists(archive + "/issue-11.json"); err != nil {
		t.Errorf("expected issue-11.json in archive: %v", err)
	}
	// run-state snapshot in archive
	if _, err := readFileExists(archive + "/" + FileName); err != nil {
		t.Errorf("expected run-state snapshot in archive: %v", err)
	}
}

func TestHasContextFiles(t *testing.T) {
	dir := t.TempDir()
	if HasContextFiles(dir, 5) {
		t.Error("expected false on empty dir")
	}
	if err := AtomicWriteFile(dir+"/dev-5.json", []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasContextFiles(dir, 5) {
		t.Error("expected true after writing dev-5.json")
	}
	if HasContextFiles(dir, 6) {
		t.Error("expected false for unrelated issue")
	}
}

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func readFileExists(path string) ([]byte, error) {
	return os.ReadFile(path)
}
