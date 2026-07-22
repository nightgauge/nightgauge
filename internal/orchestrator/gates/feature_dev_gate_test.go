package gates

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFeatureDevGate_Pass(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
}

// TestFeatureDevGate_SkillSaidSuccessButZeroFiles covers the canonical
// "skill reported success but didn't change anything" scenario.
func TestFeatureDevGate_SkillSaidSuccessButZeroFiles(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-42.json"), map[string]any{
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
