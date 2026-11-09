package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// commitMessages are the PR's commit messages for the squash source
	// query (#2728); nil serves one ordinary commit.
	commitMessages []string
	// squashTitle/squashMessage are the repository's squash defaults served
	// by REST GET; patchStatus, when non-zero, fails the PATCH with it.
	squashTitle, squashMessage string
	patchStatus                int
	patched                    int
	mergeInput                 map[string]any
	// Reword fakes (#2988): crossRepo marks a fork head; createStatus fails
	// the REST commit create; refsErr fails updateRefs. created/refUpdate
	// record what was written and mutations every mutation name issued.
	crossRepo    bool
	createStatus int
	refsErr      string
	created      map[string]any
	refUpdate    map[string]any
	mutations    []string
	// rewordedHead is the head reported once updateRefs succeeds; headLag
	// status reads after it still report the old head (#2988).
	// pendingMessage replaces commitMessages once the lag is consumed.
	rewordedHead   string
	headLag        int
	pendingMessage string
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
		qs.mu.Lock()
		defer qs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/git/commits") && r.Method == http.MethodPost {
			if qs.createStatus != 0 {
				w.WriteHeader(qs.createStatus)
				_, _ = w.Write([]byte(`{"message":"Repository rule violations found"}`))
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&qs.created)
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": "reworded1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			if r.Method == http.MethodPatch {
				qs.patched++
				if qs.patchStatus != 0 {
					w.WriteHeader(qs.patchStatus)
					_, _ = w.Write([]byte(`{"message":"Must have admin rights"}`))
					return
				}
				qs.squashTitle, qs.squashMessage = SquashTitlePRTitle, SquashMessagePRBody
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "o/r",
				"squash_merge_commit_title": qs.squashTitle, "squash_merge_commit_message": qs.squashMessage})
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data any
		switch {
		case strings.Contains(req.Query, "prSquashSource"):
			msgs := qs.commitMessages
			if msgs == nil {
				msgs = []string{"fix: real change\n\nexplanation"}
			}
			nodes := make([]any, 0, len(msgs))
			parent := "base1"
			for i, m := range msgs {
				oid := fmt.Sprintf("c%d", i)
				nodes = append(nodes, map[string]any{"commit": map[string]any{"oid": oid, "message": m,
					"tree":    map[string]any{"oid": "tree" + oid},
					"parents": map[string]any{"nodes": []any{map[string]any{"oid": parent}}}}})
				parent = oid
			}
			data = map[string]any{"node": map[string]any{"number": 42, "title": "fix the thing",
				"headRefName": "feat/42-x", "headRefOid": parent, "isCrossRepository": qs.crossRepo,
				"baseRepository": map[string]any{"id": "R_1", "name": "r", "owner": map[string]any{"login": "o"}},
				"commits":        map[string]any{"nodes": nodes}}}
		case strings.Contains(req.Query, "updateRefs"):
			qs.mutations = append(qs.mutations, "updateRefs")
			qs.refUpdate = req.Variables
			if qs.refsErr != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]any{"message": qs.refsErr}}})
				return
			}
			qs.rewordedHead, _ = req.Variables["after"].(string)
			if m, ok := qs.created["message"].(string); ok {
				qs.pendingMessage = m
				if qs.headLag == 0 {
					qs.commitMessages = []string{m}
				}
			}
			data = map[string]any{"updateRefs": map[string]any{"clientMutationId": nil}}
		case strings.Contains(req.Query, "enqueuePullRequest"):
			qs.mutations = append(qs.mutations, "enqueuePullRequest")
			qs.enqueued++
			qs.readsAtEnqueue = qs.reads
			if qs.enqueueErr != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]any{"message": qs.enqueueErr}}})
				return
			}
			data = map[string]any{"enqueuePullRequest": map[string]any{"mergeQueueEntry": map[string]any{"state": "QUEUED", "position": 1}}}
		case strings.Contains(req.Query, "mergePullRequest"):
			qs.directMerge++
			qs.mergeInput, _ = req.Variables["input"].(map[string]any)
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
			node := qs.statuses[i]
			if qs.rewordedHead != "" {
				if qs.headLag > 0 {
					qs.headLag--
					if qs.headLag == 0 && qs.pendingMessage != "" {
						qs.commitMessages = []string{qs.pendingMessage}
					}
				} else {
					cp := make(map[string]any, len(node))
					for k, v := range node {
						cp[k] = v
					}
					cp["headRefOid"] = qs.rewordedHead
					node = cp
				}
			}
			data = map[string]any{"node": node}
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
	// 10s, not 2s: the test client paces requests at 1/s past a burst of 5,
	// and the squash-defaults check before an enqueue adds three (#2728).
	svc.QueueWait = MergeQueueWaitOptions{Timeout: 10 * time.Second, PollInterval: time.Millisecond}
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

