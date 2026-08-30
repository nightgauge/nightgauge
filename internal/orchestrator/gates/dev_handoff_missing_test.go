package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/deliverable"
)

// #223. These are the inverse of the #202 tests in dev_ground_truth_test.go.
// There, the dev context CLAIMED work git could not find. Here the context
// claims nothing and git finds the work sitting in the workspace.
//
// #202 wired inspectDevWork as an additional failure condition only, so it was
// unreachable on the empty-context paths — git could convict a lying context
// but never exonerate a missing one. #221 paid for that: feature-dev wrote 206
// insertions across 7 files plus a new package, ended its turn without writing
// its handoff, and the gate told the operator "dev context records zero file
// changes" over a worktree full of them.
//
// Like the #202 tests, these drive REAL git rather than stubbing it: the whole
// bug was a check that looked right and never asked the filesystem.

// emptyDevContext writes the shape a stage produces when it reports success
// having recorded nothing — the `fileTouches == 0` path.
func emptyDevContext(t *testing.T, ws string, issue int) {
	t.Helper()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(issue)), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": []string{},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{"ran": true, "status": "passed"},
	})
}

// TestFeatureDevGate_EmptyContext_DirtyTree_IsDerived is the #221/#341
// reproduction, now asserting the #1076 contract: an empty files_changed plus a
// dirty worktree is neither a no-op NOR a terminal failure. The handoff is
// written from git and the stage proceeds.
//
// #223 made this case legible; it stayed fatal for another 850 issues because
// the detector was never wired to a producer. The assertion that matters is
// that the work survives INTO THE DELIVERABLE — a verdict naming the files was
// never the same thing as a later stage being able to read them.
func TestFeatureDevGate_EmptyContext_DirtyTree_IsDerived(t *testing.T) {
	ws := gitRepo(t)
	emptyDevContext(t, ws, 221)
	writeFile(t, filepath.Join(ws, "internal", "scan", "testcmd.go"), "package scan\n")
	writeFile(t, filepath.Join(ws, "cmd", "nightgauge", "main.go"), "package main\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if !gr.Passed {
		t.Fatalf("expected pass: the work is on disk and the handoff is derivable; reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
	if gr.TerminalKind != "" {
		t.Errorf("TerminalKind = %q, want empty — a derived handoff is not a terminal failure", gr.TerminalKind)
	}

	doc := readDevContext(t, ws, 221)
	if got := doc["handoff_source"]; got != HandoffSourceDerived {
		t.Errorf("handoff_source = %v, want %q", got, HandoffSourceDerived)
	}
	files := devContextPaths(t, doc)
	for _, want := range []string{"internal/scan/testcmd.go", "cmd/nightgauge/main.go"} {
		if !slices.Contains(files, want) {
			t.Errorf("derived files_changed lacks %q; got %v", want, files)
		}
	}
	// The whole point: a LATER stage reads this file. Provenance must be
	// visible there, not only in a gate verdict nobody re-reads.
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "handoff_source=derived") {
		t.Errorf("run record does not mark the pass as derived:\n%s", joined)
	}
}

// TestFeatureDevGate_DerivationDisabled_ReproducesHandoffMissing is the AC6
// mutation check, and it is the reason the rest of these tests mean anything.
//
// Turning the derivation off must reproduce the ORIGINAL terminal verdict
// exactly — same Kind, same TerminalKind, same stable marker. A test suite that
// cannot make the new behaviour disappear cannot prove the new behaviour is
// what is passing it: without this, every assertion above would pass just as
// happily against a gate that had been made unconditionally permissive.
func TestFeatureDevGate_DerivationDisabled_ReproducesHandoffMissing(t *testing.T) {
	defer func(prev bool) { deriveDevHandoffEnabled = prev }(deriveDevHandoffEnabled)
	deriveDevHandoffEnabled = false

	ws := gitRepo(t)
	emptyDevContext(t, ws, 221)
	writeFile(t, filepath.Join(ws, "internal", "scan", "testcmd.go"), "package scan\n")
	writeFile(t, filepath.Join(ws, "cmd", "nightgauge", "main.go"), "package main\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Passed {
		t.Fatal("derivation disabled but the gate still passed — the pass is not coming from the derivation")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindFail)
	}
	if gr.TerminalKind != TerminalKindDevHandoffMissing {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevHandoffMissing)
	}
	if !strings.Contains(gr.Reason, "[dev-handoff-missing]") {
		t.Errorf("reason lacks the stable classifier marker: %q", gr.Reason)
	}
	if !strings.Contains(gr.Reason, "git finds 2 changed file(s)") {
		t.Errorf("reason does not name what git actually found: %q", gr.Reason)
	}
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "internal/scan/testcmd.go") {
		t.Errorf("evidence does not name the changed files:\n%s", joined)
	}
	if !strings.Contains(joined, "must be preserved") {
		t.Errorf("evidence drops the preservation instruction:\n%s", joined)
	}
}

