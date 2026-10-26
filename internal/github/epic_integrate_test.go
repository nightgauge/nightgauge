package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// noEpicBranchCheckout resolves every epic repository to a checkout whose
// origin has no epic branch, so closing is decided by sub-issue state alone.
func noEpicBranchCheckout(t *testing.T) EpicCheckoutResolver {
	t.Helper()
	clone, _ := gittest.ClonedRepo(t)
	return func(string) (*git.Service, error) { return git.NewService(clone) }
}

type fakeEpicPRs struct {
	created  int
	merged   int
	mergeErr error
	nodeID   string
}

func (f *fakeEpicPRs) CreateEpicPR(_ context.Context, _, _ string, n int, _, _, _ string) (*EpicPRResult, error) {
	f.created++
	node := f.nodeID
	if node == "" {
		node = "PR_node"
	}
	return &EpicPRResult{Action: "created", PRNumber: 900, PRURL: "https://example.invalid/pr/900", PRNodeID: node}, nil
}

func (f *fakeEpicPRs) MergeEpicPR(context.Context, string, string, string, string) error {
	f.merged++
	return f.mergeErr
}

func writeCommit(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", rel)
	gittest.Run(t, dir, "commit", "-q", "-m", msg)
}

// epicFixture builds #2085's shape: an epic branch with work main lacks, while
// main moved on. conflictFile, when set, is edited on both sides.
func epicFixture(t *testing.T, conflictFile string) string {
	t.Helper()
	clone, _ := gittest.ClonedRepo(t)
	writeCommit(t, clone, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- old entry\n", "changelog")
	if conflictFile != "" && conflictFile != "CHANGELOG.md" {
		writeCommit(t, clone, conflictFile, "base\n", "seed")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "main")
	gittest.Run(t, clone, "checkout", "-q", "-b", "epic/2085-thing")
	writeCommit(t, clone, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- epic entry\n- old entry\n", "epic changelog")
	writeCommit(t, clone, "feature.go", "package x\n", "feature")
	if conflictFile != "" && conflictFile != "CHANGELOG.md" {
		writeCommit(t, clone, conflictFile, "epic side\n", "epic edit")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "epic/2085-thing")
	gittest.Run(t, clone, "checkout", "-q", "main")
	writeCommit(t, clone, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- main entry\n- old entry\n", "main changelog")
	if conflictFile != "" && conflictFile != "CHANGELOG.md" {
		writeCommit(t, clone, conflictFile, "main side\n", "main edit")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "main")
	return clone
}

func TestIntegrateEpic_ChangelogOnlyConflictMergesAndPRMerges(t *testing.T) {
	clone := epicFixture(t, "")
	svc, _ := git.NewService(clone)
	prs := &fakeEpicPRs{}
	got := integrateEpicWith(context.Background(), svc, prs, EpicRef{Owner: "o", Repo: "r", Number: 2085}, "Thing", "main")
	if got.Outcome != IntegrationMerged || !got.AllowsClose() {
		t.Fatalf("outcome = %+v, want merged", got)
	}
	if prs.created != 1 || prs.merged != 1 {
		t.Errorf("PR created=%d merged=%d, want 1/1", prs.created, prs.merged)
	}
	// The resolution was pushed before the PR merged: both entries on the epic branch.
	// BranchCleanup deleted the branch afterwards, so read the commit via reflog-free
	// path: the remote branch is gone, which is the cleanup half of AC4.
	if out, err := gittest.Command(clone, "ls-remote", "--heads", "origin", "epic/2085-thing").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("epic branch still on origin after merge: %q %v", out, err)
	}
}

func TestIntegrateEpic_NonChangelogConflictLeavesEpicOpen(t *testing.T) {
	clone := epicFixture(t, "internal/x.go")
	svc, _ := git.NewService(clone)
	before := gittest.Run(t, clone, "ls-remote", "origin", "refs/heads/epic/2085-thing")
	prs := &fakeEpicPRs{}
	got := integrateEpicWith(context.Background(), svc, prs, EpicRef{Owner: "o", Repo: "r", Number: 2085}, "Thing", "main")
	if got.Outcome != IntegrationConflict || got.AllowsClose() || !got.Blocked() {
		t.Fatalf("outcome = %+v, want conflict", got)
	}
	if strings.Join(got.ConflictFiles, ",") != "CHANGELOG.md,internal/x.go" {
		t.Errorf("conflict files = %v", got.ConflictFiles)
	}
	if prs.created != 0 {
		t.Error("a PR was opened for an unmerged conflict")
	}
	if after := gittest.Run(t, clone, "ls-remote", "origin", "refs/heads/epic/2085-thing"); after != before {
		t.Errorf("origin epic branch moved on a conflict: %s -> %s", before, after)
	}
}

func TestIntegrateEpic_RedCIAndPendingKeepEpicOpen(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&MergeQueueError{PRNumber: 900, Kind: MergeQueueDequeued}, IntegrationCIFailed},
		{&MergeQueueError{PRNumber: 900, Kind: MergeQueueTimeout}, IntegrationPending},
	} {
		clone := epicFixture(t, "")
		svc, _ := git.NewService(clone)
		got := integrateEpicWith(context.Background(), svc, &fakeEpicPRs{mergeErr: tc.err}, EpicRef{Owner: "o", Repo: "r", Number: 2085}, "Thing", "main")
		if got.Outcome != tc.want || got.AllowsClose() {
			t.Errorf("merge err %v: outcome = %+v, want %s", tc.err, got, tc.want)
		}
	}
}

func TestIntegrateEpic_NoBranchAndAlreadyOnBaseAllowClose(t *testing.T) {
	clone, _ := gittest.ClonedRepo(t)
	svc, _ := git.NewService(clone)
	prs := &fakeEpicPRs{}
	if got := integrateEpicWith(context.Background(), svc, prs, EpicRef{Owner: "o", Repo: "r", Number: 7}, "T", "main"); got.Outcome != IntegrationNoBranch {
		t.Errorf("no branch: %+v", got)
	}
	gittest.Run(t, clone, "push", "-q", "origin", "main:refs/heads/epic/7-done")
	if got := integrateEpicWith(context.Background(), svc, prs, EpicRef{Owner: "o", Repo: "r", Number: 7}, "T", "main"); got.Outcome != IntegrationAlreadyOnBase {
		t.Errorf("contained branch: %+v", got)
	}
	if prs.created != 0 {
		t.Error("PR opened for a branch with nothing to integrate")
	}
}

func TestIntegrateEpic_NoResolverNeverCloses(t *testing.T) {
	got := NewEpicService(nil).integrateEpic(context.Background(), EpicRef{Owner: "o", Repo: "r", Number: 1}, "T")
	if got.Outcome != IntegrationNoCheckout || got.AllowsClose() {
		t.Errorf("outcome = %+v, want no_checkout", got)
	}
}
