package stages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// queueReviewGh drives a merge queue and serves a review comment that changes
// while the PR is queued (#2834). EnqueueAndWait calls opts.OnPoll once, as
// the real wait does on a poll that observes the entry, after swapping in the
// late comment; a hold ends the wait the way github.MergeQueue.Wait does.
type queueReviewGh struct {
	queueGh
	comment ReviewComment
	late    *ReviewComment
	held    bool
	// dequeueFails makes the first hold's dequeue fail: the wait then polls
	// again, as github.MergeQueue.Wait does, and the hook must hold again.
	dequeueFails bool
}

func (g *queueReviewGh) ReviewComment(context.Context, int) (ReviewComment, bool, error) {
	return g.comment, true, nil
}

func (g *queueReviewGh) EnqueueAndWait(ctx context.Context, st github.MergeQueueStatus, opts github.MergeQueueWaitOptions) (github.MergeQueueStatus, error) {
	g.queueCalls++
	if g.late != nil {
		g.comment = *g.late
	}
	if opts.OnPoll != nil {
		queued := st
		queued.InQueue = true
		if g.dequeueFails && opts.OnPoll(ctx, queued) == nil {
			return github.MergeQueueStatus{State: "MERGED"}, nil
		}
		if hold := opts.OnPoll(ctx, queued); hold != nil {
			g.held = true
			return queued, &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueHeld, Cause: hold}
		}
	}
	return github.MergeQueueStatus{State: "MERGED"}, nil
}

func newQueueReviewRunner(t *testing.T, g *queueReviewGh, f *fakeFiling) (*DeterministicRunner, string) {
	t.Helper()
	r, root := newReviewRunner(t, &reviewGh{}, f)
	r.gh = g
	r.mergeabilityPollInterval = 0
	return r, root
}

func queueReviewFixture(lateBody string) *queueReviewGh {
	enq := time.Now().Add(-time.Minute)
	clean := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"}
	return &queueReviewGh{
		queueGh: queueGh{
			fakeGh: fakeGh{preMerge: clean},
			status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true},
		},
		comment: ReviewComment{Body: reviewClean, UpdatedAt: enq},
		late:    &ReviewComment{Body: lateBody, UpdatedAt: time.Now()},
	}
}

// AC3: a hold finding posted after enqueue dequeues and punts for a fix round.
func TestQueueWait_LateHoldFindingDequeues(t *testing.T) {
	g := queueReviewFixture(review2757)
	r, root := newQueueReviewRunner(t, g, &fakeFiling{})
	res, err := r.Run(context.Background(), 100, "o/r", root)
	if err != nil {
		t.Fatal(err)
	}
	if !g.held || res.Path != PathPunt || !strings.HasPrefix(res.Reason, ReasonReviewFixNeeded) {
		t.Fatalf("held=%v res=%+v, want a %s punt after a dequeue", g.held, res, ReasonReviewFixNeeded)
	}
}

// AC4: a late clean review lets the queue merge.
func TestQueueWait_LateCleanReviewMerges(t *testing.T) {
	g := queueReviewFixture(reviewClean)
	r, root := newQueueReviewRunner(t, g, nil)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if g.held || res.Path != PathMerged || res.Reason != ReasonMergeQueueMerged {
		t.Fatalf("held=%v res=%+v, want merged via queue", g.held, res)
	}
}

// A late review with only file findings files them and still merges.
func TestQueueWait_LateFileFindingsFiledThenMerges(t *testing.T) {
	g := queueReviewFixture(reviewFileOnly)
	f := &fakeFiling{}
	r, root := newQueueReviewRunner(t, g, f)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if g.held || res.Path != PathMerged || len(f.records) != 1 {
		t.Fatalf("held=%v res=%+v filed=%d, want merged with 1 filed", g.held, res, len(f.records))
	}
}

// The comment the enqueue gate already read is not re-evaluated in the queue.
func TestQueueWait_UnchangedReviewNotReevaluated(t *testing.T) {
	g := queueReviewFixture("")
	g.late = nil
	f := &fakeFiling{}
	r, root := newQueueReviewRunner(t, g, f)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if g.held || res.Path != PathMerged {
		t.Fatalf("held=%v res=%+v", g.held, res)
	}
}

// A hold whose dequeue fails holds again on the next poll instead of merging.
func TestQueueWait_HoldSurvivesFailedDequeue(t *testing.T) {
	g := queueReviewFixture(review2757)
	g.dequeueFails = true
	r, root := newQueueReviewRunner(t, g, &fakeFiling{})
	res, err := r.Run(context.Background(), 100, "o/r", root)
	if err != nil {
		t.Fatal(err)
	}
	if !g.held || res.Path != PathPunt {
		t.Fatalf("held=%v res=%+v, want the second poll to hold", g.held, res)
	}
}
