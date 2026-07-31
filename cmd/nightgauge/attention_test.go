package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// chdirTemp creates a temp workdir, chdirs into it, and restores the original
// cwd on cleanup. Needed because config.Load reads os.Getwd(), not --workdir.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore chdir: %v", err)
		}
	})
	return dir
}

// writeBareRepoConfig writes a .nightgauge/config.yaml with a bare (no-slash)
// repo name, mirroring the shape #222's PersistentPreRunE back-fill used to
// silently inject into owner/name-typed --repo flags.
func writeBareRepoConfig(t *testing.T, dir string) {
	t.Helper()
	nightgaugeDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(nightgaugeDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	body := "owner: nightgauge\ndefaultRepo: nightgauge\n"
	if err := os.WriteFile(filepath.Join(nightgaugeDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

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

// TestAttentionResolveCLI_NoDaemonLeavesCardOpen is the AC's named test: with
// no daemon reachable, resolving an option bound to a daemon-only verb must
// exit non-zero, name the verb and its retryability, and leave the request
// open on disk (#235).
func TestAttentionResolveCLI_NoDaemonLeavesCardOpen(t *testing.T) {
	dir := t.TempDir()
	store := attention.New(dir)
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "k1",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          "Choose",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme", Issue: 3},
		Options: []attention.Option{
			{ID: "clear", Label: "Clear failures", Verb: attention.VerbAutonomousClearIssueFailures},
		},
		DefaultAction: "clear",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	cmd := attentionResolveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{id, "--option", "clear", "--actor", "octocat", "--workdir", dir})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected non-zero exit when the verb requires the daemon")
	}
	if !strings.Contains(err.Error(), "autonomous.clearIssueFailures") {
		t.Errorf("error does not name the failed verb: %v", err)
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Errorf("error does not state retryability: %v", err)
	}

	got, found, gerr := attention.New(dir).Get(id)
	if gerr != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, gerr)
	}
	if got.Lifecycle.State != attention.StateOpen {
		t.Errorf("state = %q, want open (card must stay pending)", got.Lifecycle.State)
	}
	if got.Lifecycle.Resolved != nil {
		t.Error("resolved record set despite verb failure")
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

// TestAttentionListRootCommand_IgnoresBareConfigRepo is a regression test for
// #222: a bare (no-slash) config.yaml defaultRepo used to be silently
// back-filled into attention list's owner/name-typed --repo flag by
// PersistentPreRunE, filtering every card out and rendering "All clear". It
// must go through the assembled root command — building attentionListCmd()
// in isolation never exercises PersistentPreRunE and would miss this bug.
func TestAttentionListRootCommand_IgnoresBareConfigRepo(t *testing.T) {
	dir := chdirTemp(t)
	writeBareRepoConfig(t, dir)
	seedRequest(t, dir, "k1", "Fleet stopped", attention.SeverityBlockingFleet)

	cmd := rootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"attention", "list", "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "All clear") {
		t.Errorf("bare config repo silently filtered out the card:\n%s", out)
	}
	if !strings.Contains(out, "Fleet stopped") {
		t.Errorf("expected card title in output:\n%s", out)
	}
}

// TestAttentionListRootCommand_ExplicitRepoStillFilters confirms AC 2: an
// explicit --repo flag continues to filter, even through the root command's
// PersistentPreRunE (Changed("repo") short-circuits the back-fill either way).
func TestAttentionListRootCommand_ExplicitRepoStillFilters(t *testing.T) {
	dir := chdirTemp(t)
	writeBareRepoConfig(t, dir)
	seedRequest(t, dir, "k1", "Fleet stopped", attention.SeverityBlockingFleet)

	cmd := rootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"attention", "list", "--repo", "octocat/acme", "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "Fleet stopped") {
		t.Errorf("explicit --repo filter should still match the seeded card:\n%s", buf.String())
	}
}

// TestAttentionListRootCommand_FilteredToZero covers AC 4: a --repo filter
// that matches nothing must name the filter and the fleet-wide total, not
// print the bare "All clear" message used for a genuinely empty store.
func TestAttentionListRootCommand_FilteredToZero(t *testing.T) {
	dir := chdirTemp(t)
	writeBareRepoConfig(t, dir)
	seedRequest(t, dir, "k1", "Fleet stopped", attention.SeverityBlockingFleet)

	cmd := rootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"attention", "list", "--repo", "octocat/unrelated", "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "All clear") {
		t.Errorf("filtered-to-zero must not print the bare all-clear message:\n%s", out)
	}
	if !strings.Contains(out, "octocat/unrelated") {
		t.Errorf("expected the filter to be named in the output:\n%s", out)
	}
	if !strings.Contains(out, "1 total") {
		t.Errorf("expected fleet-wide total in output:\n%s", out)
	}
}
