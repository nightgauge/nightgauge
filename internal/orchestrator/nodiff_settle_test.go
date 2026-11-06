package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

type fakeNoDiffIssue struct {
	commentErr error
	closeErr   error
	comments   []string
	closes     int
}

func (f *fakeNoDiffIssue) AddComment(_ context.Context, _ string, body string) error {
	f.comments = append(f.comments, body)
	return f.commentErr
}

func (f *fakeNoDiffIssue) CloseIssue(_ context.Context, _ string) error {
	f.closes++
	return f.closeErr
}

func noDiffResult(statuses ...string) pmstages.PRCreateResult {
	res := pmstages.PRCreateResult{Branch: "feat/3012-x", BaseBranch: "main"}
	for i, s := range statuses {
		res.ACVerdicts = append(res.ACVerdicts, pmstages.ACVerdict{Index: i + 1, Text: "criterion", Status: s})
	}
	return res
}

func TestSettleNoDiffAllMetClosesIssue(t *testing.T) {
	f := &fakeNoDiffIssue{}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", 3012, noDiffResult(pmstages.ACMet, pmstages.ACMet))
	if !closed || f.closes != 1 || len(f.comments) != 1 {
		t.Fatalf("closed=%v closes=%d comments=%d, want closed with one comment", closed, f.closes, len(f.comments))
	}
	if !strings.HasPrefix(detail, "issue_closed:") {
		t.Errorf("detail = %q, want issue_closed", detail)
	}
}

func TestSettleNoDiffUnmetParksIssue(t *testing.T) {
	f := &fakeNoDiffIssue{}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", 3012, noDiffResult(pmstages.ACMet, pmstages.ACUnmet))
	if closed || f.closes != 0 || len(f.comments) != 1 {
		t.Fatalf("closed=%v closes=%d comments=%d, want parked with one comment", closed, f.closes, len(f.comments))
	}
	if !strings.Contains(detail, "[not-pipeline-actionable]") || !strings.Contains(detail, "1 acceptance criteria") {
		t.Errorf("detail = %q, want not-pipeline-actionable naming 1 unmet", detail)
	}
}

func TestSettleNoDiffCommentFailurePreventsClose(t *testing.T) {
	f := &fakeNoDiffIssue{commentErr: errors.New("boom")}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", 3012, noDiffResult(pmstages.ACMet))
	if closed || f.closes != 0 {
		t.Fatalf("closed=%v closes=%d, want no close after a failed comment", closed, f.closes)
	}
	if !strings.Contains(detail, "[not-pipeline-actionable]") {
		t.Errorf("detail = %q, want parked", detail)
	}
}

func TestSettleNoDiffCloseFailureParks(t *testing.T) {
	f := &fakeNoDiffIssue{closeErr: errors.New("boom")}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", 3012, noDiffResult(pmstages.ACMet))
	if closed || !strings.Contains(detail, "[not-pipeline-actionable]") {
		t.Errorf("closed=%v detail=%q, want parked after a failed close", closed, detail)
	}
}

func TestSettleNoDiffNoNodeIDRecordsNothing(t *testing.T) {
	f := &fakeNoDiffIssue{}
	_, closed := SettleNoDiff(context.Background(), f, f, "", 3012, noDiffResult(pmstages.ACMet))
	if closed || len(f.comments) != 0 || f.closes != 0 {
		t.Errorf("closed=%v comments=%d closes=%d, want nothing recorded", closed, len(f.comments), f.closes)
	}
}
