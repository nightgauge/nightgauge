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
