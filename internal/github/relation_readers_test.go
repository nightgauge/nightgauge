package github_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// TestReadersFollowOnlyTheListsTheyUse drives each single-issue reader that
// uses some or none of an issue's relationship lists against a forge that
// fails every relationship page after the first. Every issue here has a list
// longer than its first page that the reader does not use, and every list a
// reader does use fits its first page. So a reader that follows only the
// lists it uses reads no later page and succeeds, and one that follows every
// list fails on a page it would discard, as each of these did when it read
// through GetIssue.
//
//   - #300 carries labels, has 3 blockers and blocks 7 issues; #310, with no
//     labels to sync to board fields, blocks 7 issues.
//   - #500 is an epic whose 3 sub-issues are closed; it blocks 7 issues, and
//     so does its sub-issue #501.
//   - #510 is an epic closed as completed with one open sub-issue; it blocks 7.
func TestReadersFollowOnlyTheListsTheyUse(t *testing.T) {
	issues := map[int]githubtest.Issue{
		300: {
			Labels:    []string{gh.LabelRefined, "status:ready"},
			BlockedBy: githubtest.Numbers(301, 3),
			Blocking:  githubtest.Numbers(401, 7),
		},
		310: {Blocking: githubtest.Numbers(411, 7)},
		500: {SubIssues: []int{501, 502, 503}, Blocking: githubtest.Numbers(601, 7)},
		501: {State: "CLOSED", Blocking: githubtest.Numbers(611, 7)},
		502: {State: "CLOSED"},
		503: {State: "CLOSED"},
		510: {
			State: "CLOSED", StateReason: "COMPLETED",
			SubIssues: []int{511, 512}, Blocking: githubtest.Numbers(621, 7),
		},
		511: {State: "OPEN"},
		512: {State: "CLOSED"},
	}
	const owner, repo = githubtest.Owner, githubtest.Repo

	readers := []struct {
		name string
		// run performs the read and checks what it returned.
		run           func(ctx context.Context, c *gh.Client) error
		wantMutations []string
	}{
		{"IssueService.HasLabel", func(ctx context.Context, c *gh.Client) error {
			has, err := gh.NewIssueService(c).HasLabel(ctx, owner, repo, 300, gh.LabelRefined)
			if err == nil && !has {
				err = fmt.Errorf("HasLabel = false, want true")
			}
			return err
		}, nil},
		{"IssueService.MarkRefined", func(ctx context.Context, c *gh.Client) error {
			return gh.NewIssueService(c).MarkRefined(ctx, owner, repo, 300)
		}, []string{"addLabelsToLabelable"}},
		{"IssueService.SyncStatusLabel", func(ctx context.Context, c *gh.Client) error {
			return gh.NewIssueService(c).SyncStatusLabel(ctx, owner, repo, 300, "done")
		}, []string{"removeLabelsFromLabelable", "addLabelsToLabelable"}},
		{"IssueService.GetEpicProgressByNumber", func(ctx context.Context, c *gh.Client) error {
			p, err := gh.NewIssueService(c).GetEpicProgressByNumber(ctx, owner, repo, 500)
			if err == nil && (p.Total != 3 || p.Closed != 3) {
				err = fmt.Errorf("progress = %d of %d closed, want 3 of 3", p.Closed, p.Total)
			}
			return err
		}, nil},
		{"ProjectService.AddIssueByNumber", func(ctx context.Context, c *gh.Client) error {
			_, err := gh.NewProjectService(c, owner, 7).AddIssueByNumber(ctx, owner, repo, 310)
			return err
		}, []string{"addProjectV2ItemById"}},
		{"EpicService.CheckCompletion", func(ctx context.Context, c *gh.Client) error {
			res, err := gh.NewEpicService(c).CheckCompletion(ctx, owner, repo, 500)
			if err == nil && (!res.Complete || res.Total != 3) {
				err = fmt.Errorf("completion = %+v, want 3 sub-issues, complete", res)
			}
			return err
		}, nil},
		{"EpicService.AutoCloseSingle", func(ctx context.Context, c *gh.Client) error {
			res, _ := gh.NewEpicService(c).AutoCloseSingle(ctx, gh.EpicRef{Owner: owner, Repo: repo, Number: 500}, 0)
			if res.Status != "closed" {
				return fmt.Errorf("auto-close = %s (%s): %s, want closed", res.Status, res.Reason, res.Error)
			}
			return nil
		}, []string{"closeIssue", "addComment"}},
		{"EpicService.CloseOrphanSubs", func(ctx context.Context, c *gh.Client) error {
			res, err := gh.NewEpicService(c).CloseOrphanSubs(ctx, owner, repo, 510, 0)
			if err == nil && res.Closed != 1 {
				err = fmt.Errorf("orphan close = %+v, want #511 closed", res)
			}
			return err
		}, []string{"closeIssue", "addComment"}},
		{"EpicService.PlanWaves", func(ctx context.Context, c *gh.Client) error {
			res, err := gh.NewEpicService(c).PlanWaves(ctx, owner, repo, []int{300, 500})
			if err == nil && res.SubIssueCount != 2 {
				err = fmt.Errorf("planned %d issues, want 2", res.SubIssueCount)
			}
			return err
		}, nil},
		{"EpicService.Validate", func(ctx context.Context, c *gh.Client) error {
			res, err := gh.NewEpicService(c).Validate(ctx, owner, repo, 500)
			if err == nil && res.TotalSubIssues != 3 {
				err = fmt.Errorf("validated %d sub-issues, want 3", res.TotalSubIssues)
			}
			return err
		}, nil},
	}
	for _, r := range readers {
		t.Run(r.name, func(t *testing.T) {
			forge := githubtest.New(t, issues)
			forge.RepoLabels = []string{"status:done"}

			if err := r.run(context.Background(), forge.Client()); err != nil {
				t.Fatalf("%s: %v", r.name, err)
			}
			if got := forge.Mutations(); !slices.Equal(got, r.wantMutations) {
				t.Errorf("mutations = %v, want %v", got, r.wantMutations)
			}
			if n := forge.FollowUps(); n != 0 {
				t.Errorf("read %d later page(s) of lists it does not use", n)
			}
		})
	}
}
