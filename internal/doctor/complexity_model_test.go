package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func TestCheckComplexityModel_MissingUsesSupportedInitializer(t *testing.T) {
	root := t.TempDir()
	check, warning := checkComplexityModel(root)
	if check.OK {
		t.Fatal("missing complexity model reported healthy")
	}
	if !strings.Contains(check.Error, "nightgauge outcome init") || warning != check.Error {
		t.Fatalf("missing-model remediation = %q, warning = %q", check.Error, warning)
	}
	if strings.Contains(check.Error, "size calibrate") {
		t.Fatalf("remediation references nonexistent command: %q", check.Error)
	}
}

func TestCheckComplexityModel_ExistingFileIsHealthy(t *testing.T) {
	root := t.TempDir()
	result, err := gh.NewOutcomeService(root).InitializeModel()
	if err != nil {
		t.Fatalf("initialize model: %v", err)
	}
	modelPath := result.Path

	check, warning := checkComplexityModel(root)
	if !check.OK || warning != "" {
		t.Fatalf("existing model check = %+v, warning = %q", check, warning)
	}
	if check.Detail != modelPath {
		t.Errorf("detail = %q, want %q", check.Detail, modelPath)
	}
}

func TestCheckComplexityModel_InvalidFileUsesSupportedRepairGuidance(t *testing.T) {
	root := t.TempDir()
	modelPath := filepath.Join(root, ".nightgauge", "complexity-model.yaml")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("schema_version: [truncated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	check, warning := checkComplexityModel(root)
	if check.OK || warning != check.Error || !strings.Contains(warning, "is invalid") ||
		!strings.Contains(warning, "nightgauge outcome init") {
		t.Fatalf("invalid model check = %+v, warning = %q", check, warning)
	}
}

// TestCheckComplexityModel_V03xFixtureIsHealthy reproduces #1911/#1918: a
// genuine v0.3.x model file — no lines_changed_thresholds (#1592/v0.4.0), no
// learnings, no critical_files — must be reported healthy. Every absent
// additive section is backfilled from bootstrap defaults on decode in one
// pass, not treated as a validation failure.
func TestCheckComplexityModel_V03xFixtureIsHealthy(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "complexity-model-v0.3.x.yaml"))
	if err != nil {
		t.Fatalf("read v0.3.x fixture: %v", err)
	}

	root := t.TempDir()
	modelPath := filepath.Join(root, ".nightgauge", "complexity-model.yaml")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	check, warning := checkComplexityModel(root)
	if !check.OK || warning != "" {
		t.Fatalf("v0.3.x fixture check = %+v, warning = %q", check, warning)
	}
}

func TestCheckComplexityModel_RejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".nightgauge")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	check, warning := checkComplexityModel(root)
	if check.OK || !strings.Contains(warning, "directory is a symlink") {
		t.Fatalf("symlinked directory check = %+v, warning = %q", check, warning)
	}
}
