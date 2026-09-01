package gates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/testexec"
)

// flutterBranchRepo builds a git repo whose `main` holds a Flutter fixture and
// whose checked-out branch adds one Dart suite — the shape of a real
// feature-validate run, so the gate reads a real `git diff main...HEAD` rather
// than a stubbed file list.
func flutterBranchRepo(t *testing.T, makeRecipe, suitePath, suiteBody string) string {
	t.Helper()
	repo := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	write := func(rel, body string) {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("pubspec.yaml", "name: fixture\n")
	write("Makefile", "test:\n\t"+makeRecipe+"\n")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-m", "base")

	gittest.Run(t, repo, "checkout", "-b", "feat/suite")
	write(suitePath, suiteBody)
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-m", "add suite")
	return repo
}

const excludedSuite = "integration_test/app_e2e/setup_flow_test.dart"

// TestFeatureValidateGate_BlocksUnexecutedExcludedSuite is the regression this
// epic exists to prevent, in the shape a downstream Flutter app shipped it:
// every quality gate green, and the one file the change added carrying a tag
// the repo's own test command excludes. This gate exited zero on that tree.
func TestFeatureValidateGate_BlocksUnexecutedExcludedSuite(t *testing.T) {
	repo := flutterBranchRepo(t,
		"flutter test --exclude-tags=app-e2e",
		excludedSuite,
		"@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")
	writeGateMetrics(t, repo, 314, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 314, repo)
	if gr.Passed {
		t.Fatalf("gate passed on a suite nothing ever executed; evidence=%v", gr.Evidence)
	}
	if gr.TerminalKind != TerminalKindValidationFailed {
		t.Errorf("terminal kind = %q, want %q", gr.TerminalKind, TerminalKindValidationFailed)
	}
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, excludedSuite) {
		t.Errorf("evidence does not name the file:\n%s", joined)
	}
	if !strings.Contains(joined, "--exclude-tags=app-e2e") {
		t.Errorf("evidence does not name the exclusion mechanism:\n%s", joined)
	}
	if !strings.Contains(joined, "flutter test --tags=app-e2e") {
		t.Errorf("evidence carries no runnable remediation:\n%s", joined)
	}
}

// TestFeatureValidateGate_ExecutionRecordUnblocks — the escape hatch is
// evidence that someone ran it, not a flag that turns the check off.
func TestFeatureValidateGate_ExecutionRecordUnblocks(t *testing.T) {
	repo := flutterBranchRepo(t,
		"flutter test --exclude-tags=app-e2e",
		excludedSuite,
		"@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")
	writeGateMetrics(t, repo, 314, []map[string]any{{"gate_name": "build", "result": "pass"}})

	if err := testexec.AppendRecord(repo, 314, testexec.Record{
		File:    excludedSuite,
		Outcome: testexec.OutcomePass,
		Command: "flutter test --tags=app-e2e " + excludedSuite,
	}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}

	gr := FeatureValidateGate{}.Verify(context.Background(), 314, repo)
	if !gr.Passed {
		t.Fatalf("gate blocked despite a passing execution record: %s %v", gr.Reason, gr.Evidence)
	}
	if !strings.Contains(strings.Join(gr.Evidence, "\n"), "execution record") {
		t.Errorf("passing case should say why it is satisfied: %v", gr.Evidence)
	}
}

// TestFeatureValidateGate_CommonCaseUnchanged — a repo whose test command
// excludes nothing must produce the identical result it produced before this
// check existed: same pass, same reason, and not one extra evidence line.
func TestFeatureValidateGate_CommonCaseUnchanged(t *testing.T) {
	repo := flutterBranchRepo(t,
		"flutter test test integration_test",
		"test/widget_test.dart",
		"void main() {}\n")
	writeGateMetrics(t, repo, 99, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 99, repo)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	for _, line := range gr.Evidence {
		if strings.Contains(line, "test-execution") || strings.Contains(line, "test command:") {
			t.Fatalf("the common case gained new output: %q", line)
		}
	}
}
