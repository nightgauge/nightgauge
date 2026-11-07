package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

// isolateGitIdentity gives the git processes RefreshResumedBase spawns an
// identity and no user config: its merge commits run through plain exec, and
// the Linux runner refuses a commit with no identity where macOS only warns.
func isolateGitIdentity(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "t",
		"GIT_AUTHOR_EMAIL":    "t@example.com",
		"GIT_COMMITTER_NAME":  "t",
		"GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
}

// baseRefreshRepos returns an upstream on main and a clone of it on issue
// 42's branch, level with origin/main.
func baseRefreshRepos(t *testing.T) (upstream, clone string) {
	t.Helper()
	isolateGitIdentity(t)
	upstream = t.TempDir()
	gitT(t, upstream, "init", "-q", "-b", "main")
	writeFileT(t, filepath.Join(upstream, "a.txt"), "a\n")
	writeFileT(t, filepath.Join(upstream, "CHANGELOG.md"),
		"# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- base entry\n")
	gitT(t, upstream, "add", ".")
	gitT(t, upstream, "commit", "-q", "-m", "base")

	clone = filepath.Join(t.TempDir(), "clone")
	gitT(t, filepath.Dir(clone), "clone", "-q", upstream, clone)
	gitT(t, clone, "checkout", "-q", "-b", "feat/42-thing")
	return upstream, clone
}

// advanceUpstream lands a commit on upstream main touching path.
func advanceUpstream(t *testing.T, upstream, path, body string) string {
	t.Helper()
	writeFileT(t, filepath.Join(upstream, path), body)
	gitT(t, upstream, "add", ".")
	gitT(t, upstream, "commit", "-q", "-m", "blocker merged")
	return gitT(t, upstream, "rev-parse", "HEAD")
}

func commitFile(t *testing.T, dir, path, body string) {
	t.Helper()
	writeFileT(t, filepath.Join(dir, path), body)
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "work on "+path)
}

func assertNoMergeInProgress(t *testing.T, dir string) {
	t.Helper()
	if mergeInProgress(context.Background(), dir) {
		t.Fatal("MERGE_HEAD left behind")
	}
	if out := gitT(t, dir, "stash", "list"); out != "" {
		t.Fatalf("stash list is not empty: %s", out)
	}
}

func TestRefreshResumedBase_UpToDateIsANoOp(t *testing.T) {
	_, clone := baseRefreshRepos(t)
	before := gitT(t, clone, "rev-parse", "HEAD")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeUpToDate || res.Behind != 0 || res.Advanced() {
		t.Fatalf("got %+v, want up to date", res)
	}
	if after := gitT(t, clone, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved %s -> %s", before, after)
	}
}

func TestRefreshResumedBase_FastForwardsABranchWithNoCommits(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	blocker := advanceUpstream(t, upstream, "blocker.txt", "landed\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeMerged || res.Behind != 1 || res.Ahead != 0 {
		t.Fatalf("got %+v, want a one-commit merge", res)
	}
	if head := gitT(t, clone, "rev-parse", "HEAD"); head != blocker {
		t.Fatalf("HEAD = %s, want the fast-forwarded base %s", head, blocker)
	}
	// A merge commit here would read as dev work to ResolvePreservedResume.
	if ahead, err := commitsAhead(context.Background(), clone, "origin/main"); err != nil || ahead != 0 {
		t.Fatalf("ahead of origin/main = %d (%v), want 0", ahead, err)
	}
}

