package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

type stubIssueFetcher struct {
	issue *types.Issue
	err   error
}

func (s stubIssueFetcher) GetIssueWithRelations(context.Context, string, string, int, gh.IssueRelations) (*types.Issue, error) {
	return s.issue, s.err
}

func TestBranchNameForIssue_ComposesFromLabelsAndTitle(t *testing.T) {
	f := stubIssueFetcher{issue: &types.Issue{Number: 1911, Title: "fix(outcome): model still fails", Labels: []string{"type:bug"}}}
	_, name, err := branchNameForIssue(context.Background(), f, "o", "r", 1911)
	if err != nil {
		t.Fatalf("branchNameForIssue: %v", err)
	}
	if name != "fix/1911-fix-outcome-model-still-fails" {
		t.Errorf("name = %q", name)
	}
}

// The #1915 incident: labels arrived, the title did not, and the branch was
// named `fix/1911-`. branch-create must refuse and name the issue.
func TestBranchNameForIssue_EmptyTitleIsRefused(t *testing.T) {
	f := stubIssueFetcher{issue: &types.Issue{Number: 1911, Labels: []string{"type:bug"}}}
	fetched, name, err := branchNameForIssue(context.Background(), f, "o", "r", 1911)
	if err == nil {
		t.Fatalf("branchNameForIssue returned %q for an issue with no title, want an error", name)
	}
	if name != "" || fetched != nil {
		t.Errorf("a refused name still returned (%q, %v)", name, fetched)
	}
	if !strings.Contains(err.Error(), "o/r#1911") {
		t.Errorf("error does not name the issue: %v", err)
	}
}

func TestBranchNameForIssue_FetchErrorIsReturned(t *testing.T) {
	want := errors.New("fetch issue #1911: incomplete response from GitHub")
	_, _, err := branchNameForIssue(context.Background(), stubIssueFetcher{err: want}, "o", "r", 1911)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestEpicBranchParentFor_CrossRepoParent: branch-create bases a sub-issue
// on its parent's epic branch only when the parent lives in the sub-issue's
// repository. For a parent elsewhere, epic/<N>-* here is the branch of this
// repository's own #N, and the epic's title is not this repository's #N's
// either (#2377).
func TestEpicBranchParentFor_CrossRepoParent(t *testing.T) {
	cases := []struct {
		name  string
		issue types.Issue
		want  int
	}{
		{"parent in another repository",
			types.Issue{Repo: "example-org/app", ParentIssueNumber: 20, ParentIssueRepo: "example-org/platform"}, 0},
		{"parent in the same repository",
			types.Issue{Repo: "example-org/app", ParentIssueNumber: 20, ParentIssueRepo: "example-org/app"}, 20},
		{"issue repository from the flags when the read left it empty",
			types.Issue{ParentIssueNumber: 20, ParentIssueRepo: "example-org/app"}, 20},
		{"no parent", types.Issue{Repo: "example-org/app"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issue := c.issue
			if got := epicBranchParentFor(&issue, "example-org", "app"); got != c.want {
				t.Errorf("epicBranchParentFor = %d, want %d", got, c.want)
			}
		})
	}
}

// epicBranchCheckout is a checkout of example-org/app on main whose origin
// already holds epic/20-unrelated-issue: the epic branch of this repository's
// own #20.
func epicBranchCheckout(t *testing.T) (*gitpkg.Service, string) {
	t.Helper()
	root := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "seed")
	remote := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, root, "init", "--bare", remote)
	gittest.Run(t, root, "remote", "add", "origin", remote)
	gittest.Run(t, root, "push", "-q", "origin", "main")
	gittest.Run(t, root, "push", "-q", "origin", "main:refs/heads/epic/20-unrelated-issue")
	svc, err := gitpkg.NewService(root)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, root
}

