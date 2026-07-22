package disciplineScore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompute(t *testing.T) {
	cases := []struct {
		name          string
		in            DisciplineInput
		wantScore     int
		wantReadiness string
	}{
		{"empty repo → unready", DisciplineInput{}, 0, "unready"},
		{"tests only → 30 thin? no, unready", DisciplineInput{HasTestFiles: true}, 30, "unready"},
		{"tests + test cmd → 50 thin", DisciplineInput{HasTestFiles: true, TestCommandConfigured: true}, 50, "thin"},
		{"tests + cmd + CI → 80 ready", DisciplineInput{HasTestFiles: true, TestCommandConfigured: true, CIWorkflowCount: 3}, 80, "ready"},
		{"the full house → 100 ready", DisciplineInput{HasTestFiles: true, TestCommandConfigured: true, CIWorkflowCount: 5, HasProcessDocs: true, HasIssueTemplates: true}, 100, "ready"},
		{"CI only → 30 unready", DisciplineInput{CIWorkflowCount: 1}, 30, "unready"},
		{"docs only → 20 unready", DisciplineInput{HasProcessDocs: true, HasIssueTemplates: true}, 20, "unready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Compute(c.in)
			if got.Score != c.wantScore {
				t.Errorf("Score = %d, want %d (breakdown %v)", got.Score, c.wantScore, got.Breakdown)
			}
			if got.Readiness != c.wantReadiness {
				t.Errorf("Readiness = %q, want %q", got.Readiness, c.wantReadiness)
			}
		})
	}
}

func TestGatherSignals(t *testing.T) {
	root := t.TempDir()
	// A reasonably-disciplined repo.
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n")
	mustWrite(t, filepath.Join(root, "internal", "x_test.go"), "package x\n")
	mustWrite(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "name: CI\n")
	mustWrite(t, filepath.Join(root, "CONTRIBUTING.md"), "# Contributing\n")
	// node_modules test file must be skipped.
	mustWrite(t, filepath.Join(root, "node_modules", "dep", "a.test.ts"), "x")

	got := GatherSignals(root)
	if !got.HasTestFiles {
		t.Error("should find internal/x_test.go")
	}
	if !got.TestCommandConfigured {
		t.Error("go.mod implies a test command")
	}
	if got.CIWorkflowCount != 1 {
		t.Errorf("CIWorkflowCount = %d, want 1", got.CIWorkflowCount)
	}
	if !got.HasProcessDocs {
		t.Error("CONTRIBUTING.md should count")
	}

	// An empty repo scores low.
	empty := GatherSignals(t.TempDir())
	if Compute(empty).Readiness != "unready" {
		t.Errorf("empty repo should be unready, got %+v", Compute(empty))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
