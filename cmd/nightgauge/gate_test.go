package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
)

// These mirror the AC #3 coverage from PLAN.md #134: `gate verify feature-dev
// <N> --json` must surface Files/FileCount on the dev_handoff_missing path so
// feature-validate's context-load.md can proceed against them, and must omit
// them on the genuinely-empty path. Testing gateVerifyJSON directly (rather
// than executing the cobra command, which os.Exit(2)s on gate failure) keeps
// this a normal in-process test.

func gitTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("checkout", "-b", "main")
	run("config", "user.email", "gate-test@example.com")
	run("config", "user.name", "Gate Test")
	run("config", "status.showUntrackedFiles", "normal")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	return dir
}

func TestGateVerifyFeatureDev_DirtyTreeNoContext_JSONReportsFiles(t *testing.T) {
	ws := gitTestRepo(t)
	if err := os.MkdirAll(filepath.Join(ws, "internal", "scan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "internal", "scan", "testcmd.go"), []byte("package scan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gate, ok := gates.LookupByStageName("feature-dev")
	if !ok {
		t.Fatal("no gate registered for feature-dev")
	}
	result := gate.Verify(context.Background(), 134, ws)

	if result.Passed {
		t.Fatalf("expected gate to fail (no dev context): %+v", result)
	}
	if result.TerminalKind != "dev_handoff_missing" {
		t.Fatalf("terminal_kind = %q, want dev_handoff_missing", result.TerminalKind)
	}

	payload := gateVerifyJSON{
		Stage:        "feature-dev",
		GateName:     result.GateName,
		Passed:       result.Passed,
		Reason:       result.Reason,
		Evidence:     result.Evidence,
		DurationMs:   result.DurationMs,
		Timestamp:    result.Timestamp,
		Kind:         string(result.Kind),
		TerminalKind: result.TerminalKind,
		Files:        result.Files,
		FileCount:    result.FileCount,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["terminal_kind"] != "dev_handoff_missing" {
		t.Fatalf("decoded terminal_kind = %v", decoded["terminal_kind"])
	}
	files, _ := decoded["files"].([]any)
	if len(files) != 1 || files[0] != "internal/scan/testcmd.go" {
		t.Fatalf("decoded files = %v, want [internal/scan/testcmd.go]", decoded["files"])
	}
	if decoded["file_count"] != float64(1) {
		t.Fatalf("decoded file_count = %v, want 1", decoded["file_count"])
	}
}

func TestGateVerifyFeatureDev_CleanTreeNoContext_JSONOmitsFiles(t *testing.T) {
	ws := gitTestRepo(t)

	gate, ok := gates.LookupByStageName("feature-dev")
	if !ok {
		t.Fatal("no gate registered for feature-dev")
	}
	result := gate.Verify(context.Background(), 134, ws)

	if result.Passed {
		t.Fatalf("expected gate to fail (no dev context, no git evidence): %+v", result)
	}
	if result.TerminalKind == "dev_handoff_missing" {
		t.Fatalf("expected no dev_handoff_missing terminal kind on a genuinely empty tree: %+v", result)
	}

	payload := gateVerifyJSON{
		Stage:        "feature-dev",
		GateName:     result.GateName,
		Passed:       result.Passed,
		Reason:       result.Reason,
		Kind:         string(result.Kind),
		TerminalKind: result.TerminalKind,
		Files:        result.Files,
		FileCount:    result.FileCount,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"files"`) {
		t.Fatalf("expected files omitted on genuinely-empty path: %s", data)
	}
	if strings.Contains(string(data), `"file_count"`) {
		t.Fatalf("expected file_count omitted on genuinely-empty path: %s", data)
	}
}
