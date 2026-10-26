package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func TestResolveChangelogUnion(t *testing.T) {
	in := "# Changelog\n\n## [Unreleased]\n\n" +
		"<<<<<<< HEAD\n- epic entry\n- shared\n||||||| base\n- shared\n=======\n- main entry\n- shared\n>>>>>>> origin/main\n" +
		"\n## [1.0.0]\n"
	want := "# Changelog\n\n## [Unreleased]\n\n- main entry\n- shared\n- epic entry\n\n## [1.0.0]\n"
	got, err := ResolveChangelogUnion([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{"<<<<<<< a\nx\n", "x\n>>>>>>> b\n", "<<<<<<< a\n<<<<<<< b\n"} {
		if _, err := ResolveChangelogUnion([]byte(bad)); err == nil {
			t.Errorf("malformed %q: want error", bad)
		}
	}
}

func epicCommitFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", rel)
	gittest.Run(t, dir, "commit", "-q", "-m", "edit "+rel)
}

// fixture: main and epic/1-x both edit the given files after a shared seed.
func divergedFixture(t *testing.T, files ...string) *Service {
	t.Helper()
	clone, _ := gittest.ClonedRepo(t)
	for _, f := range files {
		epicCommitFile(t, clone, f, "## [Unreleased]\n\n- old\n")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "main")
	gittest.Run(t, clone, "checkout", "-q", "-b", "epic/1-x")
	epicCommitFile(t, clone, "feature.txt", "f\n")
	for _, f := range files {
		epicCommitFile(t, clone, f, "## [Unreleased]\n\n- epic\n- old\n")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "epic/1-x")
	gittest.Run(t, clone, "checkout", "-q", "main")
	for _, f := range files {
		epicCommitFile(t, clone, f, "## [Unreleased]\n\n- main\n- old\n")
	}
	gittest.Run(t, clone, "push", "-q", "origin", "main")
	svc, err := NewService(clone)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func worktreeCount(t *testing.T, s *Service) int {
	return strings.Count(gittest.Run(t, s.repoPath, "worktree", "list", "--porcelain"), "worktree ")
}

func TestMergeBaseIntoBranch_ChangelogOnlyConflictResolvedAndPushed(t *testing.T) {
	s := divergedFixture(t, "CHANGELOG.md", "packages/nightgauge/CHANGELOG.md")
	ahead, err := s.EpicBranchAhead("epic/1-x", "main")
	if err != nil || ahead != 3 {
		t.Fatalf("ahead = %d, %v; want 3", ahead, err)
	}
	res, err := s.MergeBaseIntoBranch("epic/1-x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != BaseMergeChangelogResolved {
		t.Fatalf("outcome = %+v", res)
	}
	gittest.Run(t, s.repoPath, "fetch", "-q", "origin")
	for _, f := range []string{"CHANGELOG.md", "packages/nightgauge/CHANGELOG.md"} {
		got := gittest.Run(t, s.repoPath, "show", "origin/epic/1-x:"+f)
		if got != "## [Unreleased]\n\n- main\n- epic\n- old" {
			t.Errorf("%s = %q", f, got)
		}
	}
	// main is now an ancestor of the epic branch: the PR can merge cleanly.
	gittest.Run(t, s.repoPath, "merge-base", "--is-ancestor", "origin/main", "origin/epic/1-x")
	if n := worktreeCount(t, s); n != 1 {
		t.Errorf("worktrees left behind: %d", n)
	}
}

func TestMergeBaseIntoBranch_OtherConflictAbortsWithoutPush(t *testing.T) {
	s := divergedFixture(t, "CHANGELOG.md", "main.go")
	before := gittest.Run(t, s.repoPath, "rev-parse", "origin/epic/1-x")
	res, err := s.MergeBaseIntoBranch("epic/1-x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != BaseMergeConflict || strings.Join(res.ConflictFiles, ",") != "CHANGELOG.md,main.go" {
		t.Fatalf("result = %+v", res)
	}
	gittest.Run(t, s.repoPath, "fetch", "-q", "origin")
	if after := gittest.Run(t, s.repoPath, "rev-parse", "origin/epic/1-x"); after != before {
		t.Errorf("origin branch moved: %s -> %s", before, after)
	}
	if n := worktreeCount(t, s); n != 1 {
		t.Errorf("worktrees left behind: %d", n)
	}
}

func TestMergeBaseIntoBranch_CleanAndUpToDate(t *testing.T) {
	s := divergedFixture(t)
	if _, err := s.EpicBranchAhead("epic/1-x", "main"); err != nil {
		t.Fatal(err)
	}
	res, err := s.MergeBaseIntoBranch("epic/1-x", "main")
	if err != nil || res.Outcome != BaseMergeUpToDate {
		t.Fatalf("epic already contains main: %+v %v", res, err)
	}
	epicCommitFile(t, s.repoPath, "other.txt", "o\n")
	gittest.Run(t, s.repoPath, "push", "-q", "origin", "main")
	gittest.Run(t, s.repoPath, "fetch", "-q", "origin")
	res, err = s.MergeBaseIntoBranch("epic/1-x", "main")
	if err != nil || res.Outcome != BaseMergeMerged {
		t.Fatalf("clean merge: %+v %v", res, err)
	}
}
