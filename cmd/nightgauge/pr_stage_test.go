package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// runPrStageCapture executes a pr-stage subcommand with args, capturing stdout.
func runPrStageCapture(t *testing.T, args []string) (prStageResultJSON, error) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	cmd := prStageCmd()
	cmd.SetArgs(args)
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)

	var res prStageResultJSON
	if runErr == nil {
		if jsonErr := json.Unmarshal(out, &res); jsonErr != nil {
			t.Fatalf("stdout is not JSON: %v\nstdout: %q", jsonErr, string(out))
		}
	}
	return res, runErr
}

// runPrStageCaptureStderr executes a pr-stage subcommand and returns what it
// wrote to STDERR — the live phase-transition channel (#1397).
//
// Separate from runPrStageCapture because the two streams answer different
// questions: stdout carries the one JSON result, stderr carries the
// transitions as they happen. Cobra writes to cmd.ErrOrStderr(), so this
// redirects it on the command rather than swapping os.Stderr.
func runPrStageCaptureStderr(t *testing.T, args []string) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	var stderr bytes.Buffer
	cmd := prStageCmd()
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	_ = cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	_, _ = io.ReadAll(r)
	return stderr.String()
}

func writePrStageConfig(t *testing.T, dir string) {
	t.Helper()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("owner: testorg\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestPrStageMerge_NoPRContextPunts locks the deterministic pr-merge punt path:
// with no pr-{N}.json in the worktree the runner punts `no-pr-context-file`,
// emits valid JSON, exits 0, and does NOT flag rate_limited (so the TS caller
// falls through to the LLM, never defers). This is the exact contract the TS
// deterministic-first shim depends on for the punt→LLM-fallthrough case.
func TestPrStageMerge_NoPRContextPunts(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	res, err := runPrStageCapture(t, []string{"merge", "300", "--workdir", dir, "--json"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Stage != "pr-merge" {
		t.Errorf("stage = %q, want pr-merge", res.Stage)
	}
	if res.Path != "punt" {
		t.Errorf("path = %q, want punt", res.Path)
	}
	if !strings.Contains(res.Reason, "no-pr-context-file") {
		t.Errorf("reason = %q, want no-pr-context-file", res.Reason)
	}
	if res.RateLimited {
		t.Errorf("rate_limited = true on a missing-context punt, want false")
	}
}

// TestPrStageCreate_MissingDevContextPunts locks the deterministic pr-create
// punt path: DecideCreate punts `missing-dev-context` before any GitHub call,
// so with no dev-{N}.json the verb emits punt JSON and exits 0.
func TestPrStageCreate_MissingDevContextPunts(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	res, err := runPrStageCapture(t, []string{"create", "300", "--repo", "testorg/testrepo", "--workdir", dir, "--json"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Stage != "pr-create" {
		t.Errorf("stage = %q, want pr-create", res.Stage)
	}
	if res.Path != "punt" {
		t.Errorf("path = %q, want punt", res.Path)
	}
	if !strings.Contains(res.Reason, "missing-dev-context") {
		t.Errorf("reason = %q, want missing-dev-context", res.Reason)
	}
	if res.RateLimited {
		t.Errorf("rate_limited = true on a missing-context punt, want false")
	}
}

// TestPrStageCreate_RequiresRepo confirms the verb hard-errors (exit 1 semantics)
// when --repo is omitted — the caller then falls through to the LLM path.
func TestPrStageCreate_RequiresRepo(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	_, err := runPrStageCapture(t, []string{"create", "300", "--workdir", dir, "--json"})
	if err == nil {
		t.Fatalf("expected an error when --repo is omitted, got nil")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Errorf("error = %v, want a --repo-required message", err)
	}
}

// TestPrStageMerge_EmitsPhases is #1397 AC1: the CLI's JSON carries the phases
// the deterministic runner reported, in order, with terminal state.
//
// Before this, HeadlessOrchestrator shelled out to this verb in a separate
// process and nothing attached a PhaseReporter, so the extension's route to the
// deterministic pr-merge showed 0/14 for the whole stage while the Go
// scheduler's route to the SAME runner showed real progress — the dual-path
// drift class, on the route an ordinary VS Code user takes.
//
// Hermetic: with no pr-{N}.json the runner punts at read-pr-context before any
// GitHub call.
func TestPrStageMerge_EmitsPhases(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	res, err := runPrStageCapture(t, []string{"merge", "300", "--workdir", dir, "--json"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Phases) == 0 {
		t.Fatal("pr-stage merge --json emitted no `phases` — the extension's route to the " +
			"deterministic runner cannot show progress, so it stays at 0/14 (#1397)")
	}

	// The starts are on the wire, not just terminal states: a consumer with only
	// terminal states can rebuild the durable record but not the live count.
	var sawRunning, sawTerminal bool
	for _, p := range res.Phases {
		if p.Stage != "pr-merge" {
			t.Errorf("phase %+v is attributed to the wrong stage", p)
		}
		if p.Total <= 0 {
			t.Errorf("phase %+v carries no registry total — consumers index on it", p)
		}
		switch p.Status {
		case "running":
			sawRunning = true
		case "complete", "failed", "skipped":
			sawTerminal = true
		default:
			t.Errorf("phase %+v has an unknown status", p)
		}
	}
	if !sawRunning {
		t.Error("no `running` transition — the live phase count cannot advance")
	}
	if !sawTerminal {
		t.Error("no terminal transition — the durable record would be left `running`")
	}

	// AC4: a punt writes no skips, and the phase the runner was inside is
	// recorded failed.
	for _, p := range res.Phases {
		if p.Status == "skipped" {
			t.Errorf("punt emitted a skip for %q — a punt hands the stage to the skill", p.Name)
		}
	}
	last := res.Phases[len(res.Phases)-1]
	if last.Name != "read-pr-context" || last.Status != "failed" {
		t.Errorf("last transition = %+v, want read-pr-context failed (the punt point)", last)
	}
}

// TestPrStageCreate_EmitsPhases is the pr-create half of AC1 — the two verbs
// are wired separately, so one being instrumented says nothing about the other.
func TestPrStageCreate_EmitsPhases(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	res, err := runPrStageCapture(t, []string{
		"create", "300", "--workdir", dir, "--repo", "testorg/testrepo", "--json",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Phases) == 0 {
		t.Fatal("pr-stage create --json emitted no `phases` (#1397)")
	}
	for _, p := range res.Phases {
		if p.Stage != "pr-create" {
			t.Errorf("phase %+v is attributed to the wrong stage", p)
		}
	}
}

// TestPrStageMerge_StreamsPhasesLive is the half of #1397 the `phases` array
// cannot provide, and it is the half the issue was actually about.
//
// The deterministic pr-merge waits out in-flight CI on a 30s x 30 budget. A
// consumer that learns the phases only when the process EXITS therefore sits at
// 0/14 for up to fifteen minutes and then jumps to the end — which is the
// reported symptom, not a fix for it. So each transition must be on the wire
// when it is reported.
//
// Asserted end-to-end through the real command, because the recorder being
// capable of streaming says nothing about whether the CLI wired it: that
// wiring is one `if outputJSON` in newPrStagePhaseRecorder, and nothing else
// would fail if it were dropped.
func TestPrStageMerge_StreamsPhasesLive(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	stderr := runPrStageCaptureStderr(t, []string{"merge", "300", "--workdir", dir, "--json"})

	var streamed []orchestrator.PhaseTransition
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, orchestrator.PhaseStreamPrefix) {
			continue
		}
		var tr orchestrator.PhaseTransition
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, orchestrator.PhaseStreamPrefix)), &tr); err != nil {
			t.Fatalf("streamed line is not a transition: %v (%q)", err, line)
		}
		streamed = append(streamed, tr)
	}

	if len(streamed) == 0 {
		t.Fatalf("pr-stage merge --json streamed no phase transitions on stderr — the caller's "+
			"count cannot advance until the process exits, which is the defect (#1397).\nstderr was:\n%s", stderr)
	}
	for _, tr := range streamed {
		if tr.Stage != "pr-merge" {
			t.Errorf("streamed transition %+v is attributed to the wrong stage", tr)
		}
		if tr.Total <= 0 {
			t.Errorf("streamed transition %+v carries no registry total — consumers index on it", tr)
		}
	}
}

// TestPrStageMerge_DoesNotStreamWithoutJSON: a human at the terminal has no
// consumer for sentinel lines, so the recorder only accumulates.
func TestPrStageMerge_DoesNotStreamWithoutJSON(t *testing.T) {
	dir := t.TempDir()
	writePrStageConfig(t, dir)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "dummy-token-for-hermetic-punt")

	stderr := runPrStageCaptureStderr(t, []string{"merge", "300", "--workdir", dir})

	if strings.Contains(stderr, orchestrator.PhaseStreamPrefix) {
		t.Errorf("sentinel phase lines leaked into human output:\n%s", stderr)
	}
}
