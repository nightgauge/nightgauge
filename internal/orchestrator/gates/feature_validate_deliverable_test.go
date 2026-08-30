package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// End-to-end coverage for #152: the validate gate must re-derive the verdict
// from the change's real git diff, not from the roll-up the skill wrote.
//
// These tests drive a real repository on purpose. The unit tests in
// internal/deliverable pin the classification; what they cannot show is that
// the gate resolves a base ref, gets a diff back, finds the artifact and
// rewrites it. Every one of those is a place the feature could be dead at
// runtime while every unit test stayed green — which is exactly how #169's
// four features shipped broken.

// initRepo builds a git repo with a `main` baseline and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()

	gittest.Run(t, ws, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", ".")
	gittest.Run(t, ws, "commit", "-m", "base")
	return ws
}

// commitOnBranch checks out a feature branch and commits the given files.
func commitOnBranch(t *testing.T, ws, branch string, files map[string]string) {
	t.Helper()

	gittest.Run(t, ws, "checkout", "-b", branch)
	for name, content := range files {
		full := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, ws, "add", ".")
	gittest.Run(t, ws, "commit", "-m", "work")
}

func writeValidateArtifact(t *testing.T, ws string, issue int, body map[string]any) {
	t.Helper()
	dir := filepath.Join(ws, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "validate-"+strconv.Itoa(issue)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readValidateArtifact(t *testing.T, ws string, issue int) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws, ".nightgauge", "pipeline", "validate-"+strconv.Itoa(issue)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// passingArtifact is what feature-validate wrote in the run that produced #152:
// a green roll-up, the pre-existing unit suite run, and both other tiers idle
// with an honest reason on the one that mattered.
func passingArtifact() map[string]any {
	return map[string]any{
		"schema_version":    "2.5",
		"issue_number":      152,
		"validation_status": "passed",
		"commit_sha":        "deadbeef",
		"unit_tests":        map[string]any{"ran": true, "passed": true, "tests_run": 1361},
		"integration_tests": map[string]any{"ran": false, "passed": false},
		"e2e_tests": map[string]any{
			"ran": false, "passed": false,
			"reason": "no E2E framework detected by deterministic binary (nightgauge e2e detect); suite unwired from CI per plan scope",
		},
	}
}

func TestFeatureValidateGate_SupersedesPassedWhenTheSuiteNeverRan(t *testing.T) {
	ws := initRepo(t)
	commitOnBranch(t, ws, "feat/152", map[string]string{
		"integration_test/signup_flow_test.dart": "void main() {}\n",
		"lib/signup.dart":                        "class Signup {}\n",
	})
	writeGateMetrics(t, ws, 152, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
	})
	writeValidateArtifact(t, ws, 152, passingArtifact())

	gr := FeatureValidateGate{}.Verify(context.Background(), 152, ws)

	// The gate still passes. An unexercised deliverable is not a stage failure
	// — turning it into one would block legitimate work and get the check
	// routed around.
	if !gr.Passed {
		t.Fatalf("gate must still pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}

	doc := readValidateArtifact(t, ws, 152)
	if got := doc["validation_status"]; got != "passed_unverified" {
		t.Fatalf("validation_status = %v, want passed_unverified", got)
	}
	block, ok := doc["unverified_deliverable"].(map[string]any)
	if !ok {
		t.Fatal("evidence block was not written")
	}
	files, _ := block["files"].([]any)
	if len(files) != 1 || files[0] != "integration_test/signup_flow_test.dart" {
		t.Fatalf("files = %v", files)
	}
	// Fields the rest of the artifact depends on must survive the rewrite —
	// the gate edits a document it does not fully model.
	if doc["commit_sha"] != "deadbeef" {
		t.Fatalf("commit_sha lost in rewrite: %v", doc["commit_sha"])
	}

	// The gate surfaces it in its own evidence too, so a run log shows the
	// reason without anyone opening the artifact.
	if !strings.Contains(strings.Join(gr.Evidence, " "), "never executed") {
		t.Fatalf("gate evidence should name the gap: %v", gr.Evidence)
	}
}

func TestFeatureValidateGate_LeavesACleanRunAlone(t *testing.T) {
	ws := initRepo(t)
	commitOnBranch(t, ws, "feat/152", map[string]string{
		"internal/auth/token.go":      "package auth\n",
		"internal/auth/token_test.go": "package auth\n",
	})
	writeGateMetrics(t, ws, 152, []map[string]any{{"gate_name": "build", "result": "pass"}})

	art := passingArtifact()
	art["unit_tests"] = map[string]any{"ran": true, "passed": true, "tests_run": 12}
	writeValidateArtifact(t, ws, 152, art)

	gr := FeatureValidateGate{}.Verify(context.Background(), 152, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}

	doc := readValidateArtifact(t, ws, 152)
	if got := doc["validation_status"]; got != "passed" {
		t.Fatalf("a run whose unit tests ran must stay passed, got %v", got)
	}
	if _, present := doc["unverified_deliverable"]; present {
		t.Fatal("no evidence block should be written for a clean run")
	}
}

// A repo the gate cannot diff yields no evidence, and no evidence must not read
// as an accusation. This is the fail-safe direction: the check exists to stop a
// run overclaiming, so it must never itself overclaim.
func TestFeatureValidateGate_SilentWhenTheDiffIsUncomputable(t *testing.T) {
	ws := t.TempDir() // no git repo at all
	writeGateMetrics(t, ws, 152, []map[string]any{{"gate_name": "build", "result": "pass"}})
	writeValidateArtifact(t, ws, 152, passingArtifact())

	gr := FeatureValidateGate{}.Verify(context.Background(), 152, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	if got := readValidateArtifact(t, ws, 152)["validation_status"]; got != "passed" {
		t.Fatalf("verdict must stand when no diff is available, got %v", got)
	}
}

// A genuine quality-gate failure outranks an unexercised deliverable: the run
// keeps its louder signal and the artifact is left untouched.
func TestFeatureValidateGate_QualityFailureTakesPrecedence(t *testing.T) {
	ws := initRepo(t)
	commitOnBranch(t, ws, "feat/152", map[string]string{
		"integration_test/signup_flow_test.dart": "void main() {}\n",
	})
	writeGateMetrics(t, ws, 152, []map[string]any{
		{"gate_name": "build", "result": "catch", "error_summary": "compile error"},
	})
	art := passingArtifact()
	art["validation_status"] = "failed"
	writeValidateArtifact(t, ws, 152, art)

	gr := FeatureValidateGate{}.Verify(context.Background(), 152, ws)
	if gr.Passed {
		t.Fatal("a caught quality gate must still fail the stage")
	}
	if got := readValidateArtifact(t, ws, 152)["validation_status"]; got != "failed" {
		t.Fatalf("failure verdict must be preserved, got %v", got)
	}
}
