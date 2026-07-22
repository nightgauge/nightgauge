package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFixture(t *testing.T, dir string, issueNumber int, rs *RuntimeState) {
	t.Helper()
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(dir, stateFileName(issueNumber))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func stateFileName(n int) string {
	return filepath.Base(filepath.Join(".", "runtime-"+(func() string {
		s := ""
		v := n
		if v == 0 {
			return "0"
		}
		for v > 0 {
			s = string(rune('0'+v%10)) + s
			v /= 10
		}
		return s
	}())+".json"))
}

func TestLocalStateService_NoFile_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	svc := NewLocalStateService(dir)
	got := svc.GetState("99")
	if got != nil {
		t.Errorf("expected nil for missing state file, got %v", got)
	}
}

func TestLocalStateService_InvalidKey_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	svc := NewLocalStateService(dir)
	for _, key := range []string{"", "abc", "-1", "0"} {
		got := svc.GetState(key)
		if got != nil {
			t.Errorf("key %q: expected nil, got %v", key, got)
		}
	}
}

func TestLocalStateService_RunningPipeline(t *testing.T) {
	dir := t.TempDir()
	rs := &RuntimeState{
		Repo:        "acme/myrepo",
		IssueNumber: 42,
		Stage:       PipelineStage("feature-dev"),
		StartedAt:   time.Now().UTC().Truncate(time.Second),
		StageErrors: make(map[string]string),
	}
	// Write manually (not using Persist to keep test simple)
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-42.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := NewLocalStateService(dir)
	got := svc.GetState("42")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", got)
	}
	if m["status"] != "running" {
		t.Errorf("expected status running, got %v", m["status"])
	}
	if m["stage"] != "feature-dev" {
		t.Errorf("expected stage feature-dev, got %v", m["stage"])
	}
	if m["issueNumber"] != 42 {
		t.Errorf("expected issueNumber 42, got %v", m["issueNumber"])
	}
	if m["startedAt"] == "" || m["startedAt"] == nil {
		t.Error("expected startedAt to be set")
	}
}

func TestLocalStateService_CompletedPipeline(t *testing.T) {
	dir := t.TempDir()
	// Build a completed runtime state (6 stages completed)
	rs := &RuntimeState{
		IssueNumber: 10,
		Stage:       PipelineStage("pr-merge"),
		StartedAt:   time.Now().UTC().Truncate(time.Second),
		StageErrors: make(map[string]string),
	}
	stages := []PipelineStage{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge",
	}
	for _, s := range stages {
		rs.CompletedStages = append(rs.CompletedStages, StageResult{Stage: s})
	}

	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-10.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	svc := NewLocalStateService(dir)
	got := svc.GetState("10")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	m := got.(map[string]interface{})
	if m["status"] != "completed" {
		t.Errorf("expected status completed, got %v", m["status"])
	}
}
