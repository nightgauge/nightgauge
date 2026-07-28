package deliverable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// reportedArtifact is the validate context from the run in #152, trimmed to the
// fields this package reads. The shape matters: fixtures invented from the
// happy path are what let a gap like this survive, so the numbers here are the
// reported ones — 1,361 pre-existing unit tests green, both other tiers idle,
// and a truthful reason on the tier that mattered.
func reportedArtifact() map[string]any {
	return map[string]any{
		"schema_version":    "2.4",
		"issue_number":      float64(4318),
		"validation_status": "passed",
		"build":             map[string]any{"ran": true, "passed": true},
		"unit_tests": map[string]any{
			"ran": true, "passed": true, "tests_run": float64(1361),
		},
		"integration_tests": map[string]any{
			"ran": false, "passed": false, "tests_run": float64(0),
		},
		"e2e_tests": map[string]any{
			"ran": false, "passed": false,
			"reason": "no E2E framework detected by deterministic binary (nightgauge e2e detect); suite unwired from CI per plan scope",
		},
		"notes": "The new suite needs a cross-repo Docker stack, a booted emulator and Mailpit.",
	}
}

func reportedChangedFiles() []string {
	return []string{
		"integration_test/signup_flow_test.dart",
		"lib/src/auth/signup_controller.dart",
	}
}

func TestDerive_CatchesTheReportedRun(t *testing.T) {
	f := Derive(reportedArtifact(), reportedChangedFiles())

	if !f.Detected() {
		t.Fatal("the run that produced #152 must be detected")
	}
	if got := f.Paths(); !reflect.DeepEqual(got, []string{"integration_test/signup_flow_test.dart"}) {
		t.Fatalf("paths = %v", got)
	}
	if f.SupersededStatus != "passed" {
		t.Fatalf("SupersededStatus = %q, want passed", f.SupersededStatus)
	}
	// The stage's own explanation must ride along — it is the most useful
	// sentence in the whole artifact and it is what nobody ever read.
	if r := f.TierReasons[TierE2E]; r == "" {
		t.Fatal("the idle tier's reason must be carried into the finding")
	}
}

func TestApply_SupersedesPassedAndRecordsEvidence(t *testing.T) {
	doc := reportedArtifact()
	f := Derive(doc, reportedChangedFiles())

	if !Apply(doc, f) {
		t.Fatal("Apply returned false for a detected finding on a passed run")
	}
	if got := doc["validation_status"]; got != StatusPassedUnverified {
		t.Fatalf("validation_status = %v, want %s", got, StatusPassedUnverified)
	}

	block, ok := doc["unverified_deliverable"].(map[string]any)
	if !ok {
		t.Fatal("unverified_deliverable block missing")
	}
	if detected, _ := block["detected"].(bool); !detected {
		t.Fatal("block must record detected=true")
	}
	if got := block["superseded_status"]; got != "passed" {
		t.Fatalf("superseded_status = %v — a retro must see the divergence, not just the correction", got)
	}
	if got := block["tiers"]; !reflect.DeepEqual(got, []string{"integration", "e2e"}) {
		t.Fatalf("tiers = %v", got)
	}
}

// An unexercised deliverable is strictly less urgent than a gate that actually
// caught something. Overwriting a failure would trade a loud signal for a
// quiet one.
func TestApply_NeverOverwritesAFailure(t *testing.T) {
	for _, status := range []string{"failed", "partial", "skipped"} {
		t.Run(status, func(t *testing.T) {
			doc := reportedArtifact()
			doc["validation_status"] = status
			f := Derive(doc, reportedChangedFiles())

			if Apply(doc, f) {
				t.Fatalf("Apply must not supersede %q", status)
			}
			if doc["validation_status"] != status {
				t.Fatalf("verdict mutated to %v", doc["validation_status"])
			}
			if _, present := doc["unverified_deliverable"]; present {
				t.Fatal("no block should be written when the verdict stands")
			}
		})
	}
}

func TestApply_NoOpWhenEverythingRan(t *testing.T) {
	doc := reportedArtifact()
	doc["e2e_tests"] = map[string]any{"ran": true, "passed": true}
	f := Derive(doc, reportedChangedFiles())

	if f.Detected() {
		t.Fatal("nothing to report once the owning tier ran")
	}
	if Apply(doc, f) {
		t.Fatal("Apply must be a no-op")
	}
	if doc["validation_status"] != "passed" {
		t.Fatalf("clean run must stay passed, got %v", doc["validation_status"])
	}
}

// Apply mutates a generic document precisely because a typed round-trip would
// drop everything this package does not model — which is nearly the whole
// artifact, and which downstream stages read.
func TestWriteValidateContext_PreservesUnknownFields(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".nightgauge", "pipeline"), 0o755); err != nil {
		t.Fatal(err)
	}

	original := reportedArtifact()
	original["gate_metrics"] = []any{map[string]any{"gate_name": "lint", "result": "pass"}}
	original["commit_sha"] = "abc123"
	raw, _ := json.MarshalIndent(original, "", "  ")
	path := filepath.Join(ws, ".nightgauge", "pipeline", "validate-4318.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := ReadValidateContext(ws, 4318)
	if err != nil {
		t.Fatal(err)
	}
	if !Apply(doc, Derive(doc, reportedChangedFiles())) {
		t.Fatal("expected the finding to apply")
	}
	if err := WriteValidateContext(ws, 4318, doc); err != nil {
		t.Fatal(err)
	}

	reread, err := ReadValidateContext(ws, 4318)
	if err != nil {
		t.Fatal(err)
	}
	if reread["commit_sha"] != "abc123" {
		t.Fatalf("commit_sha lost: %v", reread["commit_sha"])
	}
	if reread["gate_metrics"] == nil {
		t.Fatal("gate_metrics lost — downstream stages read this")
	}
	if reread["notes"] == nil {
		t.Fatal("notes lost")
	}
	if reread["validation_status"] != StatusPassedUnverified {
		t.Fatalf("verdict not persisted: %v", reread["validation_status"])
	}
}

func TestFindingFromArtifact_RoundTrips(t *testing.T) {
	doc := reportedArtifact()
	derived := Derive(doc, reportedChangedFiles())
	Apply(doc, derived)

	// pr-create and the attention sweep read the artifact after the gate
	// corrected it; they cannot re-derive, because re-deriving needs a git diff
	// they may be too late to obtain.
	reread := FindingFromArtifact(doc)

	if !reread.Detected() {
		t.Fatal("round-trip lost the finding")
	}
	if !reflect.DeepEqual(reread.Paths(), derived.Paths()) {
		t.Fatalf("paths %v != %v", reread.Paths(), derived.Paths())
	}
	if !reflect.DeepEqual(reread.Tiers, derived.Tiers) {
		t.Fatalf("tiers %v != %v — canonical order must survive", reread.Tiers, derived.Tiers)
	}
	if reread.Summary() != derived.Summary() {
		t.Fatalf("summary drifted:\n pre: %s\npost: %s", derived.Summary(), reread.Summary())
	}
}

func TestFindingFromArtifact_AbsentBlock(t *testing.T) {
	if FindingFromArtifact(reportedArtifact()).Detected() {
		t.Fatal("a clean artifact must not yield a finding")
	}
	if FindingFromArtifact(map[string]any{}).Detected() {
		t.Fatal("an empty document must not yield a finding")
	}
}

func TestSummary_NamesTheGap(t *testing.T) {
	f := Derive(reportedArtifact(), reportedChangedFiles())
	want := "1 test file added but never executed (integration/e2e tier did not run)"
	if got := f.Summary(); got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}
