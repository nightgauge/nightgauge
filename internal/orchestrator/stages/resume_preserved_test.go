package stages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

const resumeIssueBody = "## Acceptance Criteria\n\n- [ ] It works.\n"

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	identity := []string{"-c", "user.name=t", "-c", "user.email=t@example.com"}
	out, err := gittest.Command(dir, append(identity, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resumeRepo is a repo on issue 42's branch, level with origin/main, carrying
// the context a killed run leaves behind.
func resumeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeFileT(t, filepath.Join(dir, "a.txt"), "a\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "base")
	gitT(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitT(t, dir, "checkout", "-q", "-b", "feat/42-thing")

	writeFileT(t, pipelineContextPath(dir, "dev-42.json"), "{}\n")
	rec, err := json.Marshal(preflight.ACReconcileResult{ACFingerprint: preflight.ACFingerprint(resumeIssueBody)})
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, pipelineContextPath(dir, "ac-reconcile-42.json"), string(rec))
	return dir
}

func commitOnBranch(t *testing.T, dir string) {
	t.Helper()
	writeFileT(t, filepath.Join(dir, "work.txt"), "work\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "work")
}

// preserveWip leaves the shape preserveWorkInProgress does, on a branch the
// re-dispatch tore down.
func preserveWip(t *testing.T, dir string, issue string) string {
	t.Helper()
	gitT(t, dir, "checkout", "-q", "-b", "doomed")
	writeFileT(t, filepath.Join(dir, "work.txt"), "work\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "chore: preserve WIP\n\nRefs: #"+issue+"\nStage: feature-validate")
	sha := gitT(t, dir, "rev-parse", "HEAD")
	gitT(t, dir, "update-ref", "refs/nightgauge/wip/feat-42-thing-1", sha)
	gitT(t, dir, "checkout", "-q", "feat/42-thing")
	gitT(t, dir, "branch", "-qD", "doomed")
	return sha
}

func bodyReader(body string) IssueBodyReader {
	return func(context.Context, int) (string, error) { return body, nil }
}

func TestResolvePreservedResume_BranchWithCommitsResumes(t *testing.T) {
	dir := resumeRepo(t)
	commitOnBranch(t, dir)
	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if !got.Resume {
		t.Fatalf("got %+v, want a resume", got)
	}
}

// AC 7: the torn-down case. The worktree is fresh, and the ref is the only way
// back to the work.
func TestResolvePreservedResume_RestoresPreservedWork(t *testing.T) {
	dir := resumeRepo(t)
	sha := preserveWip(t, dir, "42")
	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if !got.Resume || got.RestoredCommit != sha {
		t.Fatalf("got %+v, want a resume restoring %s", got, sha)
	}
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Fatalf("the preserved work is not on the branch: %v", err)
	}
}

func TestResolvePreservedResume_ChangedCriteriaReplans(t *testing.T) {
	dir := resumeRepo(t)
	commitOnBranch(t, dir)
	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody+"- [ ] And another.\n"))
	if got.Resume {
		t.Fatalf("got %+v: new criteria must replan (#2623)", got)
	}
}

func TestResolvePreservedResume_NewIssueCostsNoForgeCall(t *testing.T) {
	dir := resumeRepo(t)
	if err := os.Remove(pipelineContextPath(dir, "dev-42.json")); err != nil {
		t.Fatal(err)
	}
	called := false
	got := ResolvePreservedResume(context.Background(), dir, 42, func(context.Context, int) (string, error) {
		called = true
		return "", nil
	})
	if got.Resume || called {
		t.Fatalf("got %+v, forge called: %t", got, called)
	}
}

func TestResolvePreservedResume_NoWorkToResumeFrom(t *testing.T) {
	dir := resumeRepo(t)
	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if got.Resume {
		t.Fatalf("got %+v: a branch level with base has nothing to validate", got)
	}
}

func TestResolvePreservedResume_AnotherIssuesWipIsNotOurs(t *testing.T) {
	dir := resumeRepo(t)
	preserveWip(t, dir, "99")
	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if got.Resume {
		t.Fatalf("got %+v: merged another issue's work", got)
	}
}

func TestResolvePreservedResume_WrongBranch(t *testing.T) {
	dir := resumeRepo(t)
	gitT(t, dir, "checkout", "-q", "-b", "feat/43-other")
	commitOnBranch(t, dir)
	if got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody)); got.Resume {
		t.Fatalf("got %+v", got)
	}
}

func TestResolvePreservedResume_UnreadableIssueReplans(t *testing.T) {
	dir := resumeRepo(t)
	commitOnBranch(t, dir)
	got := ResolvePreservedResume(context.Background(), dir, 42, func(context.Context, int) (string, error) {
		return "", errors.New("HTTP 502")
	})
	if got.Resume {
		t.Fatalf("got %+v: an unverified fingerprint is not a match", got)
	}
}

func TestResolvePreservedResume_ConflictingWipLeavesTheTreeAsFound(t *testing.T) {
	dir := resumeRepo(t)
	preserveWip(t, dir, "42")
	gitT(t, dir, "checkout", "-q", "main")
	writeFileT(t, filepath.Join(dir, "work.txt"), "base moved\n")
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "moved")
	gitT(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitT(t, dir, "checkout", "-q", "feat/42-thing")
	gitT(t, dir, "reset", "-q", "--hard", "origin/main")

	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if got.Resume {
		t.Fatalf("got %+v", got)
	}
	if status := gitT(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("left the tree dirty:\n%s", status)
	}
}
