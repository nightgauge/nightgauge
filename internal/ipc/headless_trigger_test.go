package ipc

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

func TestTriggerIssueBusy_QueuedIssueIsBusy(t *testing.T) {
	s, _ := newRemotePinServer(t)
	if s.TriggerIssueBusy("acme/api", 7) {
		t.Fatal("an issue with no run here is busy")
	}
	s.scheduler.QueueAddItem(orchestrator.QueueItem{Repo: "acme/api", IssueNumber: 7})
	if !s.TriggerIssueBusy("acme/api", 7) {
		t.Error("a queued issue is not busy")
	}
	if s.TriggerIssueBusy("acme/api", 8) {
		t.Error("another issue is busy")
	}
}

func TestValidateTriggerPin_UsesTheRemotePinValidation(t *testing.T) {
	s, calls := newRemotePinServer(t)
	if got := s.ValidateTriggerPin("acme/api", "opencode", "lmstudio/qwen/qwen3.8-27b"); got != "" {
		t.Errorf("an accepted pair was refused: %q", got)
	}
	if got := s.ValidateTriggerPin("acme/api", "opencode", "lmstudio/other/model"); got != "model-not-in-catalog" {
		t.Errorf("refusal = %q, want the public reason only", got)
	}
	if *calls != 2 {
		t.Errorf("validation calls = %d, want 2", *calls)
	}
}

func TestTriggerIssueBusy_RegisteredBusyCheckCounts(t *testing.T) {
	s, _ := newRemotePinServer(t)
	s.AddIssueBusyCheck(func(repo string, n int) bool { return repo == "acme/api" && n == 9 })
	if !s.TriggerIssueBusy("acme/api", 9) {
		t.Error("an issue a registered check reports running is not busy")
	}
	if !s.issueRunningHere("acme/api", 9) {
		t.Error("issueRunningHere, which a cloud queue_add consults, misses it")
	}
	if s.TriggerIssueBusy("acme/api", 10) {
		t.Error("another issue is busy")
	}
}
