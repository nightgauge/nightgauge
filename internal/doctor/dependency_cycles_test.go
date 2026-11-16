package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// #3081: doctor reports each dependency cycle the scheduler recorded.
func TestDependencyCycleFindings(t *testing.T) {
	root := layouttest.Repo(t)
	if fs, _ := dependencyCycleFindings(root); len(fs) != 0 {
		t.Fatalf("no record: %d findings, want 0", len(fs))
	}
	dir := layouttest.PipelineDir(t, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"observed_at":"2026-11-16T00:00:00Z","cycles":[{"issues":["o/a#1","o/a#2","o/a#3"],` +
		`"edges":[{"blocked":"o/a#1","blocker":"o/a#2"},{"blocked":"o/a#2","blocker":"o/a#3"},{"blocked":"o/a#3","blocker":"o/a#1"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, DependencyCyclesFile), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _ := dependencyCycleFindings(root)
	if len(fs) != 1 || fs[0].Code != dependencyCyclesCode || fs[0].Severity != SeverityWarning {
		t.Fatalf("findings = %+v, want one NGD114 warning", fs)
	}
	if err := os.WriteFile(filepath.Join(dir, DependencyCyclesFile), []byte(`{"observed_at":"x","cycles":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if fs, _ := dependencyCycleFindings(root); len(fs) != 0 {
		t.Fatalf("empty record: %d findings, want 0", len(fs))
	}
}