// TestEnsureBranchForIssue_EpicBranchOnlyForAParentInThisRepository guards
// the `git branch-create` call site itself (#2377), not only the helper it
// asks. A sub-issue of example-org/platform#20 must branch from main, although
// this repository has an epic/20-* branch for its own #20: no epic branch is
// created, no epic title is read, and --json reports parent_issue null. A
// parent in this repository still gets its epic branch as the base.
func TestEnsureBranchForIssue_EpicBranchOnlyForAParentInThisRepository(t *testing.T) {
	t.Run("parent in another repository", func(t *testing.T) {
		svc, root := epicBranchCheckout(t)
		noTitle := func() (issueFetcher, error) {
			return nil, errors.New("an epic title was read for a parent in another repository")
		}
		issue := &types.Issue{Number: 21, Title: "Sub issue", Repo: "example-org/app",
			ParentIssueNumber: 20, ParentIssueRepo: "example-org/platform"}

		res, err := ensureBranchForIssue(context.Background(), svc, noTitle, "example-org", "app", issue, "feat/21-sub-issue")
		if err != nil {
			t.Fatalf("ensureBranchForIssue: %v", err)
		}
		if res.BaseBranch != "main" {
			t.Errorf("base branch = %q, want main: epic/20-* here is this repository's #20", res.BaseBranch)
		}
		payload := branchCreatePayload(res)
		if payload["parent_issue"] != nil || payload["epic_branch"] != nil {
			t.Errorf("parent_issue = %v, epic_branch = %v, want both null", payload["parent_issue"], payload["epic_branch"])
		}
		if heads := gittest.Run(t, root, "ls-remote", "--heads", "origin"); strings.Count(heads, "refs/heads/epic/") != 1 {
			t.Errorf("remote heads:\n%s\nwant only epic/20-unrelated-issue", heads)
		}
		if local := gittest.Run(t, root, "branch", "--list", "epic/*"); local != "" {
			t.Errorf("local epic branches %q, want none", local)
		}
	})

	t.Run("parent in this repository", func(t *testing.T) {
		svc, root := epicBranchCheckout(t)
		titles := func() (issueFetcher, error) {
			return stubIssueFetcher{issue: &types.Issue{Number: 30, Title: "Local epic"}}, nil
		}
		issue := &types.Issue{Number: 31, Title: "Sub issue", Repo: "example-org/app",
			ParentIssueNumber: 30, ParentIssueRepo: "example-org/app"}

		res, err := ensureBranchForIssue(context.Background(), svc, titles, "example-org", "app", issue, "feat/31-sub-issue")
		if err != nil {
			t.Fatalf("ensureBranchForIssue: %v", err)
		}
		if res.BaseBranch != "epic/30-local-epic" {
			t.Errorf("base branch = %q, want epic/30-local-epic", res.BaseBranch)
		}
		payload := branchCreatePayload(res)
		if payload["parent_issue"] != 30 || payload["epic_branch"] != "epic/30-local-epic" {
			t.Errorf("parent_issue = %v, epic_branch = %v, want 30 and epic/30-local-epic",
				payload["parent_issue"], payload["epic_branch"])
		}
		if heads := gittest.Run(t, root, "ls-remote", "--heads", "origin"); !strings.Contains(heads, "refs/heads/epic/30-local-epic") {
			t.Errorf("remote heads:\n%s\nwant epic/30-local-epic pushed", heads)
		}
	})
}

// TestEpicCreateBranch_RefusesACheckoutOfAnotherRepository (#2388): the
// command creates and pushes epic/<N>-* in the checkout it runs in, so from a
// checkout of example-org/app it refuses --repo example-org/platform before it
// reads the epic or creates a branch.
func TestEpicCreateBranch_RefusesACheckoutOfAnotherRepository(t *testing.T) {
	root := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "seed")
	gittest.Run(t, root, "remote", "add", "origin", "https://github.com/example-org/app.git")
	t.Chdir(root)

	cmd := epicCreateBranchCmd()
	cmd.SetArgs([]string{"20", "--owner", "example-org", "--repo", "example-org/platform", "--json"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "a checkout of example-org/app, not example-org/platform") {
		t.Fatalf("err = %v, want a refusal naming the checkout's repository", err)
	}
	if local := gittest.Run(t, root, "branch", "--list", "epic/*"); local != "" {
		t.Errorf("local epic branches %q, want none", local)
	}
}
