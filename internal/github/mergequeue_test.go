package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// queueServer is a stub GraphQL endpoint for the merge-queue cycle (#2214).
// Status reads are served from statuses in order (the last one repeats).
type queueServer struct {
	mu         sync.Mutex
	statuses   []map[string]any
	reads      int
	enqueued   int
	enqueueErr string
	// readsAtEnqueue is the number of status reads served before the enqueue.
	readsAtEnqueue int
	directMerge    int
	checks         []map[string]any
}

func prNode(state string, queueEnabled, inQueue bool, entry map[string]any, mergeOID string) map[string]any {
	n := map[string]any{
		"id": "PR_1", "number": 42, "state": state, "baseRefName": "main",
		"isMergeQueueEnabled": queueEnabled, "isInMergeQueue": inQueue,
		"mergeCommit": nil, "mergeQueueEntry": nil,
		"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "headRefOid": "head1",
		"commits": rollup("SUCCESS"),
	}
	if entry != nil {
		n["mergeQueueEntry"] = entry
	}
	if mergeOID != "" {
		n["mergeCommit"] = map[string]any{"oid": mergeOID}
	}
	return n
}

func rollup(state string) map[string]any {
	if state == "" {
		return map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": nil}}}}
	}
	return map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": state}}}}}
}

// readiness returns an OPEN, queue-enabled, unqueued PR with the given
// mergeability and check rollup (#2584).
func readiness(mergeable, checks string) map[string]any {
	n := prNode("OPEN", true, false, nil, "")
	n["mergeable"] = mergeable
	n["commits"] = rollup(checks)
	if mergeable != "MERGEABLE" || checks != "SUCCESS" {
		n["mergeStateStatus"] = "BLOCKED"
	}
	return n
}

func entry(state, head string) map[string]any {
	return map[string]any{"state": state, "position": 1, "headCommit": map[string]any{"oid": head}}
}

func (qs *queueServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		qs.mu.Lock()
		defer qs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var data any
		switch {
		case strings.Contains(req.Query, "enqueuePullRequest"):
			qs.enqueued++
			qs.readsAtEnqueue = qs.reads
			if qs.enqueueErr != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]any{"message": qs.enqueueErr}}})
				return
			}
			data = map[string]any{"enqueuePullRequest": map[string]any{"mergeQueueEntry": map[string]any{"state": "QUEUED", "position": 1}}}
		case strings.Contains(req.Query, "mergePullRequest"):
			qs.directMerge++
			data = map[string]any{"mergePullRequest": map[string]any{"pullRequest": map[string]any{"mergeCommit": map[string]any{"oid": "direct123"}}}}
		case strings.Contains(req.Query, "contexts("):
			data = map[string]any{"node": map[string]any{"baseRepository": map[string]any{"object": map[string]any{
				"statusCheckRollup": map[string]any{"contexts": map[string]any{"nodes": qs.checks}}}}}}
		case strings.Contains(req.Query, "isMergeQueueEnabled"):
			i := qs.reads
			if i >= len(qs.statuses) {
				i = len(qs.statuses) - 1
			}
			qs.reads++
			data = map[string]any{"node": qs.statuses[i]}
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
}

func newQueuePRService(t *testing.T, qs *queueServer) *PRService {
	srv := httptest.NewServer(qs.handler(t))
	t.Cleanup(srv.Close)
	svc := NewPRService(NewClientWithURL("tok", srv.URL))
	svc.QueueWait = MergeQueueWaitOptions{Timeout: 2 * time.Second, PollInterval: time.Millisecond}
	return svc
}

func TestMergePRWithStrategy_QueueRequired_EnqueuesAndWaitsForMerge(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),                            // detection
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""), // queued
		prNode("MERGED", true, false, nil, "landed1"),
	}}
	sha, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	if err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if sha != "landed1" {
		t.Errorf("sha = %q, want the queue's merge commit landed1", sha)
	}
	if qs.enqueued != 1 || qs.directMerge != 0 {
		t.Errorf("enqueued=%d directMerge=%d, want 1/0", qs.enqueued, qs.directMerge)
	}
}

func TestMergePRWithStrategy_AlreadyQueued_DoesNotReEnqueue(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, true, entry("QUEUED", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}}
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if qs.enqueued != 0 {
		t.Errorf("enqueued=%d, want 0 for a PR already in the queue", qs.enqueued)
	}
}

