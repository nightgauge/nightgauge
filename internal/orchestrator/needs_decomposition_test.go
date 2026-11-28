package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
)

// fakeDecompositionOps records labels and comments per issue.
type fakeDecompositionOps struct {
	labels   []string
	comments []string
	readErr  error
	labelErr error
}

func (f *fakeDecompositionOps) HasMarkerComment(_ context.Context, _, _ string, _ int, marker string) (bool, error) {
	if f.readErr != nil {
		return false, f.readErr
	}
	for _, c := range f.comments {
		if strings.Contains(c, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeDecompositionOps) AddComment(_ context.Context, _, _ string, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakeDecompositionOps) AddLabel(_ context.Context, _, _ string, _ int, label string) error {
	if f.labelErr != nil {
		return f.labelErr
	}
	f.labels = append(f.labels, label)
	return nil
}

// gateDetail is the failure text an oversized plan's planning gate produces.
func gateDetail(t *testing.T) string {
	t.Helper()
	return gateFailureError(gates.GateResult{
		Reason: gates.NeedsDecompositionMarker + " plan lists 31 files, over max_planned_files=25; " +
			"plan is size XL but the issue is labelled size:S",
		Evidence:     []string{"planned_files=31", "max_planned_files=25", "plan_size=XL", "issue_size=S"},
		Kind:         gates.KindFail,
		TerminalKind: gates.TerminalKindNeedsDecomposition,
	}).Error()
}

func TestGateFailureError_NeedsDecompositionCarriesEvidence(t *testing.T) {
	t.Parallel()
	msg := gateDetail(t)
	if got := ClassifyTerminalKind(msg); got != TerminalKindNeedsDecomposition {
		t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", msg, got, TerminalKindNeedsDecomposition)
	}
	want := needsDecompositionDetail{PlannedFiles: 31, MaxPlannedFiles: 25, PlanSize: "XL", IssueSize: "S"}
	if got := parseNeedsDecompositionDetail(msg); got != want {
		t.Errorf("parsed %+v, want %+v", got, want)
	}
	if !TerminalKindParks(TerminalKindNeedsDecomposition) || TerminalKindRemediation(TerminalKindNeedsDecomposition) == "" {
		t.Error("needs_decomposition must park with a remediation")
	}
}

func TestApplyNeedsDecomposition_LabelsAndCommentsOnce(t *testing.T) {
	t.Parallel()
	ops := &fakeDecompositionOps{}
	detail := gateDetail(t)
	for i := 0; i < 2; i++ {
		if err := applyNeedsDecomposition(context.Background(), ops, "acme/web", 42, detail); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(ops.comments) != 1 {
		t.Fatalf("posted %d comments over two runs, want 1", len(ops.comments))
	}
	c := ops.comments[0]
	for _, want := range []string{needsDecompositionCommentMarker, "#42", "Planned files: 31", "`pipeline.size_gate.thresholds.max_planned_files`: 25",
		"Plan size: XL", "Issue size label: S"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q:\n%s", want, c)
		}
	}
	if len(ops.labels) == 0 || ops.labels[0] != needsDecompositionLabel {
		t.Errorf("labels = %v, want %q applied", ops.labels, needsDecompositionLabel)
	}
}

func TestApplyNeedsDecomposition_Failures(t *testing.T) {
	t.Parallel()
	// An unreadable comment list posts nothing: the dedupe cannot hold.
	ops := &fakeDecompositionOps{readErr: errors.New("boom")}
	boom := errors.New("boom")
	ops.readErr = boom
	if err := applyNeedsDecomposition(context.Background(), ops, "acme/web", 42, gateDetail(t)); !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the read error", err)
	}
	if len(ops.comments) != 0 || len(ops.labels) != 1 {
		t.Errorf("comments=%d labels=%v, want no comment and the label", len(ops.comments), ops.labels)
	}
	// A label failure still posts the comment.
	ops = &fakeDecompositionOps{labelErr: errors.New("no perms")}
	if err := applyNeedsDecomposition(context.Background(), ops, "acme/web", 42, gateDetail(t)); err == nil {
		t.Error("want the label error returned")
	}
	if len(ops.comments) != 1 {
		t.Errorf("comments=%d, want the comment despite the label failure", len(ops.comments))
	}
}

func TestNeedsDecompositionComment_UnknownSizesAndDisabledCount(t *testing.T) {
	t.Parallel()
	c := needsDecompositionComment(7, needsDecompositionDetail{PlannedFiles: 3})
	for _, want := range []string{"Plan size: unknown", "Issue size label: unknown", "0 (file-count check disabled)"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q:\n%s", want, c)
		}
	}
}