func TestRefreshResumedBase_MergesIntoABranchWithItsOwnCommits(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	commitFile(t, clone, "work.txt", "work\n")
	blocker := advanceUpstream(t, upstream, "blocker.txt", "landed\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeMerged || res.Behind != 1 || res.Ahead != 1 {
		t.Fatalf("got %+v, want a merge", res)
	}
	gitT(t, clone, "merge-base", "--is-ancestor", blocker, "HEAD")
	if parents := strings.Fields(gitT(t, clone, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("HEAD has parents %v, want a merge commit", parents)
	}
}

func TestRefreshResumedBase_KeepsUncommittedWork(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	commitFile(t, clone, "work.txt", "work\n")
	writeFileT(t, filepath.Join(clone, "a.txt"), "a\nedited, not committed\n")
	writeFileT(t, filepath.Join(clone, "new.txt"), "untracked\n")
	advanceUpstream(t, upstream, "blocker.txt", "landed\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeMerged {
		t.Fatalf("got %+v, want merged", res)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(b) != "a\nedited, not committed\n" {
		t.Fatalf("uncommitted edit lost: %q", b)
	}
	if _, err := os.Stat(filepath.Join(clone, "new.txt")); err != nil {
		t.Fatalf("untracked file lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "blocker.txt")); err != nil {
		t.Fatalf("base change not merged: %v", err)
	}
	assertNoMergeInProgress(t, clone)
}

func TestRefreshResumedBase_ResolvesAChangelogOnlyConflict(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	commitFile(t, clone, "CHANGELOG.md",
		"# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- base entry\n- branch entry\n")
	advanceUpstream(t, upstream, "CHANGELOG.md",
		"# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- base entry\n- blocker entry\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeChangelogResolved || !res.Advanced() {
		t.Fatalf("got %+v, want changelog_resolved", res)
	}
	b, _ := os.ReadFile(filepath.Join(clone, "CHANGELOG.md"))
	if !strings.Contains(string(b), "- branch entry") || !strings.Contains(string(b), "- blocker entry") ||
		strings.Contains(string(b), "<<<<<<<") {
		t.Fatalf("CHANGELOG not unioned:\n%s", b)
	}
	assertNoMergeInProgress(t, clone)
}

func TestRefreshResumedBase_RealConflictAbortsAndLeavesTheTreeAsFound(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	commitFile(t, clone, "a.txt", "branch side\n")
	writeFileT(t, filepath.Join(clone, "dirty.txt"), "uncommitted\n")
	gitT(t, clone, "add", "dirty.txt")
	before := gitT(t, clone, "rev-parse", "HEAD")
	advanceUpstream(t, upstream, "a.txt", "base side\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeConflict || len(res.ConflictFiles) != 1 || res.ConflictFiles[0] != "a.txt" {
		t.Fatalf("got %+v, want a conflict naming a.txt", res)
	}
	if after := gitT(t, clone, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved %s -> %s", before, after)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "dirty.txt")); string(b) != "uncommitted\n" {
		t.Fatalf("uncommitted work lost: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(b) != "branch side\n" {
		t.Fatalf("a.txt = %q, want the branch side restored", b)
	}
	assertNoMergeInProgress(t, clone)
}

func TestRefreshResumedBase_DirtyFileTheBaseChangedStopsBeforeStashing(t *testing.T) {
	upstream, clone := baseRefreshRepos(t)
	commitFile(t, clone, "work.txt", "work\n")
	writeFileT(t, filepath.Join(clone, "a.txt"), "uncommitted side\n")
	advanceUpstream(t, upstream, "a.txt", "base side\n")

	res, err := RefreshResumedBase(context.Background(), clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != gitpkg.BaseMergeConflict || res.Reason == "" ||
		len(res.ConflictFiles) != 1 || res.ConflictFiles[0] != "a.txt" {
		t.Fatalf("got %+v, want a stop naming a.txt", res)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(b) != "uncommitted side\n" {
		t.Fatalf("uncommitted edit changed: %q", b)
	}
	assertNoMergeInProgress(t, clone)
}

func TestRefreshResumedBase_RefusesAnotherIssuesBranch(t *testing.T) {
	_, clone := baseRefreshRepos(t)
	gitT(t, clone, "checkout", "-q", "-b", "feat/7-other")

	if _, err := RefreshResumedBase(context.Background(), clone, 42); err == nil {
		t.Fatal("want an error on another issue's branch")
	}
}

func TestRefreshResumedBase_FetchFailureIsAnError(t *testing.T) {
	_, clone := baseRefreshRepos(t)
	gitT(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
	before := gitT(t, clone, "rev-parse", "HEAD")

	if _, err := RefreshResumedBase(context.Background(), clone, 42); err == nil {
		t.Fatal("want an error when origin cannot be fetched")
	}
	if after := gitT(t, clone, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved %s -> %s", before, after)
	}
}