func TestMergePRWithStrategy_Dequeued_ReturnsClassifiedFailureWithChecks(t *testing.T) {
	qs := &queueServer{
		statuses: []map[string]any{
			prNode("OPEN", true, false, nil, ""),
			prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
			prNode("OPEN", true, false, nil, ""), // the failed group removed it
		},
		checks: []map[string]any{
			{"__typename": "CheckRun", "name": "go-test", "status": "COMPLETED", "conclusion": "FAILURE"},
			{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS"},
			{"__typename": "StatusContext", "context": "ext/scan", "state": "ERROR"},
		},
	}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want *MergeQueueError", err)
	}
	if qe.Kind != MergeQueueDequeued || qe.EntryHeadOID != "mg1" {
		t.Errorf("kind=%s head=%s, want dequeued/mg1", qe.Kind, qe.EntryHeadOID)
	}
	if strings.Join(qe.FailedChecks, ",") != "ext/scan,go-test" {
		t.Errorf("FailedChecks = %v", qe.FailedChecks)
	}
	if !strings.Contains(err.Error(), "failed merge-group checks: ext/scan, go-test") {
		t.Errorf("error does not name the failing checks: %v", err)
	}
	if qs.directMerge != 0 {
		t.Errorf("a queue failure must never fall back to a direct merge")
	}
}

func TestMergePRWithStrategy_Unmergeable_Fails(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, entry("UNMERGEABLE", "mg2"), ""),
	}}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueUnmergeable {
		t.Fatalf("err = %v, want unmergeable MergeQueueError", err)
	}
}

func TestMergePRWithStrategy_QueueWaitTimesOut(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
	}}
	svc := newQueuePRService(t, qs)
	svc.QueueWait = MergeQueueWaitOptions{Timeout: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond}
	_, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueTimeout {
		t.Fatalf("err = %v, want timeout MergeQueueError", err)
	}
}

func TestMergePRWithStrategy_NoQueue_MergesDirectly(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{prNode("OPEN", false, false, nil, "")}}
	sha, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "MERGE")
	if err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if sha != "direct123" || qs.directMerge != 1 || qs.enqueued != 0 {
		t.Errorf("sha=%q direct=%d enqueued=%d, want the unchanged direct merge", sha, qs.directMerge, qs.enqueued)
	}
}

func TestClassifyMergeQueue(t *testing.T) {
	open := MergeQueueStatus{State: "OPEN"}
	queued := MergeQueueStatus{State: "OPEN", InQueue: true, EntryState: "AWAITING_CHECKS"}
	cases := []struct {
		name     string
		st       MergeQueueStatus
		seen     bool
		absent   int
		want     MergeQueueOutcome
		wantKind string
	}{
		{"merged", MergeQueueStatus{State: "MERGED"}, true, 0, MergeQueueMerged, ""},
		{"closed", MergeQueueStatus{State: "CLOSED"}, true, 0, MergeQueueFailed, MergeQueueClosed},
		{"queued", queued, true, 0, MergeQueuePending, ""},
		{"unmergeable", MergeQueueStatus{State: "OPEN", InQueue: true, EntryState: "UNMERGEABLE"}, true, 0, MergeQueueFailed, MergeQueueUnmergeable},
		{"left the queue after being seen", open, true, 1, MergeQueueFailed, MergeQueueDequeued},
		{"not yet visible inside the grace", open, false, 1, MergeQueuePending, ""},
		{"never visible past the grace", open, false, 3, MergeQueueFailed, MergeQueueDequeued},
	}
	for _, tc := range cases {
		got, kind := ClassifyMergeQueue(tc.st, tc.seen, tc.absent, 3)
		if got != tc.want || kind != tc.wantKind {
			t.Errorf("%s: got %v/%q, want %v/%q", tc.name, got, kind, tc.want, tc.wantKind)
		}
	}
}

// TestEvaluateMergedCommit_MergeGroupCommit pins #2055 for queued merges
// (#2214): the commit a merge queue lands is the merge-group commit, whose
// tree differs from the PR head's (it was rebuilt on a newer base). The PR
// run is then not evidence, and the verdict rests on the required checks the
// merge_group run reported on that very commit.
func TestEvaluateMergedCommit_MergeGroupCommit(t *testing.T) {
	required := []string{"build", "lint"}
	late := mergedAt.Add(time.Hour)
	groupProv := &MergeProvenance{PRNumber: 7, MergeSHA: "mg00000000", MergeTree: "tree-group", HeadSHA: "h000000000", HeadTree: "tree-head", BaseRef: "main", MergedAt: mergedAt}
	if groupProv.TreesMatch() {
		t.Fatal("fixture: a merge-group commit must have a tree differing from the PR head")
	}

	green := MergeEvidence{Provenance: groupProv,
		HeadChecks:    []CheckDetail{check("build", "COMPLETED", "FAILURE")}, // stale head result must not decide
		MergeChecks:   []CheckDetail{check("build", "COMPLETED", "SUCCESS"), check("lint", "COMPLETED", "SUCCESS")},
		RequiredNames: required, RequiredKnown: true, Now: late}
	if v, reasons := EvaluateMergedCommit(green); v != ChecksComplete {
		t.Errorf("merge-group commit carrying green required checks: got %v %v, want complete", v, reasons)
	}

	red := green
	red.MergeChecks = []CheckDetail{check("build", "COMPLETED", "FAILURE"), check("lint", "COMPLETED", "SUCCESS")}
	if v, _ := EvaluateMergedCommit(red); v != ChecksIncomplete {
		t.Errorf("merge-group commit with a failed required check: got %v, want incomplete", v)
	}
}