// withMergeable sets the PR's own mergeability on a status node.
func withMergeable(n map[string]any, mergeable string) map[string]any {
	n["mergeable"] = mergeable
	return n
}

// behind is an UNMERGEABLE entry at position 2, blocked behind an entry ahead.
func behind(head string) map[string]any {
	e := entry("UNMERGEABLE", head)
	e["position"] = 2
	return e
}

func TestMergePRWithStrategy_Unmergeable_Fails(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		withMergeable(prNode("OPEN", true, true, entry("UNMERGEABLE", "mg2"), ""), "CONFLICTING"),
	}}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueUnmergeable {
		t.Fatalf("err = %v, want unmergeable MergeQueueError", err)
	}
}

// An UNMERGEABLE entry on a still-mergeable PR is waited out; once the PR
// reads CONFLICTING after removal the wait fails as unmergeable (#2611).
func TestMergePRWithStrategy_UnmergeableBehindEntry_ConflictAfterRemoval(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, behind("mg2"), ""),
		prNode("OPEN", true, true, behind("mg2"), ""),
		withMergeable(prNode("OPEN", true, false, nil, ""), "CONFLICTING"),
	}}
	_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	var qe *MergeQueueError
	if !errors.As(err, &qe) || qe.Kind != MergeQueueUnmergeable {
		t.Fatalf("err = %v, want unmergeable MergeQueueError", err)
	}
	if qs.enqueued != 1 {
		t.Errorf("enqueued=%d, want 1 (no re-enqueue of a conflicting PR)", qs.enqueued)
	}
}

// A blocked entry that GitHub removes while the PR stays MERGEABLE is
// re-enqueued once and then merges, never failing (#2611).
func TestMergePRWithStrategy_UnmergeableBehindEntry_ReEnqueuesAndMerges(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, behind("mg2"), ""),
		prNode("OPEN", true, false, nil, ""), // removed, still MERGEABLE
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg3"), ""),
		prNode("MERGED", true, false, nil, "landed2"),
	}}
	sha, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
	if err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if sha != "landed2" {
		t.Errorf("sha = %q, want landed2", sha)
	}
	if qs.enqueued != 2 {
		t.Errorf("enqueued=%d, want 2 (initial + one re-enqueue)", qs.enqueued)
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
		{"unmergeable conflicting", MergeQueueStatus{State: "OPEN", InQueue: true, EntryState: "UNMERGEABLE", Mergeable: "CONFLICTING"}, true, 0, MergeQueueFailed, MergeQueueUnmergeable},
		{"unmergeable behind entry", MergeQueueStatus{State: "OPEN", InQueue: true, EntryState: "UNMERGEABLE", Mergeable: "MERGEABLE"}, true, 0, MergeQueuePending, ""},
		{"unmergeable unknown", MergeQueueStatus{State: "OPEN", InQueue: true, EntryState: "UNMERGEABLE", Mergeable: "UNKNOWN"}, true, 0, MergeQueuePending, ""},
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

// onQueuedRecorder counts OnQueued calls and remembers the status reads
// served when the first one fired (#2770).
type onQueuedRecorder struct {
	calls       int
	readsAtCall int
}

func (r *onQueuedRecorder) hook(qs *queueServer) func(MergeQueueStatus) {
	return func(MergeQueueStatus) {
		r.calls++
		if r.calls == 1 {
			qs.mu.Lock()
			r.readsAtCall = qs.reads
			qs.mu.Unlock()
		}
	}
}

// TestOnQueued_FiresOnceForAQueuedPR: enqueued, then several polls see the
// entry; the hook fires once, at the enqueue, before the queue wait.
func TestOnQueued_FiresOnceForAQueuedPR(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}}
	svc := newQueuePRService(t, qs)
	rec := &onQueuedRecorder{}
	svc.QueueWait.OnQueued = rec.hook(qs)
	if _, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("OnQueued fired %d times, want once", rec.calls)
	}
	if rec.readsAtCall != qs.readsAtEnqueue {
		t.Errorf("OnQueued fired after %d status reads, want at the enqueue (%d)", rec.readsAtCall, qs.readsAtEnqueue)
	}
}

