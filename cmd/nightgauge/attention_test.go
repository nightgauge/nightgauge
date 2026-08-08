package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// startTestDaemon starts a Server's socket listener rooted at dir, wired to
// the same attention store dir (so requests raised via attention.New(dir)
// are visible to the daemon-side resolve path). Returns once the socket is
// dialable.
func startTestDaemon(t *testing.T, dir string) {
	t.Helper()
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), dir)
	srv := ipc.NewServer(nil, ipc.WithAutonomousScheduler(as))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sockPath := ipc.DaemonSocketPath(dir)
	go func() {
		_ = srv.ListenSocket(ctx, sockPath)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := ipc.DialClient(ctx, sockPath, 50*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test daemon socket never became dialable")
}

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
	if _, _, err := store.Raise(attention.DecisionRequest{
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
	if _, _, err := store.Raise(attention.DecisionRequest{
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

// shortTempDir returns a short-path temp dir. Unix domain sockets have a
// ~104-byte sun_path limit (macOS) — t.TempDir() embeds the full test name,
// which is too long once ".nightgauge/daemon.sock" is appended for a test
// with a long name. Used only by tests that dial a daemon socket.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ngsock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestAttentionResolveCLI_RoutesThroughDaemon proves Step 3's routing (#263):
// against a live daemon socket, a verb outside the CLI's local 3-verb subset
// (VerbAutonomousClearIssueFailures) succeeds via the daemon path, where
// TestAttentionResolveCLI_NoDaemonLeavesCardOpen shows the identical verb
// fails without a daemon.
func TestAttentionResolveCLI_RoutesThroughDaemon(t *testing.T) {
	dir := shortTempDir(t)
	startTestDaemon(t, dir)

	store := attention.New(dir)
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "daemon-route:1",
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
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{id, "--option", "clear", "--actor", "octocat", "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "Resolved") {
		t.Errorf("resolve output missing confirmation: %q", buf.String())
	}

	got, found, gerr := attention.New(dir).Get(id)
	if gerr != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, gerr)
	}
	if got.Lifecycle.State != attention.StateResolved {
		t.Errorf("state = %q, want resolved (daemon path should have executed the verb)", got.Lifecycle.State)
	}
}

// TestAttentionShowCLI_AnnotatesUnavailableOptions covers Step 5 (#263):
// without a daemon, a daemon-only-verb option is annotated as unavailable;
// with a daemon reachable, no option is annotated.
func TestAttentionShowCLI_AnnotatesUnavailableOptions(t *testing.T) {
	dir := shortTempDir(t)
	store := attention.New(dir)
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "show-annotate:1",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityFYI,
		Title:          "Choose",
		Producer:       "test",
		Options: []attention.Option{
			{ID: "clear", Label: "Clear failures", Verb: attention.VerbAutonomousClearIssueFailures},
			{ID: "noop", Label: "Do nothing", Verb: attention.VerbNoop},
		},
		DefaultAction: "clear",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	// No daemon: the daemon-only verb is annotated unavailable.
	cmd := attentionShowCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{id, "--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "unavailable from this CLI") {
		t.Errorf("expected unavailable annotation with no daemon:\n%s", out)
	}
	if !strings.Contains(out, "nightgauge serve") {
		t.Errorf("expected the fix command named in the annotation:\n%s", out)
	}

	// With a daemon reachable, no option is annotated.
	startTestDaemon(t, dir)
	cmd2 := attentionShowCmd()
	var buf2 bytes.Buffer
	cmd2.SetOut(&buf2)
	cmd2.SetArgs([]string{id, "--workdir", dir})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(buf2.String(), "unavailable from this CLI") {
		t.Errorf("no option should be annotated unavailable when a daemon is reachable:\n%s", buf2.String())
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
