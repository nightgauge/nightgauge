package github_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// TestGetItemWithoutRelations reads one board item's own fields against a
// forge that fails every relationship page after the first (#1707). #700 has
// more sub-issues, blockers and blocked issues than any first page, so the
// read that completes those lists (GetItem) fails, and the read that selects
// none of them (GetItemWithoutRelations, behind `forge project field-get`)
// succeeds without asking for a later page.
func TestGetItemWithoutRelations(t *testing.T) {
	ctx := context.Background()
	const owner, repo = githubtest.Owner, githubtest.Repo
	f := githubtest.New(t, map[int]githubtest.Issue{
		700: {
			Status:    "In review",
			SubIssues: githubtest.Numbers(1001, 60),
			BlockedBy: githubtest.Numbers(301, 11),
			Blocking:  githubtest.Numbers(401, 7),
		},
		701: {},
	})
	board := gh.NewBoardService(f.Client(), owner, 1)

	item, err := board.GetItemWithoutRelations(ctx, owner, repo, 700)
	if err != nil {
		t.Fatalf("GetItemWithoutRelations: %v", err)
	}
	if item.Number != 700 || item.Repo != owner+"/"+repo || item.Status != "In review" || item.State != "OPEN" {
		t.Fatalf("item = %+v, want #700 OPEN in review", item)
	}
	if n := len(item.SubIssues) + len(item.BlockedBy) + len(item.Blocking); n != 0 {
		t.Errorf("returned %d relationships; the read names none", n)
	}
	if !item.IsEpic {
		t.Error("IsEpic = false, want true from the first page of sub-issues")
	}
	if n := f.FollowUps(); n != 0 {
		t.Fatalf("relationship pages requested = %d, want 0", n)
	}

	if _, err := board.GetItemWithoutRelations(ctx, owner, repo, 701); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("an issue that is not on the board: err = %v, want forge.ErrNotFound", err)
	}

	// The same item through the read that completes its lists cannot be read.
	if _, err := board.GetItem(ctx, owner, repo, 700); err == nil {
		t.Fatal("GetItem succeeded; the fake refuses every later relationship page, so the test proves nothing")
	}
	if f.FollowUps() == 0 {
		t.Fatal("GetItem asked for no later page; the fixture's lists do not exceed their first pages")
	}
}
