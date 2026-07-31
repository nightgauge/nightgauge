package gates

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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

// TestFeatureDevGate_EmptyContext_DirtyTree_IsHandoffMissing is the #221
// reproduction and the core acceptance criterion: an empty files_changed plus a
// dirty worktree must NOT be a no-op.
func TestFeatureDevGate_EmptyContext_DirtyTree_IsHandoffMissing(t *testing.T) {
	ws := gitRepo(t)
	emptyDevContext(t, ws, 221)
	writeFile(t, filepath.Join(ws, "internal", "scan", "testcmd.go"), "package scan\n")
	writeFile(t, filepath.Join(ws, "cmd", "nightgauge", "main.go"), "package main\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Passed {
		t.Fatal("expected fail: the handoff was never written, so no later stage can consume this work")
	}
	if gr.Kind == KindNoOp {
		t.Errorf("Kind = %q — a no-op is wrapped by the scheduler as "+
			"\"exited 0 with no state change\", and there WAS a state change; "+
			"that sentence is what sent an operator looking for work sitting on disk", gr.Kind)
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
	// The verdict must not assert the opposite of what is on disk.
	if strings.Contains(gr.Reason, "records zero file changes,") &&
		!strings.Contains(gr.Reason, "but git finds") {
		t.Errorf("reason contradicts the filesystem without correcting itself: %q", gr.Reason)
	}
	if !strings.Contains(gr.Reason, "git finds 2 changed file(s)") {
		t.Errorf("reason does not name what git actually found: %q", gr.Reason)
	}
	// Naming the files is what makes the verdict actionable without opening
	// the worktree.
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "internal/scan/testcmd.go") {
		t.Errorf("evidence does not name the changed files:\n%s", joined)
	}
	if !strings.Contains(joined, "must be preserved") {
		t.Errorf("evidence does not tell the operator the work is salvageable:\n%s", joined)
	}
}

// TestFeatureDevGate_MissingContext_DirtyTree_IsHandoffMissing covers the other
// early return — the context file absent entirely rather than empty. Same
// question, same answer; #221 hit the empty variant but a stage killed slightly
// earlier produces this one.
func TestFeatureDevGate_MissingContext_DirtyTree_IsHandoffMissing(t *testing.T) {
	ws := gitRepo(t)
	// deliberately no dev context written
	writeFile(t, filepath.Join(ws, "src", "real_work.go"), "package src\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 221, ws)

	if gr.Passed {
		t.Fatal("expected fail: no handoff exists")
	}
	if gr.Kind != KindFail {
		t.Errorf("Kind = %q, want %q", gr.Kind, KindFail)
	}
	if gr.TerminalKind != TerminalKindDevHandoffMissing {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevHandoffMissing)
	}
	if !strings.Contains(gr.Reason, "dev context file missing") {
		t.Errorf("reason drops the original condition: %q", gr.Reason)
	}
}

// TestFeatureDevGate_EmptyContext_CommittedWork_IsHandoffMissing covers a stage
// that committed its work (against the #1608 contract) and still failed to
// write the handoff. The commits are real output and must be recognized.
func TestFeatureDevGate_EmptyContext_CommittedWork_IsHandoffMissing(t *testing.T) {
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

	if gr.TerminalKind != TerminalKindDevHandoffMissing {
		t.Errorf("TerminalKind = %q, want %q — committed work is still work; reason=%q",
			gr.TerminalKind, TerminalKindDevHandoffMissing, gr.Reason)
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
