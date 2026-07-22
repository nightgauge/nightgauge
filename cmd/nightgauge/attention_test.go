package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
)

func seedRequest(t *testing.T, dir, key, title string, sev attention.Severity) string {
	t.Helper()
	store := attention.New(dir)
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: key,
		Kind:           attention.KindChoose,
		Severity:       sev,
		Title:          title,
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme", Issue: 3},
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return id
}

func TestAttentionListTable(t *testing.T) {
	dir := t.TempDir()
	seedRequest(t, dir, "k1", "Fleet stopped", attention.SeverityBlockingFleet)
	seedRequest(t, dir, "k2", "Budget ceiling hit", attention.SeverityBlockingRun)

	cmd := attentionListCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Fleet stopped") || !strings.Contains(out, "Budget ceiling hit") {
		t.Errorf("table missing rows:\n%s", out)
	}
	// Most-severe-first ordering: blocking_fleet row precedes blocking_run row.
	if strings.Index(out, "Fleet stopped") > strings.Index(out, "Budget ceiling hit") {
		t.Errorf("rows not ordered by severity:\n%s", out)
	}
}

func TestAttentionListEmpty(t *testing.T) {
	cmd := attentionListCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workdir", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "All clear") {
		t.Errorf("empty state missing: %q", buf.String())
	}
}

func TestAttentionResolveCLI(t *testing.T) {
	dir := t.TempDir()
	id := seedRequest(t, dir, "k1", "Choose", attention.SeverityFYI)

	cmd := attentionResolveCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{id, "--option", "leave", "--actor", "octocat", "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "Resolved") {
		t.Errorf("resolve output missing confirmation: %q", buf.String())
	}
	// Persisted state is terminal.
	got, found, err := attention.New(dir).Get(id)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.Lifecycle.State != attention.StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
	if got.Lifecycle.Resolved == nil || got.Lifecycle.Resolved.Actor != "octocat" {
		t.Error("resolve audit missing actor")
	}
}

func TestAttentionResolveRejectsUndeclaredOption(t *testing.T) {
	dir := t.TempDir()
	id := seedRequest(t, dir, "k1", "Choose", attention.SeverityFYI)
	cmd := attentionResolveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{id, "--option", "smuggled", "--workdir", dir})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for an undeclared option id")
	}
}
