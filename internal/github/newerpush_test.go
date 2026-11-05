package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	npMerge = "1111111111111111111111111111111111111111"
	npNewer = "2222222222222222222222222222222222222222"
)

func npProv() *MergeProvenance {
	return &MergeProvenance{PRNumber: 7, MergeSHA: npMerge, MergeTree: "t1", HeadSHA: "h000000000", HeadTree: "t1", BaseRef: "main", MergedAt: mergedAt}
}

func npCancelled() CheckDetail {
	return CheckDetail{Name: "main-build", Status: "COMPLETED", Conclusion: "CANCELLED", App: GitHubActionsApp, CheckSuiteID: 41, HeadSHA: npMerge}
}

// TestEvaluateMergedCommitNewerPushCancel pins #2952 in the binary: a run a
// newer push cancelled is INFO, never RED; a failure and an unproven cancel
// stay RED.
func TestEvaluateMergedCommitNewerPushCancel(t *testing.T) {
	required := []string{"build"}
	head := []CheckDetail{check("build", "COMPLETED", "SUCCESS")}
	late := mergedAt.Add(time.Hour)
	failed := npCancelled()
	failed.Conclusion = "FAILURE"
	cases := []struct {
		name    string
		checks  []CheckDetail
		cancels map[int64]string
		tree    string
		want    ChecksCompleteVerdict
		reason  string
	}{
		{"cancelled by a newer push: green with info", []CheckDetail{npCancelled()}, map[int64]string{41: npNewer}, "t1", ChecksComplete, "cancelled by a newer push; judge 22222222"},
		{"cancel not proven: red", []CheckDetail{npCancelled()}, nil, "t1", ChecksIncomplete, "main-build"},
		{"a failure is never relaxed", []CheckDetail{failed}, map[int64]string{41: npNewer}, "t1", ChecksIncomplete, "main-build"},
		{"another suite's cancel stays red", []CheckDetail{npCancelled()}, map[int64]string{99: npNewer}, "t1", ChecksIncomplete, "main-build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := MergeEvidence{Provenance: npProv(), HeadChecks: head, MergeChecks: tc.checks, RequiredNames: required,
				RequiredKnown: true, Now: late, NewerPushCancels: tc.cancels,
				Runs: []WorkflowRunSummary{{Name: "Main build", Status: "COMPLETED", Conclusion: "CANCELLED", CheckSuiteID: 41}}}
			got, reasons := EvaluateMergedCommit(ev)
			if got != tc.want || !strings.Contains(strings.Join(reasons, "\n"), tc.reason) {
				t.Fatalf("got %s %q, want %s containing %q", got, reasons, tc.want, tc.reason)
			}
		})
	}
}

type fakeNewerPush struct {
	runs    []WorkflowRunSummary
	later   []WorkflowRunSummary
	laterEr error
	compare string
	cmpErr  error
}

func (f fakeNewerPush) GetWorkflowRunsForRef(context.Context, string, string, string) ([]WorkflowRunSummary, error) {
	return f.runs, nil
}
func (f fakeNewerPush) GetWorkflowPushRuns(context.Context, string, string, int64, string) ([]WorkflowRunSummary, error) {
	return f.later, f.laterEr
}
func (f fakeNewerPush) CompareStatus(context.Context, string, string, string, string) (string, error) {
	return f.compare, f.cmpErr
}

// TestNewerPushCancelsFor pins the facts the drop needs: every unreadable or
// non-matching fact drops nothing.
func TestNewerPushCancelsFor(t *testing.T) {
	t0 := mergedAt.Add(time.Minute)
	own := WorkflowRunSummary{CheckSuiteID: 41, WorkflowID: 5, Event: "push", HeadBranch: "main", CreatedAt: t0}
	newer := WorkflowRunSummary{HeadSHA: npNewer, Event: "push", HeadBranch: "main", CreatedAt: t0.Add(time.Minute)}
	older := newer
	older.CreatedAt = t0.Add(-time.Minute)
	prRun := own
	prRun.Event = "pull_request"
	ok := fakeNewerPush{runs: []WorkflowRunSummary{own}, later: []WorkflowRunSummary{newer}, compare: "ahead"}
	cases := []struct {
		name string
		r    any
		prov *MergeProvenance
		want bool
	}{
		{"newer descendant push", ok, npProv(), true},
		{"compare not ahead", fakeNewerPush{runs: ok.runs, later: ok.later, compare: "diverged"}, npProv(), false},
		{"compare unreadable", fakeNewerPush{runs: ok.runs, later: ok.later, cmpErr: errors.New("x")}, npProv(), false},
		{"later runs unreadable", fakeNewerPush{runs: ok.runs, laterEr: errors.New("x")}, npProv(), false},
		{"only an older run", fakeNewerPush{runs: ok.runs, later: []WorkflowRunSummary{older}, compare: "ahead"}, npProv(), false},
		{"own run not a push", fakeNewerPush{runs: []WorkflowRunSummary{prRun}, later: ok.later, compare: "ahead"}, npProv(), false},
		{"no merged PR", ok, nil, false},
		{"reader lacks the capability", struct{}{}, npProv(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewerPushCancelsFor(context.Background(), tc.r, "o", "r", npMerge, tc.prov, []CheckDetail{npCancelled()}, nil)
			if (got[41] == npNewer) != tc.want {
				t.Fatalf("got %v, want hit=%v", got, tc.want)
			}
		})
	}
}