func TestMergePRWithStrategy_WaitsForMergeabilityThenEnqueues(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		readiness("UNKNOWN", ""),          // detection, right after the push
		readiness("MERGEABLE", "PENDING"), // computed, checks running
		readiness("MERGEABLE", "SUCCESS"), // ready
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}}
	svc := newQueuePRService(t, qs)
	svc.QueueWait.ReadyPollInterval = time.Millisecond
	sha, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	if err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if sha != "landed1" || qs.enqueued != 1 {
		t.Errorf("sha=%q enqueued=%d, want landed1/1", sha, qs.enqueued)
	}
	if qs.readsAtEnqueue != 3 {
		t.Errorf("enqueued after %d status reads, want 3: the enqueue must wait for the ready read", qs.readsAtEnqueue)
	}
}

func TestMergePRWithStrategy_NeverReady_NotReadyWithoutEnqueue(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{readiness("UNKNOWN", "")}}
	svc := newQueuePRService(t, qs)
	svc.QueueWait.ReadyTimeout = 20 * time.Millisecond
	svc.QueueWait.ReadyPollInterval = time.Millisecond
	_, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueNotReady {
		t.Fatalf("err = %v, want not_ready MergeQueueError", err)
	}
	if !strings.Contains(err.Error(), "mergeability not yet computed") {
		t.Errorf("error does not say what was pending: %v", err)
	}
	if qs.enqueued != 0 {
		t.Errorf("enqueued=%d, want 0 for a PR that never became ready", qs.enqueued)
	}
}

func TestMergePRWithStrategy_RedChecks_ChecksFailedWithoutEnqueue(t *testing.T) {
	qs := &queueServer{
		statuses: []map[string]any{readiness("MERGEABLE", "FAILURE")},
		checks: []map[string]any{
			{"__typename": "CheckRun", "name": "go-test", "status": "COMPLETED", "conclusion": "FAILURE"},
			{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS"},
		},
	}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueChecksFailed {
		t.Fatalf("err = %v, want checks_failed MergeQueueError", err)
	}
	if strings.Join(qe.FailedChecks, ",") != "go-test" || !strings.Contains(err.Error(), "failed checks: go-test") {
		t.Errorf("FailedChecks = %v, err = %v", qe.FailedChecks, err)
	}
	if qs.enqueued != 0 {
		t.Errorf("enqueued=%d, want 0 for a red PR", qs.enqueued)
	}
}

func TestMergePRWithStrategy_AlreadyQueued_SkipsReadinessWait(t *testing.T) {
	queued := prNode("OPEN", true, true, entry("QUEUED", "mg1"), "")
	queued["mergeable"] = "UNKNOWN"
	qs := &queueServer{statuses: []map[string]any{queued, prNode("MERGED", true, false, nil, "landed1")}}
	svc := newQueuePRService(t, qs)
	svc.QueueWait.ReadyTimeout = time.Nanosecond
	if _, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
}

func TestMergePRWithStrategy_EnqueueRejectedNotReady_ClassifiedNotReady(t *testing.T) {
	qs := &queueServer{
		statuses:   []map[string]any{readiness("MERGEABLE", "SUCCESS")},
		enqueueErr: "Pull request mergeability check has not yet completed",
	}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueNotReady {
		t.Fatalf("err = %v, want not_ready MergeQueueError", err)
	}
}

func TestClassifyEnqueueReadiness(t *testing.T) {
	cases := []struct {
		name, mergeable, state, checks string
		ready                          bool
		kind                           string
	}{
		{"unknown", "UNKNOWN", "UNKNOWN", "", false, ""},
		{"conflicting", "CONFLICTING", "DIRTY", "SUCCESS", false, MergeQueueUnmergeable},
		{"red", "MERGEABLE", "BLOCKED", "FAILURE", false, MergeQueueChecksFailed},
		{"errored", "MERGEABLE", "BLOCKED", "ERROR", false, MergeQueueChecksFailed},
		{"green", "MERGEABLE", "BLOCKED", "SUCCESS", true, ""},
		{"pending", "MERGEABLE", "BLOCKED", "PENDING", false, ""},
		{"expected", "MERGEABLE", "BLOCKED", "EXPECTED", false, ""},
		{"no checks yet", "MERGEABLE", "BLOCKED", "", false, ""},
		{"no checks, clean", "MERGEABLE", "CLEAN", "", true, ""},
	}
	for _, tc := range cases {
		ready, kind := ClassifyEnqueueReadiness(MergeQueueStatus{Mergeable: tc.mergeable, MergeStateStatus: tc.state, ChecksState: tc.checks})
		if ready != tc.ready || kind != tc.kind {
			t.Errorf("%s: got %v/%q, want %v/%q", tc.name, ready, kind, tc.ready, tc.kind)
		}
	}
}
