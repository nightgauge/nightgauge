package github

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var mergedAt = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func check(name, status, conclusion string) CheckDetail {
	return CheckDetail{Name: name, Status: status, Conclusion: conclusion}
}

func prov(mergeTree, headTree string) *MergeProvenance {
	return &MergeProvenance{PRNumber: 7, MergeSHA: "m000000000", MergeTree: mergeTree, HeadSHA: "h000000000", HeadTree: headTree, BaseRef: "main", MergedAt: mergedAt}
}

// TestEvaluateMergedCommit pins the #2055 post-merge rule: with matching
// trees the PR head's required checks are the gate and the merge commit only
// has to be green or running in what it still runs.
func TestEvaluateMergedCommit(t *testing.T) {
	required := []string{"build", "lint"}
	headGreen := []CheckDetail{check("build", "COMPLETED", "SUCCESS"), check("lint", "COMPLETED", "SKIPPED"), check("advisory", "COMPLETED", "FAILURE")}
	headRed := []CheckDetail{check("build", "COMPLETED", "FAILURE"), check("lint", "COMPLETED", "SUCCESS")}
	pushGreen := []CheckDetail{check("CodeQL", "COMPLETED", "SUCCESS"), check("cache-warm", "COMPLETED", "SUCCESS")}
	pushRunning := []CheckDetail{check("CodeQL", "IN_PROGRESS", ""), check("cache-warm", "COMPLETED", "SUCCESS")}
	pushRed := []CheckDetail{check("CodeQL", "COMPLETED", "FAILURE")}
	late := mergedAt.Add(time.Hour)

	cases := []struct {
		name   string
		ev     MergeEvidence
		want   ChecksCompleteVerdict
		reason string
	}{
		{"tree equal, head required green, push green", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksComplete, ""},
		{"a failing advisory check on the head does not make the merge red", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksComplete, ""},
		{"PR head's required check red", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headRed, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksIncomplete, "PR #7 head"},
		{"a required check absent from the head is not-yet", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen[:1], MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksNotYet, "lint"},
		{"push job still running", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushRunning, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksNotYet, "CodeQL"},
		{"push job red", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushRed, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksIncomplete, "merge commit"},
		{"no push checks yet, inside the grace", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, RequiredNames: required, RequiredKnown: true, Now: mergedAt.Add(time.Minute)}, ChecksNotYet, "no checks on the merge commit yet"},
		{"no push checks after the grace: nothing runs on push", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksComplete, ""},
		{"a workflow run still in flight is not-yet", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late,
			Runs: []WorkflowRunSummary{{Name: "CodeQL", Status: "IN_PROGRESS"}}}, ChecksNotYet, "still in flight"},
		// Trees differ: the PR run is not evidence; the merge commit must
		// carry every required check itself, which it does not here.
		{"tree differs, required absent, inside the grace: not-yet", MergeEvidence{Provenance: prov("t1", "t2"), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: mergedAt.Add(time.Minute)}, ChecksNotYet, "differs from PR #7"},
		{"tree differs, required absent, after the grace: red, never tested", MergeEvidence{Provenance: prov("t1", "t2"), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksIncomplete, "workflow_dispatch"},
		{"tree differs, a required check running on the merge commit: not-yet", MergeEvidence{Provenance: prov("t1", "t2"),
			MergeChecks: []CheckDetail{check("build", "IN_PROGRESS", "")}, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksNotYet, "differs"},
		{"tree differs, merge commit carries every required check", MergeEvidence{Provenance: prov("t1", "t2"),
			MergeChecks: []CheckDetail{check("build", "COMPLETED", "SUCCESS"), check("lint", "COMPLETED", "SUCCESS")}, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksComplete, ""},
		{"an unread tree never matches", MergeEvidence{Provenance: prov("", ""), HeadChecks: headGreen, MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: mergedAt.Add(time.Minute)}, ChecksNotYet, "differs"},
		// cache-warm tests nothing: a red one never makes main red.
		{"a red cache-warm is informational", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen,
			MergeChecks: []CheckDetail{check("CodeQL", "COMPLETED", "SUCCESS"), check("cache-warm", "COMPLETED", "FAILURE")}, RequiredNames: required, RequiredKnown: true, Now: late,
			Runs: []WorkflowRunSummary{{Name: "Cache warm", Status: "IN_PROGRESS"}}}, ChecksComplete, ""},
		// An unknown required set is never green.
		{"tree equal, required set unknown: not-yet", MergeEvidence{Provenance: prov("t1", "t1"), HeadChecks: headGreen, MergeChecks: pushGreen, Now: late}, ChecksNotYet, "could not be read"},
		{"no merged PR, required set unknown: not-yet", MergeEvidence{MergeChecks: pushGreen, Now: late}, ChecksNotYet, "could not be read"},
		{"tree differs, merge commit green, required set unknown: not-yet", MergeEvidence{Provenance: prov("t1", "t2"),
			MergeChecks: []CheckDetail{check("build", "COMPLETED", "SUCCESS"), check("lint", "COMPLETED", "SUCCESS")}, Now: late}, ChecksNotYet, "could not be read"},
		{"no merged PR keeps the merge-commit rule", MergeEvidence{MergeChecks: pushGreen, RequiredNames: required, RequiredKnown: true, Now: late}, ChecksNotYet, "required check(s) absent"},
		{"no merged PR, no required set, all green", MergeEvidence{MergeChecks: pushGreen, RequiredKnown: true, Now: late}, ChecksComplete, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := EvaluateMergedCommit(tc.ev)
			if got != tc.want {
				t.Fatalf("verdict = %q (%v), want %q", got, reasons, tc.want)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(reasons, "; "), tc.reason) {
				t.Errorf("reasons %v do not mention %q", reasons, tc.reason)
			}
		})
	}
}

