package gates

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/deliverable"
)

// #1482. The #1076 derivation fired only when the handoff was ABSENT or empty.
// A handoff that is present, lists every file, and is missing
// `build_verification` was the same defect wearing a different shape — and it
// was more expensive, because the deliverable policy stamped the contract
// version onto it first, so the run record showed a certified document being
// rejected by the contract it had just been certified against.
//
// The specimen: a docs-only run in a downstream site repository. feature-dev
// changed 5 files, ran its static-site build clean, and recorded the outcome as
// prose under `quality_checks.build`. Check 3 failed the stage, the issue went
// back to Ready with backoff, and the repository halted behind it with the
// implementation sitting uncommitted in its stage worktree.
//
// Like the rest of this package's ground-truth tests, these drive REAL git.

// devContextMissingBuildVerification writes the #1482 shape: a complete file
// list, the build recorded as prose in the wrong field, no build_verification.
func devContextMissingBuildVerification(t *testing.T, ws string, issue int, files ...string) {
	t.Helper()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(issue)), map[string]any{
		// The stale version #1177's rule repairs, so this fixture exercises
		// the exact composition #1482 reported: stamp, then reject.
		"schema_version": "1.0",
		"issue_number":   issue,
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": files,
			"deleted":  []string{},
		},
		"quality_checks": map[string]any{
			"build": "hugo --minify succeeded with no errors",
		},
	})
}

// TestFeatureDevGate_MissingBuildVerification_DirtyTree_IsDerived is the #1482
// reproduction and the primary acceptance criterion: present file, schema
// version repaired, `build_verification` absent, worktree dirty → a pass that
// says plainly where the deliverable came from.
func TestFeatureDevGate_MissingBuildVerification_DirtyTree_IsDerived(t *testing.T) {
	ws := gitRepo(t)
	devContextMissingBuildVerification(t, ws, 69, "content/index.md")
	writeFile(t, filepath.Join(ws, "content", "index.md"), "# hello\n")
	writeFile(t, filepath.Join(ws, "layouts", "single.html"), "<p>x</p>\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 69, ws)

	if !gr.Passed {
		t.Fatalf("expected pass: the work is on disk and build_verification is derivable; reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
	if gr.TerminalKind != "" {
		t.Errorf("TerminalKind = %q, want empty", gr.TerminalKind)
	}

	doc := readDevContext(t, ws, 69)
	if got := doc["handoff_source"]; got != HandoffSourceDerived {
		t.Errorf("handoff_source = %v, want %q", got, HandoffSourceDerived)
	}
	bv, ok := doc["build_verification"].(map[string]any)
	if !ok {
		t.Fatalf("derived deliverable has no build_verification object: %v", doc["build_verification"])
	}
	if bv["status"] != "unverified" {
		t.Errorf("build_verification.status = %v, want %q — deriving must never claim a build it did not witness", bv["status"], "unverified")
	}
	if bv["ran"] != false {
		t.Errorf("build_verification.ran = %v, want false", bv["ran"])
	}
	if files := devContextPaths(t, doc); !slices.Contains(files, "content/index.md") {
		t.Errorf("derived files_changed lacks content/index.md; got %v", files)
	}

	// The derivation has to be legible in the run record — a repair nobody can
	// see in the gate evidence is a repair nobody fixes.
	joined := strings.Join(gr.Evidence, "\n")
	for _, want := range []string{
		"handoff_source=derived",
		"dev context lacks build_verification",
		"dev.build_verification.recorded_as_prose",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("gate evidence does not record %q:\n%s", want, joined)
		}
	}

	// And in the deliverable itself, for the stages and miners that read the
	// file rather than the verdict.
	marker, ok := doc[deliverable.PolicyMarkerField].(map[string]any)
	if !ok {
		t.Fatalf("derived deliverable dropped the policy marker: %v", doc[deliverable.PolicyMarkerField])
	}
	untrustworthy, _ := marker["untrustworthy"].([]any)
	if !slices.Contains(untrustworthy, any("build_verification")) {
		t.Errorf("policy marker does not name build_verification untrustworthy: %v", marker)
	}
}

// TestFeatureDevGate_MissingBuildVerification_CleanTree_StillFails is the
// boundary. #1482 must not become a licence to pass any deliverable with a
// missing object: with nothing in the worktree there is no ground truth to
// derive from, and check 3's original verdict is still the right answer.
func TestFeatureDevGate_MissingBuildVerification_CleanTree_StillFails(t *testing.T) {
	ws := gitRepo(t)
	// The context claims a file, git finds nothing — the tree is untouched.
	devContextMissingBuildVerification(t, ws, 70, "content/index.md")

	gr := FeatureDevGate{}.Verify(context.Background(), 70, ws)

	if gr.Passed {
		t.Fatal("gate passed with no work in the tree — #1482 has papered over #55")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindFail)
	}
	if gr.TerminalKind != TerminalKindDevBuildVerificationMissing {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevBuildVerificationMissing)
	}
	if !strings.Contains(gr.Reason, "lacks build_verification") {
		t.Errorf("reason no longer names the missing object: %q", gr.Reason)
	}
}

// TestFeatureDevGate_BuildVerificationFailed_StillFails pins the other
// boundary. Derivation repairs an ABSENT record, never a bad one: a recorded
// build failure is real work that broke, and it must keep reaching check 4 as a
// terminal failure even with a dirty tree that would otherwise derive.
func TestFeatureDevGate_BuildVerificationFailed_StillFails(t *testing.T) {
	ws := gitRepo(t)
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(71)), map[string]any{
		"schema_version": "1.9",
		"issue_number":   71,
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": []string{"content/index.md"},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{"ran": true, "status": "failed"},
	})
	writeFile(t, filepath.Join(ws, "content", "index.md"), "# hello\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 71, ws)

	if gr.Passed {
		t.Fatal("gate passed over a recorded build failure")
	}
	if gr.TerminalKind != TerminalKindDevBuildVerificationFailed {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevBuildVerificationFailed)
	}
	// The derivation must not have overwritten the stage's own verdict.
	doc := readDevContext(t, ws, 71)
	if got := doc["handoff_source"]; got != nil {
		t.Errorf("handoff_source = %v, want unset — a failed build is not a derivable defect", got)
	}
}

// TestFeatureDevGate_MissingBuildVerification_DerivationDisabled is the
// mutation check for #1482, in the same shape as #1076's AC6: turning the
// derivation off must restore a terminal failure. Without it every assertion
// above would pass just as happily against a gate made unconditionally
// permissive.
func TestFeatureDevGate_MissingBuildVerification_DerivationDisabled(t *testing.T) {
	defer func(prev bool) { deriveDevHandoffEnabled = prev }(deriveDevHandoffEnabled)
	deriveDevHandoffEnabled = false

	ws := gitRepo(t)
	devContextMissingBuildVerification(t, ws, 69, "content/index.md")
	writeFile(t, filepath.Join(ws, "content", "index.md"), "# hello\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 69, ws)

	if gr.Passed {
		t.Fatal("derivation disabled but the gate still passed — the pass is not coming from the derivation")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindFail)
	}
}
