// Package layouttest gives tests a real git repository and the per-clone
// directories internal/layout resolves for it. Per-clone data lives under the
// git common dir (ADR-024 § 7), so a test fixture that writes pipeline state,
// plans, retros or logs needs a repository, not a bare temp directory.
package layouttest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Repo returns a fresh, empty git repository in a temp directory.
func Repo(t testing.TB) string {
	t.Helper()
	return gittest.InitRepo(t, t.TempDir(), "-q")
}

// Init makes dir a git repository when it is not the top of one already and
// returns dir. Use it for a fixture root a test already created.
func Init(t testing.TB, dir string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir
	}
	return gittest.InitRepo(t, dir, "-q")
}

// PipelineDir is layout.PipelineStateDir(root), failing the test on error.
// root is made a git repository first when it is not one.
func PipelineDir(t testing.TB, root string) string {
	t.Helper()
	return classDir(t, root, layout.ClassPipeline)
}

// PlansDir is layout.PlansDir(root); see PipelineDir.
func PlansDir(t testing.TB, root string) string {
	t.Helper()
	return classDir(t, root, layout.ClassPlans)
}

// RetrosDir is layout.RetrosDir(root); see PipelineDir.
func RetrosDir(t testing.TB, root string) string {
	t.Helper()
	return classDir(t, root, layout.ClassRetros)
}

// LogsDir is layout.CloneLogsDir(root); see PipelineDir.
func LogsDir(t testing.TB, root string) string {
	t.Helper()
	return classDir(t, root, layout.ClassLogs)
}

// MkPipelineDir is PipelineDir with the directory created.
func MkPipelineDir(t testing.TB, root string) string {
	t.Helper()
	return mk(t, PipelineDir(t, root))
}

// MkPlansDir is PlansDir with the directory created.
func MkPlansDir(t testing.TB, root string) string {
	t.Helper()
	return mk(t, PlansDir(t, root))
}

// MkRetrosDir is RetrosDir with the directory created.
func MkRetrosDir(t testing.TB, root string) string {
	t.Helper()
	return mk(t, RetrosDir(t, root))
}

// MkLogsDir is LogsDir with the directory created.
func MkLogsDir(t testing.TB, root string) string {
	t.Helper()
	return mk(t, LogsDir(t, root))
}

func classDir(t testing.TB, root, class string) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("layouttest: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			t.Fatalf("layouttest: %v", err)
		}
	}
	if _, err := layout.GitCommonDir(abs); err != nil {
		Init(t, abs)
	}
	dir, err := layout.ClassDir(abs, class)
	if err != nil {
		t.Fatalf("layouttest: resolve %s for %s: %v", class, abs, err)
	}
	return dir
}

func mk(t testing.TB, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("layouttest: %v", err)
	}
	return dir
}
