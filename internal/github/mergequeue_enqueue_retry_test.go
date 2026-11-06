package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// enqueueFake scripts enqueue outcomes and status reads for enqueueRetrying (#2729).
type enqueueFake struct {
	enqueueErrs []error // per attempt; nil = success; beyond the end repeats the last
	inQueue     bool    // what a status re-read reports
	enqueues    int
	reads       int
	sleeps      []time.Duration
}

func (f *enqueueFake) queue() *MergeQueue {
	q := NewMergeQueue(func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		if strings.Contains(query, "enqueuePullRequest") {
			i := f.enqueues
			f.enqueues++
			if i >= len(f.enqueueErrs) {
				i = len(f.enqueueErrs) - 1
			}
			if err := f.enqueueErrs[i]; err != nil {
				return nil, err
			}
			return []byte(`{"data":{"enqueuePullRequest":{}}}`), nil
		}
		f.reads++
		in := "false"
		if f.inQueue {
			in = "true"
		}
		return []byte(`{"data":{"node":{"id":"PR_1","number":42,"state":"OPEN","isMergeQueueEnabled":true,"isInMergeQueue":` + in + `}}}`), nil
	})
	q.sleep = func(_ context.Context, d time.Duration) error { f.sleeps = append(f.sleeps, d); return nil }
	return q
}

var openPR = MergeQueueStatus{PRID: "PR_1", Number: 42, State: "OPEN"}

func TestEnqueueRetrying_TransientOnceThenSuccess(t *testing.T) {
	f := &enqueueFake{enqueueErrs: []error{errors.New("HTTP 502 Bad Gateway"), nil}}
	if err := f.queue().enqueueRetrying(context.Background(), openPR); err != nil {
		t.Fatalf("enqueueRetrying: %v", err)
	}
	if f.enqueues != 2 || len(f.sleeps) != 1 || f.sleeps[0] != enqueueRetryBase {
		t.Errorf("enqueues=%d sleeps=%v, want 2 mutations after one %s backoff", f.enqueues, f.sleeps, enqueueRetryBase)
	}
}

func TestEnqueueRetrying_LandedBehindDroppedResponse_NoSecondMutation(t *testing.T) {
	f := &enqueueFake{enqueueErrs: []error{errors.New("read: connection reset by peer")}, inQueue: true}
	if err := f.queue().enqueueRetrying(context.Background(), openPR); err != nil {
		t.Fatalf("enqueueRetrying: %v", err)
	}
	if f.enqueues != 1 || f.reads != 1 {
		t.Errorf("enqueues=%d reads=%d, want 1 mutation and 1 status re-read", f.enqueues, f.reads)
	}
}

func TestEnqueueRetrying_NonTransientNotRetried(t *testing.T) {
	cause := errors.New("Pull request is in clean status")
	f := &enqueueFake{enqueueErrs: []error{cause}}
	err := f.queue().enqueueRetrying(context.Background(), openPR)
	if err == nil || !strings.Contains(err.Error(), cause.Error()) || f.enqueues != 1 {
		t.Fatalf("err=%v enqueues=%d, want the cause after 1 attempt", err, f.enqueues)
	}
}

func TestEnqueueRetrying_RateLimitNotRetried(t *testing.T) {
	f := &enqueueFake{enqueueErrs: []error{errors.New("API rate limit exceeded (503)")}}
	if err := f.queue().enqueueRetrying(context.Background(), openPR); err == nil || f.enqueues != 1 {
		t.Fatalf("err=%v enqueues=%d, want rate limit returned after 1 attempt", err, f.enqueues)
	}
}

func TestEnqueueRetrying_PersistentTransientExhausts(t *testing.T) {
	f := &enqueueFake{enqueueErrs: []error{errors.New("Something went wrong while executing your query")}}
	err := f.queue().enqueueRetrying(context.Background(), openPR)
	if err == nil || !strings.Contains(err.Error(), "failed after 4 attempts") ||
		!strings.Contains(err.Error(), "enqueue pull request:") {
		t.Fatalf("err = %v, want exhausted-retries error", err)
	}
	if f.enqueues != maxEnqueueRetries+1 {
		t.Errorf("enqueues = %d, want %d", f.enqueues, maxEnqueueRetries+1)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, d := range want {
		if i >= len(f.sleeps) || f.sleeps[i] != d {
			t.Fatalf("sleeps = %v, want %v", f.sleeps, want)
		}
	}
}
