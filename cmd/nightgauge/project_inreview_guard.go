package main

import (
	"context"
	"errors"
	"fmt"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// errInReviewWithoutOpenPR is the refusal `project move-status`/`sync-status`
// return for "in-review" when no open PR backs the issue. Its text is the
// contract the VS Code extension's failure writers match to tell "refused, as
// designed" from a failed write.
var errInReviewWithoutOpenPR = errors.New("refusing In review: no open PR whose head branch names the issue")

// requireOpenPRForInReview is the CLI's "In review" guard (#1496). Every writer
// of "In review" derives it from the same evidence, an open PR of the issue's
// own (gh.OwnOpenPRForIssue), so two writers racing after a failed run cannot
// disagree: with no open PR only the scheduler's revert writes anything. Any
// other status passes unchecked. An unreadable PR list refuses too: it is not
// evidence that a PR exists.
func requireOpenPRForInReview(ctx context.Context, client *gh.Client, owner, repo string, issue int, status string) error {
	if status != "in-review" {
		return nil
	}
	_, found, err := gh.NewPRService(client).OwnOpenPRForIssue(ctx, owner, repo, issue)
	if err != nil {
		return fmt.Errorf("refusing In review for %s/%s#%d: could not list open PRs: %w", owner, repo, issue, err)
	}
	if !found {
		return fmt.Errorf("%w (%s/%s#%d)", errInReviewWithoutOpenPR, owner, repo, issue)
	}
	return nil
}