func TestGetMergeProvenance(t *testing.T) {
	const merge = "1111111111111111111111111111111111111111"
	const head = "2222222222222222222222222222222222222222"
	srv := httptest.NewServer(PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/commits/1111111":                                        {`{"sha":"` + merge + `","commit":{"tree":{"sha":"tree-a"}}}`},
		"/repos/o/r/commits/" + head:                                        {`{"sha":"` + head + `","commit":{"tree":{"sha":"tree-a"}}}`},
		"/repos/o/r/commits/" + merge + "/pulls":                            {`[{"number":6,"merge_commit_sha":"ffff","merged_at":"2026-09-23T11:00:00Z","head":{"sha":"x"},"base":{"ref":"main"}},{"number":7,"merge_commit_sha":"` + merge + `","merged_at":"2026-09-23T12:00:00Z","head":{"sha":"` + head + `"},"base":{"ref":"main"}}]`},
		"/repos/o/r/commits/3333333":                                        {`{"sha":"3333333333333333333333333333333333333333","commit":{"tree":{"sha":"tree-b"}}}`},
		"/repos/o/r/commits/3333333333333333333333333333333333333333/pulls": {`[{"number":8,"merge_commit_sha":"4444","merged_at":null,"head":{"sha":"3333333333333333333333333333333333333333"},"base":{"ref":"main"}}]`},
	}})
	defer srv.Close()
	svc := newCIServiceForRESTTest(srv)

	p, err := svc.GetMergeProvenance(context.Background(), "o", "r", "1111111")
	if err != nil {
		t.Fatalf("GetMergeProvenance: %v", err)
	}
	if p == nil || p.PRNumber != 7 || p.HeadSHA != head || !p.TreesMatch() || p.BaseRef != "main" || !p.MergedAt.Equal(mergedAt) {
		t.Fatalf("provenance = %+v, want PR #7 with matching trees", p)
	}

	// An open PR's head is not a merge commit: no provenance, old rule.
	if p, err := svc.GetMergeProvenance(context.Background(), "o", "r", "3333333"); err != nil || p != nil {
		t.Errorf("an unmerged PR head: provenance = %+v, err = %v; want nil, nil", p, err)
	}
	// An unknown commit is not an error either.
	if p, err := svc.GetMergeProvenance(context.Background(), "o", "r", "5555555"); err != nil || p != nil {
		t.Errorf("an unknown commit: provenance = %+v, err = %v; want nil, nil", p, err)
	}
}