// TestFeatureDevGate_MissingContext_DirtyTree_IsHandoffMissing covers the other
// early return — the context file absent entirely rather than empty. Same
// question, same answer; #221 hit the empty variant but a stage killed slightly
// earlier produces this one.
func TestFeatureDevGate_MissingContext_DirtyTree_IsDerived(t *testing.T) {
	ws := gitRepo(t)
	// deliberately no dev context written
	writeFile(t, filepath.Join(ws, "src", "real_work.go"), "package src\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if !gr.Passed {
		t.Fatalf("expected pass: a handoff that never existed is derivable from git; reason=%q", gr.Reason)
	}
	doc := readDevContext(t, ws, 221)
	if got := doc["handoff_source"]; got != HandoffSourceDerived {
		t.Errorf("handoff_source = %v, want %q", got, HandoffSourceDerived)
	}
	if files := devContextPaths(t, doc); !slices.Contains(files, "src/real_work.go") {
		t.Errorf("derived files_changed = %v, want it to name src/real_work.go", files)
	}
	// No stage-authored document existed, so the narrative is genuinely gone.
	// That loss must be recorded rather than implied by absence.
	derivation, _ := doc["handoff_derivation"].(map[string]any)
	if derivation["narrative_preserved"] != false {
		t.Errorf("narrative_preserved = %v, want false — nothing was authored to preserve", derivation["narrative_preserved"])
	}
	if derivation["reason"] != "dev context file missing" {
		t.Errorf("derivation reason = %v, want the original condition", derivation["reason"])
	}
}

// TestFeatureDevGate_EmptyContext_CommittedWork_IsHandoffMissing covers a stage
// that committed its work (against the #1608 contract) and still failed to
// write the handoff. The commits are real output and must be recognized.
func TestFeatureDevGate_EmptyContext_CommittedWork_IsDerived(t *testing.T) {
	ws := gitRepo(t)
	git(t, ws, "checkout", "-b", "feat/221-thing")
	writeFile(t, filepath.Join(ws, "src", "committed.go"), "package src\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "work")
	emptyDevContext(t, ws, 221)
	// The context file itself is bookkeeping and is excluded, so the only
	// deliverable git sees is the commit.
	git(t, ws, "add", "-A")
	git(t, ws, "commit", "-m", "bookkeeping")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if !gr.Passed {
		t.Fatalf("committed work is still work and still derivable; reason=%q", gr.Reason)
	}
	doc := readDevContext(t, ws, 221)
	if got := doc["handoff_source"]; got != HandoffSourceDerived {
		t.Errorf("handoff_source = %v, want %q", got, HandoffSourceDerived)
	}
	if files := devContextPaths(t, doc); !slices.Contains(files, "src/committed.go") {
		t.Errorf("derived files_changed = %v, want it to name src/committed.go", files)
	}
}

// TestDevHandoffMissing_DirtyTree_ReportsFiles is #134's own coverage: the
// verdict's Files/FileCount must surface the changed deliverable paths so a
// caller (feature-validate's Phase 0, via `gate verify --json`) can proceed
// against them instead of only the human-readable Evidence strings.
func TestDevHandoffMissing_DirtyTree_ReportsFiles(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, "internal", "scan", "testcmd.go"), "package scan\n")
	writeFile(t, filepath.Join(ws, "cmd", "nightgauge", "main.go"), "package main\n")

	v := devHandoffMissing(ws, "dev context file missing", filepath.Join(ws, ".nightgauge", "pipeline", "dev-221.json"))

	if !v.OK {
		t.Fatal("expected OK=true: dirty tree with deliverable files")
	}
	if v.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2", v.FileCount)
	}
	joined := strings.Join(v.Files, "\n")
	if !strings.Contains(joined, "internal/scan/testcmd.go") || !strings.Contains(joined, "cmd/nightgauge/main.go") {
		t.Errorf("Files does not list the changed deliverables: %v", v.Files)
	}
}

