package hooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// These tests drive the hooks through the real issue, epic and project reads
// against githubtest's fake forge, which fails every relationship page after
// the first. A hook step that reads a list longer than its first page
// therefore fails, and one that reads only the lists it uses does not.

// TestEvaluateIssueDeps_LongListsItDoesNotUseCannotFailIt: #10 has 30
// sub-issues and blocks 7 issues, and its body depends on OPEN #20, which
// blocks 7 more. None of those lists is a dependency of #10, so the gate must
// read none of them: when it read them it failed, the skills' fallback turned
// the failure into "no open dependencies", and #10 was picked up while #20 was
// still open.
func TestEvaluateIssueDeps_LongListsItDoesNotUseCannotFailIt(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		10: {Body: "Depends on: #20", SubIssues: githubtest.Numbers(1000, 30), Blocking: githubtest.Numbers(2000, 7)},
		20: {Blocking: githubtest.Numbers(3000, 7)},
	})

	result, err := EvaluateIssueDeps(context.Background(), gh.NewIssueService(forge.Client()), githubtest.Owner, githubtest.Repo, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldBlock || result.OpenCount != 1 || result.OpenDependencies[0].Number != 20 {
		t.Fatalf("result = %+v, want #10 held by its open dependency #20", result)
	}
	if n := forge.FollowUps(); n != 0 {
		t.Errorf("the gate read %d later page(s) of lists it does not use", n)
	}
}

// TestEvaluateIssueDeps_LongBlockedByIsStillReadWhole: #10 has 11 blockers,
// more than the first page holds. The gate evaluates that list, so it must
// read past the first page, and a later page it cannot read fails the gate
// rather than leaving it to judge #10 from 5 of its 11 blockers.
func TestEvaluateIssueDeps_LongBlockedByIsStillReadWhole(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		10: {BlockedBy: githubtest.Numbers(1000, 11)},
	})

	result, err := EvaluateIssueDeps(context.Background(), gh.NewIssueService(forge.Client()), githubtest.Owner, githubtest.Repo, 10)
	if !errors.Is(err, gh.ErrConnectionTruncated) {
		t.Fatalf("err = %v (result %+v), want ErrConnectionTruncated", err, result)
	}
	if n := forge.FollowUps(); n != 1 {
		t.Errorf("later-page reads = %d, want 1: the blockedBy list must be read past its first page", n)
	}
}

// TestEvaluatePostMerge_LongListsCannotFailTheMerge: the merged issue blocks 7
// issues, and in one case is also an epic with 30 sub-issues. The hook uses
// neither list, only whether sub-issues exist, so it must read neither: when
// it did, the merge ended at issue_fetch_error with the issue left open, the
// board not moved to Done and the parent epic not rolled up.
func TestEvaluatePostMerge_LongListsCannotFailTheMerge(t *testing.T) {
	cases := []struct {
		name   string
		issue  githubtest.Issue
		isEpic bool
	}{
		{"blocks 7 issues", githubtest.Issue{Blocking: githubtest.Numbers(2000, 7)}, false},
		{"epic with 30 sub-issues that blocks 7",
			githubtest.Issue{SubIssues: githubtest.Numbers(1000, 30), Blocking: githubtest.Numbers(2000, 7)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forge := githubtest.New(t, map[int]githubtest.Issue{10: tc.issue})
			closer := &mockIssueCloser{}
			epics := &mockEpicAutoCloser{}

			result := EvaluatePostMerge(context.Background(), gh.NewIssueService(forge.Client()), closer, epics, nil, nil, PostMergeInput{
				IssueNumber:     10,
				RepositoryOwner: githubtest.Owner,
				RepositoryName:  githubtest.Repo,
			})

			if result.Failed || result.Reason != "no_parent" {
				t.Fatalf("result = %+v, want the merge reconciled (reason no_parent, not failed)", result)
			}
			if !closer.called || closer.nodeID != "I_10" {
				t.Errorf("CloseIssue called=%v node=%q, want the merged issue I_10 closed", closer.called, closer.nodeID)
			}
			if !result.IssueClosed {
				t.Error("IssueClosed = false, want true")
			}
			if epics.orphanCalled != tc.isEpic {
				t.Errorf("orphan-sub close ran = %v, want %v: whether the issue is an epic comes from its first page",
					epics.orphanCalled, tc.isEpic)
			}
			if n := forge.FollowUps(); n != 0 {
				t.Errorf("the hook read %d later page(s) of lists it does not use", n)
			}
		})
	}
}

