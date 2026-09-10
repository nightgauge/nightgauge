package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEvaluateChecksComplete_MissingRequiredCheckIsNotYet is the #1540
// regression guard: a rollup carrying 15 of 16 required names, all
// COMPLETED/SUCCESS, must not read as complete just because nothing PRESENT
// failed.
func TestEvaluateChecksComplete_MissingRequiredCheckIsNotYet(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
		// "publication boundary" is required but absent from the rollup.
	}
	verdict, reasons := EvaluateChecksComplete(checks, []string{"lint", "build", "publication boundary"})
	if verdict != ChecksNotYet {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksNotYet)
	}
	if len(reasons) == 0 {
		t.Error("want a reason naming the missing check")
	}
}

func TestEvaluateChecksComplete_AllRequiredPresentAndPassingIsGreen(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	verdict, _ := EvaluateChecksComplete(checks, []string{"lint", "build"})
	if verdict != ChecksComplete {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksComplete)
	}
}

func TestEvaluateChecksComplete_RequiredCheckStillPendingIsNotYet(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "IN_PROGRESS", Conclusion: ""},
	}
	verdict, _ := EvaluateChecksComplete(checks, []string{"lint"})
	if verdict != ChecksNotYet {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksNotYet)
	}
}

func TestEvaluateChecksComplete_RequiredCheckFailedIsIncomplete(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "FAILURE"},
	}
	verdict, reasons := EvaluateChecksComplete(checks, []string{"lint"})
	if verdict != ChecksIncomplete {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksIncomplete)
	}
	if len(reasons) == 0 {
		t.Error("want a reason naming the failed check")
	}
}

func TestEvaluateChecksComplete_CaseInsensitiveNameMatch(t *testing.T) {
	checks := []CheckDetail{
		{Name: "Lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	verdict, _ := EvaluateChecksComplete(checks, []string{"lint"})
	if verdict != ChecksComplete {
		t.Fatalf("verdict = %q, want %q (name match must be case-insensitive)", verdict, ChecksComplete)
	}
}

func TestEvaluateChecksComplete_DuplicateStatusSurfaceFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		second CheckDetail
		want   ChecksCompleteVerdict
	}{
		{name: "pending is not masked", second: CheckDetail{Name: "cla", Status: "IN_PROGRESS"}, want: ChecksNotYet},
		{name: "failure is not masked", second: CheckDetail{Name: "cla", Status: "COMPLETED", Conclusion: "FAILURE"}, want: ChecksIncomplete},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks := []CheckDetail{{Name: "cla", Status: "COMPLETED", Conclusion: "SUCCESS"}, tc.second}
			verdict, _ := EvaluateChecksComplete(checks, []string{"cla"})
			if verdict != tc.want {
				t.Fatalf("verdict = %q, want %q", verdict, tc.want)
			}
		})
	}
}

