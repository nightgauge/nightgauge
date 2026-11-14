package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// replanItem resolves the partial-delivery-close-refused card's `replan`
// option into the QueueItem the VerbQueueAdd handler enqueues
// (internal/ipc/attention.go): repo and issue come from the card's context.
func replanItem(t *testing.T) QueueItem {
	t.Helper()
	card := BuildPartialDeliveryCloseRefused("nightgauge/test", 3160, "run-1", 3141,
		[]string{partialRerunCriterion}, false)
	for _, opt := range card.Options {
		if opt.ID == "replan" {
			if opt.Verb != attention.VerbQueueAdd {
				t.Fatalf("replan verb = %q, want %q", opt.Verb, attention.VerbQueueAdd)
			}
			return QueueItem{Repo: card.Context.Repo, IssueNumber: card.Context.Issue}
		}
	}
	t.Fatal("card has no replan option")
	return QueueItem{}
}

// runReplan enqueues the replan item and runs the pipeline for the item it
// dequeues, returning the completed run's runtime.
func runReplan(t *testing.T, read func(context.Context, string, int) (hooks.PartialDeliveryStatus, error)) (*state.RuntimeState, *partialRerunRunner) {
	t.Helper()
	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = read
	s.noDiffCommenter = &recordingCommenter{}
	s.QueueAddItem(replanItem(t))
	queued := s.QueueList()
	if len(queued) != 1 {
		t.Fatalf("queue = %+v, want the one replan item", queued)
	}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })
	s.runPipeline(context.Background(), types.BoardItem{Number: queued[0].IssueNumber, Repo: queued[0].Repo,
		ID: "item-3160", NodeID: "I_3160"})
	if snap == nil {
		t.Fatal("the replan run never completed")
	}
	return snap, runner
}

// #3095 AC 1-2: the replan card carries no scope in queue.add, yet the run it
// starts is scoped, because run start re-reads the newest trusted comment.
// The extension path gets the same scope from `issue partial-delivery
// --json`'s prompt_blocks (PartialRerunScopeBlocks, asserted below).
func TestPartialReplan_RunIsScopedToTheRemainder(t *testing.T) {
	read := pendingRemainder(3141, partialRerunCriterion)
	snap, runner := runReplan(t, read)
	if got := snap.PartialRerunScope(); got == nil || got.PR != 3141 ||
		len(got.Criteria) != 1 || got.Criteria[0] != partialRerunCriterion {
		t.Fatalf("PartialRerunScope() = %+v, want PR #3141 with the remaining criterion", got)
	}
	prompt, ok := runner.prompt(state.StageFeaturePlanning)
	if !ok || !strings.Contains(prompt, partialRerunPromptHeading) || !strings.Contains(prompt, partialRerunCriterion) {
		t.Fatalf("feature-planning prompt does not carry the scope block and its criterion")
	}

	status, _ := read(context.Background(), "nightgauge/test", 3160)
	blocks := PartialRerunScopeBlocks(status.PR, status.Criteria)
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		ext := blocks[string(stage)]
		if !strings.Contains(ext, partialRerunPromptHeading) || !strings.Contains(ext, partialRerunCriterion) {
			t.Errorf("extension block for %s lacks the scope heading or criterion", stage)
		}
		// The extension does not stop a no-diff feature-dev before
		// feature-validate: its block must not say so.
		if strings.Contains(ext, "without feature-validate") {
			t.Errorf("extension block for %s promises the Go-only no-diff stop", stage)
		}
	}
	// Both paths splice partial_delivery into the planning context (#3163),
	// so both planning blocks say so.
	if !strings.Contains(blocks[string(state.StageFeaturePlanning)], "under `partial_delivery`") {
		t.Errorf("extension planning block does not name the spliced partial_delivery record")
	}
	if goDev := partialRerunPromptBlock(state.StageFeatureDev, snap.PartialRerunScope()); !strings.Contains(goDev, "without feature-validate") {
		t.Errorf("Go feature-dev block lost its no-diff stop sentence")
	}
}

// AC 4: a plain requeue with nothing pending yields no scope and no block.
func TestPartialReplan_NothingPendingIsUnscoped(t *testing.T) {
	none := func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{}, nil
	}
	snap, runner := runReplan(t, none)
	if got := snap.PartialRerunScope(); got != nil {
		t.Fatalf("PartialRerunScope() = %+v, want nil", got)
	}
	if prompt, _ := runner.prompt(state.StageFeaturePlanning); strings.Contains(prompt, partialRerunPromptHeading) {
		t.Fatal("unscoped feature-planning prompt carries the scope block")
	}
	if PartialRerunScopeBlocks(0, nil) != nil {
		t.Fatal("no remainder rendered extension prompt blocks")
	}
}

// AC 3: the card says what the replan run does, not an internal field it
// never sets.
func TestPartialReplan_CardDescribesTheRun(t *testing.T) {
	card := BuildPartialDeliveryCloseRefused("nightgauge/test", 3160, "run-1", 3141,
		[]string{partialRerunCriterion}, false)
	for _, opt := range card.Options {
		if opt.ID == "replan" && opt.Label != "Re-run: plan only the criteria PR #3141 left unmet" {
			t.Errorf("replan label = %q", opt.Label)
		}
	}
	if strings.Contains(card.Body, "focus_acs") ||
		!strings.Contains(card.Body, "re-reads the remaining criteria from PR #3141's partial-delivery comment") {
		t.Errorf("card body does not describe the re-run: %q", card.Body)
	}
}
