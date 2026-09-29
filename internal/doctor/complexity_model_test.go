package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestCheckComplexityModel_MissingIsBootstrappedNotAWarning is #2202: the
// model is gitignored learned state, so a fresh clone never has it; the
// deterministic baseline is installed on first use. Absence is info only —
// never a warning — and offers `outcome init`.
func TestCheckComplexityModel_MissingIsBootstrappedNotAWarning(t *testing.T) {
	root := t.TempDir()
	fs, _ := complexityModelFindings(root)
	if len(fs) != 1 || fs[0].Severity != SeverityInfo || fs[0].Code != codeComplexityModelMissing {
		t.Fatalf("missing model must be one info finding, got %s", findingsText(fs))
	}
	if !strings.Contains(fs[0].Cause, "bootstrapped automatically") {
		t.Fatalf("cause must explain the bootstrap, got %q", fs[0].Cause)
	}
	if r := fs[0].Remedies; len(r) != 1 || r[0].Verb != verbOutcomeInit || r[0].Kind != RemedyConfirm {
		t.Fatalf("remedy = %+v, want confirm outcome.init", r)
	}
}

func TestCheckComplexityModel_ExistingFileIsHealthy(t *testing.T) {
	root := t.TempDir()
	result, err := gh.NewOutcomeService(root).InitializeModel()
	if err != nil {
		t.Fatalf("initialize model: %v", err)
	}
	modelPath := result.Path

	fs, detail := complexityModelFindings(root)
	warning := findingsText(fs)
	if len(fs) != 0 {
		t.Fatalf("existing model check = %+v, warning = %q", fs, warning)
	}
	if detail != modelPath {
		t.Errorf("detail = %q, want %q", detail, modelPath)
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

	fs, _ := complexityModelFindings(root)
	warning := findingsText(fs)
	if len(fs) == 0 || !strings.Contains(warning, "is invalid") ||
		!strings.Contains(warning, "nightgauge outcome init") {
		t.Fatalf("invalid model check = %+v, warning = %q", fs, warning)
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

	fs, _ := complexityModelFindings(root)
	warning := findingsText(fs)
	if len(fs) != 0 {
		t.Fatalf("v0.3.x fixture check = %+v, warning = %q", fs, warning)
	}
}

func TestCheckComplexityModel_RejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".nightgauge")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	fs, _ := complexityModelFindings(root)
	warning := findingsText(fs)
	if len(fs) == 0 || !strings.Contains(warning, "directory is a symlink") {
		t.Fatalf("symlinked directory check = %+v, warning = %q", fs, warning)
	}
}

// TestComplexityModelRemedyPreview: the outcome-init remedy's preview names
// the file it would write, and producing it writes nothing.
func TestComplexityModelRemedyPreview(t *testing.T) {
	root := t.TempDir()
	modelPath := filepath.Join(root, ".nightgauge", "complexity-model.yaml")
	fs, _ := complexityModelFindings(root)
	if len(fs) != 1 || len(fs[0].Remedies) == 0 {
		t.Fatalf("want one finding with a remedy, got %s", findingsText(fs))
	}
	rem := fs[0].Remedies[0]
	if rem.Kind != RemedyConfirm || rem.Verb != verbOutcomeInit || !strings.Contains(rem.Preview, modelPath) {
		t.Errorf("remedy = %+v, want confirm outcome.init previewing %s", rem, modelPath)
	}
	if _, err := os.Stat(modelPath); !os.IsNotExist(err) {
		t.Errorf("the check wrote %s (stat err %v)", modelPath, err)
	}
}
