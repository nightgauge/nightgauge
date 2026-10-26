package orchestrator

import (
	"context"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestCheckEpicCompletion_BlockedIntegrationRaisesCardAndCloseResolvesIt
// (#2560): an epic left open because its branch could not reach main raises an
// Action Center card naming the conflicts; the close that follows a later
// successful integration retracts it. A pending integration raises nothing.
func TestCheckEpicCompletion_BlockedIntegrationRaisesCardAndCloseResolvesIt(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	result := hooks.PostMergeResult{IssueClosed: true, EpicNumber: 2085, EpicRepo: "o/r", Reason: "error",
		EpicReason: "integration_conflict",
		Integration: &gh.EpicIntegration{Outcome: gh.IntegrationConflict, Branch: "epic/2085-x",
			ConflictFiles: []string{"internal/x.go"}}}
	s.evaluatePostMergeFn = func(context.Context, hooks.IssueFetcher, hooks.IssueCloser, hooks.EpicAutoCloser,
		hooks.PRVerifier, hooks.BoardSyncer, hooks.PostMergeInput) hooks.PostMergeResult {
		return result
	}
	item := types.BoardItem{Repo: "o/r", Number: 2099}

	s.checkEpicCompletion(context.Background(), item, 0)
	reqs := openRunRequests(t, s)
	if len(reqs) != 1 || reqs[0].Producer != producerEpicIntegration || reqs[0].Context.Issue != 2085 {
		t.Fatalf("cards = %+v, want one epic-integration card for #2085", reqs)
	}
	assertSteerSet(t, reqs[0])

	result = hooks.PostMergeResult{IssueClosed: true, AutoClosed: true, EpicNumber: 2085, EpicRepo: "o/r"}
	s.checkEpicCompletion(context.Background(), item, 0)
	if reqs := openRunRequests(t, s); len(reqs) != 0 {
		t.Errorf("card still open after the epic closed: %+v", reqs)
	}

	result = hooks.PostMergeResult{IssueClosed: true, EpicNumber: 2085, EpicRepo: "o/r",
		Integration: &gh.EpicIntegration{Outcome: gh.IntegrationPending}}
	s.checkEpicCompletion(context.Background(), item, 0)
	if reqs := openRunRequests(t, s); len(reqs) != 0 {
		t.Errorf("pending integration raised a card: %+v", reqs)
	}
}