// TestEvaluatePostMerge_EpicStepsReadOnlyTheSubIssueList runs the hook's epic
// steps through the real EpicService. Each uses only an epic's sub-issue list,
// its state and its node id, so an epic that blocks 7 issues must not fail
// them: when the steps read every list, merging the last sub-issue of a
// complete epic ended at auto_close_error (check_failed) and left the epic
// open, and an umbrella merge could not close its orphaned sub-issues.
func TestEvaluatePostMerge_EpicStepsReadOnlyTheSubIssueList(t *testing.T) {
	t.Run("parent epic rollup", func(t *testing.T) {
		// #11 is the last sub-issue of #10 to close; all three are closed.
		forge := githubtest.New(t, map[int]githubtest.Issue{
			10: {SubIssues: []int{11, 12, 13}, Blocking: githubtest.Numbers(2000, 7)},
			11: {State: "CLOSED", Parent: 10, Blocking: githubtest.Numbers(3000, 7)},
			12: {State: "CLOSED"},
			13: {State: "CLOSED"},
		})
		c := forge.Client()

		result := EvaluatePostMerge(context.Background(), gh.NewIssueService(c), &mockIssueCloser{}, gh.NewEpicService(c), nil, nil, PostMergeInput{
			IssueNumber:     11,
			RepositoryOwner: githubtest.Owner,
			RepositoryName:  githubtest.Repo,
		})

		if result.Failed || result.Reason != "closed" || !result.AutoClosed || result.EpicNumber != 10 {
			t.Fatalf("result = %+v, want epic #10 auto-closed", result)
		}
		if got, want := forge.Mutations(), []string{"closeIssue", "addComment"}; !slices.Equal(got, want) {
			t.Errorf("mutations = %v, want %v: the epic closed and commented on", got, want)
		}
		if n := forge.FollowUps(); n != 0 {
			t.Errorf("the rollup read %d later page(s) of lists it does not use", n)
		}
	})

	t.Run("umbrella merge closes orphaned sub-issues", func(t *testing.T) {
		// Epic #10 merged through one pull request and closed as completed,
		// leaving #12 open.
		forge := githubtest.New(t, map[int]githubtest.Issue{
			10: {State: "CLOSED", StateReason: "COMPLETED", SubIssues: []int{11, 12}, Blocking: githubtest.Numbers(2000, 7)},
			11: {State: "CLOSED"},
			12: {State: "OPEN"},
		})
		c := forge.Client()

		result := EvaluatePostMerge(context.Background(), gh.NewIssueService(c), &mockIssueCloser{}, gh.NewEpicService(c), nil, nil, PostMergeInput{
			IssueNumber:     10,
			RepositoryOwner: githubtest.Owner,
			RepositoryName:  githubtest.Repo,
		})

		if result.Failed || result.OrphanSubsClosed != 1 {
			t.Fatalf("result = %+v, want the open sub-issue #12 closed", result)
		}
		if n := forge.FollowUps(); n != 0 {
			t.Errorf("the orphan close read %d later page(s) of lists it does not use", n)
		}
	})
}

// boardWithoutRow is a project board the merged issue has no row on until it
// is added. Adding it goes through the real ProjectService.AddIssueByNumber.
type boardWithoutRow struct {
	projects *gh.ProjectService
	added    bool
}

func (b *boardWithoutRow) SyncStatus(_ context.Context, owner, repo string, number int, _ string) error {
	if !b.added {
		return fmt.Errorf("%w: issue #%d (%s/%s)", gh.ErrIssueNotOnBoard, number, owner, repo)
	}
	return nil
}

func (b *boardWithoutRow) AddIssueByNumber(ctx context.Context, owner, repo string, number int) (string, error) {
	id, err := b.projects.AddIssueByNumber(ctx, owner, repo, number)
	if err == nil {
		b.added = true
	}
	return id, err
}

// TestEvaluatePostMerge_BoardRepairReadsNoList: the merged issue blocks 7
// issues and has no board row. Adding the row uses only the issue's node id and
// labels, so it must read no list: when it read them, the repair failed and the
// hook reported the board sync failed, leaving the issue off the board.
func TestEvaluatePostMerge_BoardRepairReadsNoList(t *testing.T) {
	forge := githubtest.New(t, map[int]githubtest.Issue{
		10: {Blocking: githubtest.Numbers(2000, 7)},
	})
	c := forge.Client()
	board := &boardWithoutRow{projects: gh.NewProjectService(c, githubtest.Owner, 7)}

	result := EvaluatePostMerge(context.Background(), gh.NewIssueService(c), &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
		IssueNumber:     10,
		RepositoryOwner: githubtest.Owner,
		RepositoryName:  githubtest.Repo,
		ProjectNumber:   7,
	})

	if result.Failed || result.IssueDoneSync != BoardSyncRepaired {
		t.Fatalf("result = %+v, want the board row added and set to Done (repaired)", result)
	}
	if got, want := forge.Mutations(), []string{"addProjectV2ItemById"}; !slices.Equal(got, want) {
		t.Errorf("mutations = %v, want %v", got, want)
	}
	if n := forge.FollowUps(); n != 0 {
		t.Errorf("the board repair read %d later page(s) of lists it does not use", n)
	}
}
