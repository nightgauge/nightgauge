package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

// epicReasonSuffix renders the epic service's discriminator when there is one.
func epicReasonSuffix(epicReason string) string {
	if epicReason == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", epicReason)
}

// errorSuffix renders the underlying error when there is one.
func errorSuffix(errText string) string {
	if errText == "" {
		return ""
	}
	return fmt.Sprintf(": %s", errText)
}

// raisePostMergeFailureCard puts a failed post-merge rollup in the Action
// Center (#1025).
//
// It lives in the CLI verb rather than in the scheduler's epic check because
// `Scheduler.SetAttention` is only ever called from the autonomous loop, so
// `s.attention` is nil on the extension and hand-merge paths — which are the
// paths AGENTS.md mandates and therefore the paths that actually fail. A rollup
// that fails with no card is how epic #206 sat open with every child closed.
//
// Deliberately event-shaped, not standing: this verb runs once per merge and
// nothing re-observes the condition, so a standing card would never
// auto-retract. Fail-open throughout — the hook is non-blocking, and a card
// that cannot be written must not turn a successful merge into an error.
func raisePostMergeFailureCard(workdir, owner, repo string, issueNumber int, result hooks.PostMergeResult) {
	root := config.MainCheckoutRoot(workdir)
	if root == "" {
		root = workdir
	}
	if root == "" {
		return
	}

	id, err := attention.NewID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Note: could not mint an attention id for the post-merge failure: %v\n", err)
		return
	}

	title := fmt.Sprintf("Post-merge rollup failed for %s/%s#%d", owner, repo, issueNumber)
	body := fmt.Sprintf(
		"`nightgauge hook post-merge` reported %s%s%s.\n\n"+
			"The hook is non-blocking and exits 0, so nothing else will surface this. "+
			"If an epic was involved (#%d), its rollup did not happen and the board is "+
			"out of date until it is re-run.",
		result.Reason, epicReasonSuffix(result.EpicReason), errorSuffix(result.Error),
		result.EpicNumber)

	req := attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: fmt.Sprintf("post-merge:%s/%s#%d", owner, repo, issueNumber),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityFYI,
		Title:          title,
		Body:           body,
		Producer:       "post-merge-hook",
		Context: attention.Context{
			Repo:  fmt.Sprintf("%s/%s", owner, repo),
			Issue: issueNumber,
		},
		DefaultAction: attention.ExpireNoop,
	}

	if _, _, raiseErr := attention.New(root).Raise(req); raiseErr != nil {
		fmt.Fprintf(os.Stderr, "Note: could not raise an attention card for the post-merge failure: %v\n", raiseErr)
	}
}
