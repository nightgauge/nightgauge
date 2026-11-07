package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// errInReviewWithoutOpenPR is the refusal `project move-status`/`sync-status`
// return for "in-review" when no open PR backs the issue. Its text is the
// contract the VS Code extension's failure writers match to tell "refused, as
// designed" from a failed write.
var errInReviewWithoutOpenPR = errors.New("refusing In review: no open PR whose head branch names the issue")

// errStatusYielded is the refusal `sync-status --only-from` returns when another
// writer has already moved the issue. Its "yielding <status>" text is the
// contract the extension's failure parks match (#2706).
var errStatusYielded = errors.New("the current status is not the --only-from status")

// boardItemReader reads one item's current board status.
type boardItemReader interface {
	GetItemWithoutRelations(ctx context.Context, owner, repo string, issueNumber int) (*types.BoardItem, error)
}

// requireCurrentStatus is the `--only-from` guard (#2706): the write happens
// only when the issue still holds onlyFrom. A failure park uses it to yield to
// the scheduler's Ready, whichever of the two lands first. An unreadable status
// also writes nothing: it is not evidence the status matches. An empty onlyFrom
// passes unchecked. The board has no compare-and-set, so a write landing between
// this read and the caller's write still wins.
func requireCurrentStatus(ctx context.Context, reader boardItemReader, owner, repo string, issue int, status, onlyFrom string) error {
	if onlyFrom == "" {
		return nil
	}
	item, err := reader.GetItemWithoutRelations(ctx, owner, repo, issue)
	if err != nil {
		return fmt.Errorf("not writing %s for %s/%s#%d: could not read its current status: %w", gh.StatusLabel(status), owner, repo, issue, err)
	}
	want := gh.StatusLabel(onlyFrom)
	if !strings.EqualFold(string(item.Status), want) {
		return fmt.Errorf("yielding %s for %s/%s#%d: it is %q, not %q: %w", gh.StatusLabel(status), owner, repo, issue, item.Status, want, errStatusYielded)
	}
	return nil
}

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
