package stages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// queueGh is a ghClient that can also drive a merge queue (#2214).
type queueGh struct {
	fakeGh
	status      github.MergeQueueStatus
	statusErr   error
	final       github.MergeQueueStatus
	waitErr     error
	statusCalls int
	queueCalls  int
	gotStatus   github.MergeQueueStatus
	gotOpts     github.MergeQueueWaitOptions
}

func (q *queueGh) MergeQueueStatus(_ context.Context, _ int) (github.MergeQueueStatus, error) {
	q.statusCalls++
	return q.status, q.statusErr
}

func (q *queueGh) EnqueueAndWait(_ context.Context, st github.MergeQueueStatus, opts github.MergeQueueWaitOptions) (github.MergeQueueStatus, error) {
	q.queueCalls++
	q.gotStatus = st
	q.gotOpts = opts
	return q.final, q.waitErr
}

var cleanOpen = PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", HeadRefName: "feat/x"}

func newQueueRunner(q *queueGh) *DeterministicRunner {
	r := newRunnerWith(q, 42)
	r.knowledgeConformance = nil
	r.steeringGate = nil
	return r
}

func TestRun_MergeQueueRequired_EnqueuesAndReportsMerged(t *testing.T) {
	q := &queueGh{
		fakeGh: fakeGh{preMerge: cleanOpen},
		status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"},
		final:  github.MergeQueueStatus{State: "MERGED", MergeCommitOID: "abc"},
	}
	r := newQueueRunner(q)
	r.SetMergeQueueWaitTimeout(2 * time.Hour)
	res, err := r.Run(context.Background(), 7, "", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathMerged || res.Reason != ReasonMergeQueueMerged || res.HeadRefName != "feat/x" {
		t.Errorf("res = %+v, want merged via queue", res)
	}
	if q.mergeCalls != 0 {
		t.Errorf("gh pr merge called %d times; a queue-protected base must be enqueued, not merged", q.mergeCalls)
	}
	if q.queueCalls != 1 || q.gotStatus.PRID != "PR_1" {
		t.Errorf("queueCalls=%d status=%+v", q.queueCalls, q.gotStatus)
	}
	if q.gotOpts.Timeout != 2*time.Hour {
		t.Errorf("queue wait timeout = %s, want the configured 2h", q.gotOpts.Timeout)
	}
}

func TestRun_MergeQueueDequeued_IsClassifiedFailureNotPunt(t *testing.T) {
	q := &queueGh{
		fakeGh: fakeGh{preMerge: cleanOpen},
		status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		final:  github.MergeQueueStatus{State: "OPEN"},
		waitErr: &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueDequeued,
			EntryHeadOID: "deadbeefcafe0000", FailedChecks: []string{"go-test", "lint"}},
	}
	res, err := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathRefused {
		t.Fatalf("Path = %s, want refused (no LLM fallback for a queue outcome)", res.Path)
	}
	if !strings.HasPrefix(res.Reason, ReasonMergeQueueFailed) || !strings.Contains(res.Reason, "go-test, lint") {
		t.Errorf("Reason = %q, want merge-queue-failed naming the failing checks", res.Reason)
	}
	if res.PRState != "OPEN" {
		t.Errorf("PRState = %q", res.PRState)
	}
}

func TestRun_MergeQueueTimeout_IsRefusedNotPunt(t *testing.T) {
	q := &queueGh{
		fakeGh:  fakeGh{preMerge: cleanOpen},
		status:  github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		waitErr: &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueTimeout},
	}
	res, _ := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonMergeQueueTimeout) {
		t.Errorf("res = %+v, want refused merge-queue-timeout", res)
	}
}

func TestRun_MergeQueueEnqueueFailed(t *testing.T) {
	q := &queueGh{
		fakeGh:  fakeGh{preMerge: cleanOpen},
		status:  github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		waitErr: errors.New("enqueue pull request: graphql: boom"),
	}
	res, _ := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonMergeQueueEnqueueFailed) {
		t.Errorf("res = %+v, want refused merge-queue-enqueue-failed", res)
	}
}

func TestRun_MergeQueueRateLimited_DefersLikeDirectPath(t *testing.T) {
	q := &queueGh{
		fakeGh:  fakeGh{preMerge: cleanOpen},
		status:  github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		waitErr: errors.New("API rate limit exceeded"),
	}
	res, _ := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if res.Reason != ReasonRateLimited {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonRateLimited)
	}
}

// A PR a previous attempt enqueued is still in the queue: resume the wait
// even though the PR view no longer reads CLEAN.
func TestRun_AlreadyQueued_ResumesWait(t *testing.T) {
	q := &queueGh{
		fakeGh: fakeGh{preMerge: PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED"}},
		status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, InQueue: true, EntryState: "AWAITING_CHECKS"},
		final:  github.MergeQueueStatus{State: "MERGED"},
	}
	res, _ := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if res.Path != PathMerged || q.queueCalls != 1 || q.mergeCalls != 0 {
		t.Errorf("res=%+v queueCalls=%d mergeCalls=%d", res, q.queueCalls, q.mergeCalls)
	}
}

// Non-queue branches keep today's direct merge, whether detection says "no
// queue" or detection itself fails.
func TestRun_NoMergeQueue_DirectMergeUnchanged(t *testing.T) {
	for name, q := range map[string]*queueGh{
		"no queue":         {status: github.MergeQueueStatus{State: "OPEN"}},
		"detection failed": {statusErr: errors.New("graphql: boom")},
	} {
		q.fakeGh = fakeGh{preMerge: cleanOpen, postMerge: PRViewSnapshot{State: "MERGED", HeadRefName: "feat/x"}}
		res, err := newQueueRunner(q).Run(context.Background(), 7, "", "")
		if err != nil {
			t.Fatalf("%s: Run: %v", name, err)
		}
		if res.Path != PathMerged || res.Reason != ReasonCleanMerged || q.mergeCalls != 1 || q.queueCalls != 0 {
			t.Errorf("%s: res=%+v mergeCalls=%d queueCalls=%d, want the unchanged direct merge", name, res, q.mergeCalls, q.queueCalls)
		}
		if q.statusCalls != 1 {
			t.Errorf("%s: detection ran %d times, want once per Run", name, q.statusCalls)
		}
	}
}

// Queue failures are never a punt to the LLM skill, and do not read as an
// EC timeout.
func TestRun_MergeQueueFailure_NotECTimeout(t *testing.T) {
	q := &queueGh{
		fakeGh:  fakeGh{preMerge: cleanOpen},
		status:  github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		waitErr: &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueUnmergeable},
	}
	res, _ := newQueueRunner(q).Run(context.Background(), 7, "", "")
	if res.Path == PathPunt || strings.Contains(res.Reason, ReasonMergeECTimeout) {
		t.Errorf("res = %+v, a queue failure must not punt or read as an EC timeout", res)
	}
}
