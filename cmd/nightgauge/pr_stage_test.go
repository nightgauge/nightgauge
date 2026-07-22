package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