func TestOnQueued_FiresForAnAlreadyQueuedPR(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{
		prNode("OPEN", true, true, entry("QUEUED", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}}
	svc := newQueuePRService(t, qs)
	rec := &onQueuedRecorder{}
	svc.QueueWait.OnQueued = rec.hook(qs)
	if _, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("OnQueued fired %d times, want once", rec.calls)
	}
}

// TestOnQueued_NeverFiresForAPRThatDidNotReachTheQueue is AC 3: a PR closed
// during the readiness wait, one that never became ready, one with red
// checks, and one whose enqueue was rejected fire nothing.
func TestOnQueued_NeverFiresForAPRThatDidNotReachTheQueue(t *testing.T) {
	closed := readiness("MERGEABLE", "PENDING")
	closed["state"] = "CLOSED"
	cases := map[string]struct {
		statuses   []map[string]any
		enqueueErr string
	}{
		"closed during readiness": {statuses: []map[string]any{readiness("MERGEABLE", "PENDING"), closed}},
		"never ready":             {statuses: []map[string]any{readiness("UNKNOWN", "")}},
		"red checks":              {statuses: []map[string]any{readiness("MERGEABLE", "FAILURE")}},
		"enqueue rejected":        {statuses: []map[string]any{prNode("OPEN", true, false, nil, "")}, enqueueErr: "boom"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			qs := &queueServer{statuses: tc.statuses, enqueueErr: tc.enqueueErr}
			svc := newQueuePRService(t, qs)
			svc.QueueWait.ReadyTimeout = 20 * time.Millisecond
			svc.QueueWait.ReadyPollInterval = time.Millisecond
			rec := &onQueuedRecorder{}
			svc.QueueWait.OnQueued = rec.hook(qs)
			_, _ = svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
			if rec.calls != 0 {
				t.Fatalf("OnQueued fired %d times for a PR that never reached the queue", rec.calls)
			}
		})
	}
}

// wipMessage is a WIP-preservation commit as preserveWorkInProgress writes it.
const wipMessage = "wip(feature-dev): preserve work in progress\n\nRefs: #42\n\nNightgauge-WIP: feature-dev"

func TestMergePRWithStrategy_SoleWIPCommit_SquashesWithPRTitle(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{prNode("OPEN", false, false, nil, "")},
		commitMessages: []string{wipMessage}}
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if got := qs.mergeInput["commitHeadline"]; got != "fix the thing (#42)" {
		t.Errorf("commitHeadline = %v, want the PR title", got)
	}
	body, ok := qs.mergeInput["commitBody"].(string)
	if !ok || strings.Contains(body, "Nightgauge-WIP") || strings.Contains(body, "wip(") {
		t.Errorf("commitBody = %q, want no WIP commit content", body)
	}
}

func TestMergePRWithStrategy_Merge_NoHeadline(t *testing.T) {
	qs := &queueServer{statuses: []map[string]any{prNode("OPEN", false, false, nil, "")}}
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "MERGE"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if _, set := qs.mergeInput["commitHeadline"]; set {
		t.Errorf("MERGE strategy sent commitHeadline: %v", qs.mergeInput)
	}
}

func queuedToMerged() []map[string]any {
	return []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}
}

func TestMergeQueue_DefaultsAlreadySet_EnqueuesWithoutPatch(t *testing.T) {
	qs := &queueServer{statuses: queuedToMerged(), commitMessages: []string{wipMessage},
		squashTitle: SquashTitlePRTitle, squashMessage: SquashMessagePRBody}
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if qs.patched != 0 || qs.enqueued != 1 {
		t.Errorf("patched=%d enqueued=%d, want 0/1", qs.patched, qs.enqueued)
	}
}

func TestMergeQueue_DefaultsNotPRTitled_NoWIPCommit_EnqueuesWithoutPatch(t *testing.T) {
	qs := &queueServer{statuses: queuedToMerged(),
		squashTitle: "COMMIT_OR_PR_TITLE", squashMessage: "COMMIT_MESSAGES"}
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if qs.enqueued != 1 || qs.patched != 0 {
		t.Errorf("enqueued=%d patched=%d, want 1/0", qs.enqueued, qs.patched)
	}
}

// TestMergeabilityWaitBudget pins the 2+4+8+16x7 = 126 s backoff (#2730, #2835).
func TestMergeabilityWaitBudget(t *testing.T) {
	if got, want := MergeabilityWaitBudget(), 126*time.Second; got != want {
		t.Fatalf("MergeabilityWaitBudget() = %v, want %v", got, want)
	}
}
