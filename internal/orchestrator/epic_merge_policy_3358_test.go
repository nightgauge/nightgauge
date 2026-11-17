package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A default branch that refuses an epic MERGE gets no epic branch: the whole
// epic would otherwise land as one squash commit through the queue (#3358).
//
// Serial: sets the environment.
func TestEnsureEpicBranchForItem_EpicMergePolicyGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		check   gh.EpicMergeCheck
		created bool
		reason  string
	}{
		{"squash-only", gh.EpicMergeCheck{Method: "MERGE", Base: "main", Reason: "repository setting allow_merge_commit=false"}, false, "allow_merge_commit"},
		{"queue squash", gh.EpicMergeCheck{Method: "MERGE", Base: "main", Reason: "main merge_queue rule merge_method=SQUASH"}, false, "merge_queue"},
		{"allowed", gh.EpicMergeCheck{Allowed: true, Method: "MERGE", Base: "main"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "true")
			root := gitWorkspace(t)
			remote := filepath.Join(t.TempDir(), "origin.git")
			gitIn(t, root, "init", "--bare", remote)
			gitIn(t, root, "remote", "add", "origin", remote)
			gitIn(t, root, "push", "origin", "main")

			calls := 0
			s := &Scheduler{issueSvc: newMockIssueSvc(), epicMergeChecker: func(context.Context, string) (gh.EpicMergeCheck, error) {
				calls++
				return tc.check, nil
			}}
			for _, n := range []int{3001, 3002} {
				got := s.ensureEpicBranchForItem(context.Background(), root, types.BoardItem{
					Number: n, ParentNumber: 3000, ParentRepo: "Org/repo", ParentTitle: "Local Epic", Repo: "Org/repo",
				})
				if tc.created && got != "" {
					t.Fatalf("evidence = %q, want branch created", got)
				}
				if !tc.created && (!strings.Contains(got, "epic branch skipped") || !strings.Contains(got, tc.reason)) {
					t.Fatalf("evidence = %q, want a skip naming %s", got, tc.reason)
				}
			}
			if calls != 1 {
				t.Errorf("checker called %d times for one repository, want 1", calls)
			}
			if has := strings.Contains(remoteHeads(t, root), "refs/heads/epic/3000-local-epic"); has != tc.created {
				t.Errorf("epic branch on remote = %v, want %v", has, tc.created)
			}
		})
	}
}

// A probe error fails open (the branch is created as before) and is not
// cached, so the next item re-probes (#3358).
//
// Serial: sets the environment.
func TestEnsureEpicBranchForItem_EpicMergeProbeErrorFailsOpen(t *testing.T) {
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "true")
	root := gitWorkspace(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "--bare", remote)
	gitIn(t, root, "remote", "add", "origin", remote)
	gitIn(t, root, "push", "origin", "main")
	calls := 0
	s := &Scheduler{issueSvc: newMockIssueSvc(), epicMergeChecker: func(context.Context, string) (gh.EpicMergeCheck, error) {
		calls++
		return gh.EpicMergeCheck{}, errors.New("api down")
	}}
	for _, n := range []int{3001, 3002} {
		if got := s.ensureEpicBranchForItem(context.Background(), root, types.BoardItem{
			Number: n, ParentNumber: 3000, ParentRepo: "Org/repo", ParentTitle: "Local Epic", Repo: "Org/repo",
		}); got != "" {
			t.Fatalf("evidence = %q, want the branch created on a probe error", got)
		}
	}
	if calls != 2 {
		t.Errorf("checker called %d times, want 2 (errors are not cached)", calls)
	}
	if !strings.Contains(remoteHeads(t, root), "refs/heads/epic/3000-local-epic") {
		t.Error("probe error did not fail open")
	}
}
