package stages

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/queuewait"
)

// TestRun_MergeQueueWaitIsRecordedForTheIssue: the deterministic runner's
// queue wait writes the same record `pr merge --issue` does (#2770), opened
// when the PR reaches the queue and closed when the wait returns.
func TestRun_MergeQueueWaitIsRecordedForTheIssue(t *testing.T) {
	root := layouttest.Repo(t)
	q := &queueGh{
		fakeGh:     fakeGh{preMerge: cleanOpen},
		status:     github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"},
		final:      github.MergeQueueStatus{State: "MERGED", MergeCommitOID: "abc"},
		reachQueue: true,
	}
	res, err := newQueueRunner(q).Run(context.Background(), 7, "", root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("res = %+v, want merged", res)
	}
	rec, err := queuewait.Load(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Intervals) != 1 || rec.Intervals[0].End == nil {
		t.Fatalf("intervals = %+v, want one closed interval", rec.Intervals)
	}
}

// TestRun_MergeQueueNeverReached_RecordsNothing: a wait that never saw the
// PR in the queue opens no interval.
func TestRun_MergeQueueNeverReached_RecordsNothing(t *testing.T) {
	root := layouttest.Repo(t)
	q := &queueGh{
		fakeGh:  fakeGh{preMerge: cleanOpen},
		status:  github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"},
		final:   github.MergeQueueStatus{State: "OPEN"},
		waitErr: &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueNotReady},
	}
	if _, err := newQueueRunner(q).Run(context.Background(), 7, "", root); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec, _ := queuewait.Load(root, 7); len(rec.Intervals) != 0 {
		t.Fatalf("intervals = %+v, want none", rec.Intervals)
	}
}