func TestGetCommitStatuses_MapsCombinedStatusSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/abc123/status" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"sha":"abc123",
			"statuses":[
				{"context":"cla","state":"success","updated_at":"2026-09-10T23:24:40Z","target_url":"https://example.test/cla"},
				{"context":"deploy","state":"pending","updated_at":"2026-09-10T23:25:00Z"}
			]
		}`))
	}))
	defer srv.Close()

	statuses, err := newCIServiceForRESTTest(srv).GetCommitStatuses(context.Background(), "o", "r", "abc123")
	if err != nil {
		t.Fatalf("GetCommitStatuses: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses = %d, want 2", len(statuses))
	}
	if got := statuses[0]; got.Name != "cla" || got.Status != "COMPLETED" || got.Conclusion != "SUCCESS" || got.CompletedAt == "" || got.HeadSHA != "abc123" {
		t.Errorf("successful status = %+v", got)
	}
	if got := statuses[1]; got.Status != "IN_PROGRESS" || got.Conclusion != "" || got.CompletedAt != "" {
		t.Errorf("pending status = %+v", got)
	}
}

// TestEvaluateChecksComplete_UnprotectedBranchFallsBackToAllChecks: no
// required-check set was resolved (no branch protection/ruleset) — the
// caller must still get a real verdict, not a permanent NOT-YET.
func TestEvaluateChecksComplete_UnprotectedBranchFallsBackToAllChecks(t *testing.T) {
	t.Run("all passing is green", func(t *testing.T) {
		checks := []CheckDetail{
			{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
		}
		verdict, _ := EvaluateChecksComplete(checks, nil)
		if verdict != ChecksComplete {
			t.Fatalf("verdict = %q, want %q", verdict, ChecksComplete)
		}
	})

	t.Run("empty rollup is not-yet, never green", func(t *testing.T) {
		verdict, _ := EvaluateChecksComplete(nil, nil)
		if verdict != ChecksNotYet {
			t.Fatalf("verdict = %q, want %q — an empty rollup is not evidence of nothing to fail", verdict, ChecksNotYet)
		}
	})

	t.Run("a failure is incomplete", func(t *testing.T) {
		checks := []CheckDetail{
			{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"},
		}
		verdict, _ := EvaluateChecksComplete(checks, nil)
		if verdict != ChecksIncomplete {
			t.Fatalf("verdict = %q, want %q", verdict, ChecksIncomplete)
		}
	})
}

// TestEvaluateChecksComplete_CrossCheckDisagreementIsNotYet is the #1540 AC:
// a rollup that disagrees with the per-run endpoint yields NOT-YET even
// though every required name is present and concluded in the rollup itself.
func TestEvaluateChecksComplete_CrossCheckDisagreementIsNotYet(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	runs := []WorkflowRunSummary{
		{ID: 1, Name: "lint", Status: "IN_PROGRESS"},
	}
	verdict, reasons := EvaluateChecksCompleteCrossChecked(checks, []string{"lint"}, runs)
	if verdict != ChecksNotYet {
		t.Fatalf("verdict = %q, want %q — the rollup and actions/runs disagree", verdict, ChecksNotYet)
	}
	if len(reasons) == 0 {
		t.Error("want a reason describing the disagreement")
	}
}

func TestEvaluateChecksComplete_CrossCheckAgreementStaysGreen(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	runs := []WorkflowRunSummary{
		{ID: 1, Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	verdict, _ := EvaluateChecksCompleteCrossChecked(checks, []string{"lint"}, runs)
	if verdict != ChecksComplete {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksComplete)
	}
}

// No actions/runs data available (nil runs) must not block on the auxiliary
// lookup — the required-name verdict stands on its own.
func TestEvaluateChecksComplete_CrossCheckSkippedWhenRunsUnavailable(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}
	verdict, _ := EvaluateChecksCompleteCrossChecked(checks, []string{"lint"}, nil)
	if verdict != ChecksComplete {
		t.Fatalf("verdict = %q, want %q — nil runs must not block the verdict", verdict, ChecksComplete)
	}
}

// The cross-check must not fire while a required check is still legitimately
// pending in the rollup itself — that is already NOT-YET without spending an
// extra API call.
func TestEvaluateChecksComplete_CrossCheckNotConsultedWhilePending(t *testing.T) {
	checks := []CheckDetail{
		{Name: "lint", Status: "IN_PROGRESS", Conclusion: ""},
	}
	// Deliberately nil: if the cross-check were consulted with pending
	// verdicts, a nil `runs` would need special-casing. It must not be
	// reached at all.
	verdict, _ := EvaluateChecksCompleteCrossChecked(checks, []string{"lint"}, nil)
	if verdict != ChecksNotYet {
		t.Fatalf("verdict = %q, want %q", verdict, ChecksNotYet)
	}
}

func TestCrossCheckWorkflowRuns_NoRunsAgreesByDefault(t *testing.T) {
	agree, reasons := CrossCheckWorkflowRuns(nil, nil)
	if !agree || reasons != nil {
		t.Errorf("agree=%v reasons=%v, want true/nil when no run data is available", agree, reasons)
	}
}