// TestDevHandoffMissing_CommittedWork_ReportsFiles covers the branch-ahead
// path (ChangedFilesAgainstDefaultBaseResolved), not just the dirty-tree one.
func TestDevHandoffMissing_CommittedWork_ReportsFiles(t *testing.T) {
	ws := gitRepo(t)
	git(t, ws, "checkout", "-b", "feat/221-thing")
	writeFile(t, filepath.Join(ws, "src", "committed.go"), "package src\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "work")

	v := devHandoffMissing(ws, "dev context file missing", filepath.Join(ws, ".nightgauge", "pipeline", "dev-221.json"))

	if !v.OK {
		t.Fatal("expected OK=true: branch carries commits ahead of base")
	}
	if v.FileCount != 1 {
		t.Errorf("FileCount = %d, want 1", v.FileCount)
	}
	if len(v.Files) != 1 || v.Files[0] != "src/committed.go" {
		t.Errorf("Files = %v, want [src/committed.go]", v.Files)
	}
}

// TestDevHandoffMissing_CleanTree_NoFiles is the genuinely-empty case: no
// files, no ok.
func TestDevHandoffMissing_CleanTree_NoFiles(t *testing.T) {
	ws := gitRepo(t)

	v := devHandoffMissing(ws, "dev context file missing", filepath.Join(ws, ".nightgauge", "pipeline", "dev-221.json"))

	if v.OK {
		t.Fatalf("expected OK=false: clean tree, branch level with base; verdict=%+v", v)
	}
	if len(v.Files) != 0 || v.FileCount != 0 {
		t.Errorf("expected no files on a genuinely empty verdict; verdict=%+v", v)
	}
}

// TestFeatureDevGate_EmptyContext_CleanTree_StaysNoOp is the guard against
// over-correction. When the context reports nothing AND git finds nothing, the
// original no-op verdict is correct and must survive — this fix widens what git
// can say, it does not stop the gate failing genuine no-ops.
func TestFeatureDevGate_EmptyContext_CleanTree_StaysNoOp(t *testing.T) {
	ws := gitRepo(t)
	emptyDevContext(t, ws, 221)

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Passed {
		t.Fatal("expected fail: nothing in the context, nothing on disk")
	}
	if gr.Kind != KindNoOp {
		t.Errorf("Kind = %q, want %q — a genuine no-op must still read as one", gr.Kind, KindNoOp)
	}
	if gr.Reason != "dev context records zero file changes" {
		t.Errorf("Reason = %q, want the original no-op reason unchanged", gr.Reason)
	}
}

// TestFeatureDevGate_EmptyContext_BookkeepingOnly_StaysNoOp guards the same
// boundary through the exclusion path: a workspace whose only changes are the
// pipeline's own files has produced nothing, and counting them would silently
// convert every no-op into a false "work is here" — disabling the check in any
// repo that does not gitignore .nightgauge.
func TestFeatureDevGate_EmptyContext_BookkeepingOnly_StaysNoOp(t *testing.T) {
	ws := gitRepo(t)
	emptyDevContext(t, ws, 221)
	writeFile(t, filepath.Join(ws, ".nightgauge", "scratch.txt"), "bookkeeping\n")
	writeFile(t, filepath.Join(ws, ".claude", "notes.md"), "notes\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Kind != KindNoOp {
		t.Errorf("Kind = %q, want %q — bookkeeping is not deliverable work; reason=%q",
			gr.Kind, KindNoOp, gr.Reason)
	}
}

// TestFeatureDevGate_EmptyContext_NonRepo_StaysNoOp holds the fail-open rule.
// When git cannot answer, the gate must fall back to its original verdict
// rather than inventing an exoneration — the mirror of
// TestFeatureDevGate_GroundTruth_NonRepoPassesOpen.
func TestFeatureDevGate_EmptyContext_NonRepo_StaysNoOp(t *testing.T) {
	ws := t.TempDir() // deliberately not a git repository
	emptyDevContext(t, ws, 221)

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Kind != KindNoOp {
		t.Errorf("Kind = %q, want %q — an undetermined answer must not exonerate", gr.Kind, KindNoOp)
	}
}

// readDevContext decodes the dev deliverable the gate left on disk. Every
// assertion about a derived handoff reads the FILE, not the gate verdict:
// #1076's whole complaint is that a verdict naming the work is not the same
// artifact as a handoff a later stage can consume.
func readDevContext(t *testing.T, ws string, issue int) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(ws, ".nightgauge", "pipeline", devContextName(issue)))
	if err != nil {
		t.Fatalf("dev context not on disk after the gate ran: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("dev context is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

// devContextPaths flattens a decoded deliverable's files_changed buckets.
func devContextPaths(t *testing.T, doc map[string]any) []string {
	t.Helper()
	fc, ok := doc["files_changed"].(map[string]any)
	if !ok {
		t.Fatalf("files_changed is not an object: %#v", doc["files_changed"])
	}
	var out []string
	for _, bucket := range []string{"created", "modified", "deleted"} {
		entries, _ := fc[bucket].([]any)
		for _, e := range entries {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// TestDerivedHandoff_MatchesInspectDevWork is AC2: the derived file list and
// the gate's own ground-truth probe must be ONE source, asserted rather than
// assumed.
//
// Two code paths reading git separately is how #202, #223 and #1176 each
// started — a detector and a producer that agree today and drift apart at the
// next edit. The count is deliberately pushed past maxFilesReported (10),
// because the capped Files list is exactly the plausible wrong input for a
// derivation: a handoff truncated at ten paths would hand feature-validate a
// short file set and look completely correct in every verdict string.
func TestDerivedHandoff_MatchesInspectDevWork(t *testing.T) {
	ws := gitRepo(t)
	var want []string
	for i := 0; i < 14; i++ {
		rel := filepath.Join("internal", "many", fmt.Sprintf("f%02d.go", i))
		writeFile(t, filepath.Join(ws, rel), "package many\n")
		want = append(want, filepath.ToSlash(rel))
	}

	gr := FeatureDevGate{}.Verify(context.Background(), 1076, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}

	work := inspectDevWork(ws, nil)
	got := devContextPaths(t, readDevContext(t, ws, 1076))

	if len(got) != work.FileCount {
		t.Errorf("derived handoff records %d file(s), inspectDevWork reports %d — two sources of ground truth",
			len(got), work.FileCount)
	}
	if len(got) <= maxFilesReported {
		t.Fatalf("test is not exercising the cap: %d file(s) <= maxFilesReported=%d", len(got), maxFilesReported)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("derived file list does not match the tree:\n got %v\nwant %v", got, want)
	}
}

// TestDerivedHandoff_PreservesAuthoredNarrative is AC3. A stage that recorded
// its approach and died before its file list keeps everything git cannot know.
//
// This is the case that separates "derive a handoff" from "overwrite the
// handoff": the mechanical fields must come from git and the narrative fields
// must survive, in the same document.
func TestDerivedHandoff_PreservesAuthoredNarrative(t *testing.T) {
	ws := gitRepo(t)
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(1076)), map[string]any{
		"files_changed": map[string]any{
			"created": []string{}, "modified": []string{}, "deleted": []string{},
		},
		"build_verification":        map[string]any{"ran": true, "status": "passed"},
		"knowledge_path":            ".nightgauge/knowledge/auth",
		"architectural_constraints": []string{"no new deps"},
		"retry_count":               2,
		"retry_reasons":             []string{"flaky suite"},
	})
	writeFile(t, filepath.Join(ws, "src", "work.go"), "package src\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 1076, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	doc := readDevContext(t, ws, 1076)

	if doc["knowledge_path"] != ".nightgauge/knowledge/auth" {
		t.Errorf("knowledge_path lost: %v", doc["knowledge_path"])
	}
	if got, _ := doc["retry_count"].(float64); got != 2 {
		t.Errorf("retry_count = %v, want 2", doc["retry_count"])
	}
	if doc["architectural_constraints"] == nil {
		t.Error("architectural_constraints lost — git cannot reconstruct these")
	}
	// git is authoritative for the file list, and must overwrite whatever the
	// partial document claimed.
	if files := devContextPaths(t, doc); !slices.Contains(files, "src/work.go") {
		t.Errorf("files_changed = %v, want git's answer", files)
	}
	derivation, _ := doc["handoff_derivation"].(map[string]any)
	if derivation["narrative_preserved"] != true {
		t.Errorf("narrative_preserved = %v, want true", derivation["narrative_preserved"])
	}
}

// TestDerivedHandoff_DoesNotClaimVerification guards the seam where this fix
// would quietly become the defect it is meant to remove.
//
// A derived handoff has NO evidence about the build or the suite — git proves
// which files changed and nothing whatsoever about whether they work. Stamping
// "passed" here would be the self-granted exemption docs/FAILURE_TAXONOMY.md
// names, and would make a degraded run indistinguishable from a healthy one to
// every downstream reader.
func TestDerivedHandoff_DoesNotClaimVerification(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, "src", "work.go"), "package src\n")

	if gr := (FeatureDevGate{}).Verify(context.Background(), 1076, ws); !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	doc := readDevContext(t, ws, 1076)

	bv, ok := doc["build_verification"].(map[string]any)
	if !ok {
		t.Fatalf("build_verification missing: %#v", doc["build_verification"])
	}
	if bv["ran"] != false {
		t.Errorf("build_verification.ran = %v, want false — nothing here ran a build", bv["ran"])
	}
	if bv["status"] == "passed" {
		t.Error("derived handoff claims the build passed; git witnessed no such thing")
	}
	if bv["status"] != "unverified" {
		t.Errorf("build_verification.status = %v, want %q", bv["status"], "unverified")
	}
	qc, _ := doc["quality_checks"].(map[string]any)
	for _, field := range []string{"code_standards", "security_review", "type_check", "dead_code_scan"} {
		if qc[field] != "not_run" {
			t.Errorf("quality_checks.%s = %v, want not_run", field, qc[field])
		}
	}
}

// TestDerivedHandoff_SurvivesTheDeliverablePolicy asserts the derived document
// is judged by the same rule table as an authored one and passes it clean.
//
// A repair that writes a document the schema would quarantine has moved the
// failure rather than fixed it — the run would die one check later, on the
// artifact this code produced.
func TestDerivedHandoff_SurvivesTheDeliverablePolicy(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, "src", "work.go"), "package src\n")

	if gr := (FeatureDevGate{}).Verify(context.Background(), 1076, ws); !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	path := filepath.Join(ws, ".nightgauge", "pipeline", devContextName(1076))
	outcome, err := deliverable.ApplyPolicyToFile("dev", path, time.Now())
	if err != nil {
		t.Fatalf("policy could not read the derived deliverable: %v", err)
	}
	if !outcome.OK() {
		t.Errorf("derived deliverable fails the schema policy: %v", outcome.Summary())
	}
	if outcome.Changed {
		t.Errorf("derived deliverable needed repair — it should be written canonical: %v", outcome.Summary())
	}
	want, _ := deliverable.CanonicalSchemaVersion("dev")
	if got := readDevContext(t, ws, 1076)["schema_version"]; got != want {
		t.Errorf("schema_version = %v, want %q", got, want)
	}
}
