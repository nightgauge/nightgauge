package gates

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func TestFeatureDevGate_Pass(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"foo.go"},
			"modified": []string{"bar.go"},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{
			"ran":    true,
			"status": "passed",
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

func TestFeatureDevGate_Fail_BuildFailed(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"foo.go"},
			"modified": []string{},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{
			"ran":    true,
			"status": "failed",
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when build_verification.status=failed")
	}
	if gr.TerminalKind != TerminalKindDevBuildVerificationFailed {
		t.Errorf("TerminalKind = %q, want %q (#1237)", gr.TerminalKind, TerminalKindDevBuildVerificationFailed)
	}
}

func TestFeatureDevGate_Fail_InvalidJSON(t *testing.T) {
	ws := t.TempDir()
	dir := layouttest.PipelineDir(t, ws)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev-42.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail on malformed JSON")
	}
	if gr.TerminalKind != TerminalKindValidationError {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindValidationError)
	}
}

// TestFeatureDevGate_SkillSaidSuccessButZeroFiles covers the canonical
// "skill reported success but didn't change anything" scenario.
func TestFeatureDevGate_SkillSaidSuccessButZeroFiles(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": []string{},
			"deleted":  []string{},
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when files_changed is empty")
	}
}

func TestFeatureDevGate_Fail_MissingBuildVerification(t *testing.T) {
	// The dev completion contract requires the verification step to be
	// recorded (#55) — a missing object means the skill skipped it entirely,
	// the gap the Claude-only Stop hook used to cover on one adapter.
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"foo.go"},
			"modified": []string{},
			"deleted":  []string{},
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when build_verification absent (#55); reason=%q", gr.Reason)
	}
}

func TestFeatureDevGate_Pass_BuildSkippedButRecorded(t *testing.T) {
	// status=skipped with the object present is legitimate: a repo with no
	// build system, or a fast-track docs-only change. Only a MISSING object
	// (verification never attempted) fails the contract.
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"README.md"},
			"modified": []string{},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{
			"ran":    false,
			"status": "skipped",
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when build_verification recorded as skipped; reason=%q", gr.Reason)
	}
}

func TestFeatureDevGate_Fail_FailingTests(t *testing.T) {
	// tests_status.failed > 0 trips the gate (#55) — ports the Stop hook's
	// "verify tests pass" check adapter-neutrally via the recorded evidence.
	ws := t.TempDir()
	writeJSON(t, filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json"), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"foo.go"},
			"modified": []string{},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{
			"ran":    true,
			"status": "passed",
		},
		"tests_status": map[string]any{
			"passed": 10,
			"failed": 2,
		},
	})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatal("expected fail when tests_status.failed > 0")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindFail)
	}
}

// writeFailingDevContext writes the #592 specimen: a dev-42.json whose
// tests_status records two failures, stamped createdAt and with mtime set.
func writeFailingDevContext(t *testing.T, ws, createdAt string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(layouttest.PipelineDir(t, ws), "dev-42.json")
	doc := map[string]any{
		"files_changed": map[string]any{
			"created": []string{"foo.go"}, "modified": []string{}, "deleted": []string{},
		},
		"build_verification": map[string]any{"ran": true, "status": "passed"},
		"tests_status":       map[string]any{"passed": 10, "failed": 2},
	}
	if createdAt != "" {
		doc["created_at"] = createdAt
	}
	writeJSON(t, path, doc)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFeatureDevGate_StaleContext_NotJudged is the flutter#592 regression
// (#2637): attempt 1 recorded failing tests, attempt 2 wrote nothing. The
// retry must fail as dev_context_stale, never on attempt 1's dev_tests_failed.
func TestFeatureDevGate_StaleContext_NotJudged(t *testing.T) {
	start := time.Now()
	old := start.Add(-time.Hour)
	for name, createdAt := range map[string]string{
		"created_at old":    old.UTC().Format(time.RFC3339),
		"created_at absent": "",
	} {
		t.Run(name, func(t *testing.T) {
			ws := t.TempDir()
			writeFailingDevContext(t, ws, createdAt, old)
			gr := FeatureDevGate{}.Verify(WithStageStart(context.Background(), start), 42, ws)
			if gr.Passed {
				t.Fatal("stale dev context passed the gate")
			}
			if gr.TerminalKind != TerminalKindDevContextStale {
				t.Fatalf("TerminalKind = %q, want %q (reason %q)", gr.TerminalKind, TerminalKindDevContextStale, gr.Reason)
			}
		})
	}
}

// A file this attempt rewrote carries a fresh mtime even with a copied
// placeholder created_at; it is judged on its contents.
func TestFeatureDevGate_FreshMtimeOldCreatedAt_Judged(t *testing.T) {
	start := time.Now()
	ws := t.TempDir()
	writeFailingDevContext(t, ws, "2026-01-01T00:00:00Z", start)
	gr := FeatureDevGate{}.Verify(WithStageStart(context.Background(), start), 42, ws)
	if gr.TerminalKind != TerminalKindDevTestsFailed {
		t.Fatalf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevTestsFailed)
	}
}

// Without a stage start (the `gate verify` CLI) the guard is skipped.
func TestFeatureDevGate_NoStageStart_Unchanged(t *testing.T) {
	ws := t.TempDir()
	writeFailingDevContext(t, ws, "", time.Now().Add(-time.Hour))
	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)
	if gr.TerminalKind != TerminalKindDevTestsFailed {
		t.Fatalf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevTestsFailed)
	}
}
