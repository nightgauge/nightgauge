package stages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Pure rule ─────────────────────────────────────────────────────────────

func TestDecideCommit(t *testing.T) {
	dirty := " M src/foo.ts\n?? src/bar.ts\n"
	exhaustOnly := "?? .nightgauge/pipeline/dev-42.json\n"

	cases := []struct {
		name       string
		branch     string
		base       string
		porcelain  string
		ahead      int
		aheadKnown bool
		want       bool
		wantReason string
	}{
		{
			// The #1179 case: routing skipped feature-validate, so nothing
			// committed, and the tree still carries the implementation.
			name:   "trivial path — validate skipped, dirty tree, zero commits ahead",
			branch: "fix/1179-x", base: "main", porcelain: dirty,
			ahead: 0, aheadKnown: true, want: true, wantReason: ReasonCommitted,
		},
		{
			// The normal path: feature-validate ran and committed.
			name:   "normal path — commits ahead of base",
			branch: "fix/1179-x", base: "main", porcelain: "",
			ahead: 1, aheadKnown: true, want: false, wantReason: ReasonCommitNotNeededAhead,
		},
		{
			name:   "clean tree, nothing ahead",
			branch: "fix/1179-x", base: "main", porcelain: "",
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitNotNeededClean,
		},
		{
			name:   "only pipeline exhaust — never committed",
			branch: "fix/1179-x", base: "main", porcelain: exhaustOnly,
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitNotNeededExhaustOnly,
		},
		{
			name:   "on the base branch — refuse",
			branch: "main", base: "main", porcelain: dirty,
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitRefusedBranchIsBase,
		},
		{
			name:   "base unknown defaults to main — refuse on main",
			branch: "main", base: "", porcelain: dirty,
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitRefusedBranchIsBase,
		},
		{
			name:   "detached HEAD — refuse",
			branch: "HEAD", base: "main", porcelain: dirty,
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitRefusedDetached,
		},
		{
			name:   "branch unknown — refuse",
			branch: "", base: "main", porcelain: dirty,
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitBranchUnknown,
		},
		{
			name:   "ahead count unknown — refuse rather than guess",
			branch: "fix/1179-x", base: "main", porcelain: dirty,
			ahead: 0, aheadKnown: false, want: false, wantReason: ReasonCommitRefusedAheadUnknown,
		},
		{
			name:   "unmerged index — refuse (would commit conflict markers)",
			branch: "fix/1179-x", base: "main", porcelain: "UU src/foo.ts\n M src/bar.ts\n",
			ahead: 0, aheadKnown: true, want: false, wantReason: ReasonCommitRefusedUnmergedIndex,
		},
		{
			// #237/#332: a TRACKED bookkeeping change is a deliverable, not
			// exhaust, and must be committed like any other work.
			name:   "tracked bookkeeping deliverable is work",
			branch: "fix/1179-x", base: "main", porcelain: " D .nightgauge/pipeline/assessments/a.json\n",
			ahead: 0, aheadKnown: true, want: true, wantReason: ReasonCommitted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideCommit(tc.branch, tc.base, tc.porcelain, tc.ahead, tc.aheadKnown)
			if got.ShouldCommit != tc.want {
				t.Errorf("ShouldCommit = %v, want %v (reason %q)", got.ShouldCommit, tc.want, got.Reason)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestDecideCommit_SplitsExhaustFromWork(t *testing.T) {
	d := DecideCommit("fix/1179-x", "main",
		" M src/foo.ts\n?? .nightgauge/pipeline/dev-42.json\n", 0, true)
	if !d.ShouldCommit {
		t.Fatalf("expected a commit decision, got %q", d.Reason)
	}
	if len(d.Blocking) != 1 || d.Blocking[0] != "src/foo.ts" {
		t.Errorf("Blocking = %v, want [src/foo.ts]", d.Blocking)
	}
	if len(d.Exhaust) != 1 || d.Exhaust[0] != ".nightgauge/pipeline/dev-42.json" {
		t.Errorf("Exhaust = %v, want [.nightgauge/pipeline/dev-42.json]", d.Exhaust)
	}
}

func TestCommitMessage_DeterministicAndConventional(t *testing.T) {
	snap := PRCreateSnapshot{IssueNumber: 1179, IssueTitle: "Trivial-path commit owner", IssueType: "fix", BaseBranch: "main"}
	first := CommitMessage(snap)
	for i := 0; i < 10; i++ {
		if CommitMessage(snap) != first {
			t.Fatalf("CommitMessage is not deterministic")
		}
	}
	if !strings.HasPrefix(first, "fix(#1179): Trivial-path commit owner") {
		t.Errorf("subject = %q, want a conventional-commit subject", strings.SplitN(first, "\n", 2)[0])
	}

	bare := CommitMessage(PRCreateSnapshot{IssueNumber: 7, IssueType: "fix"})
	if strings.SplitN(bare, "\n", 2)[0] != "fix(#7): implement issue #7" {
		t.Errorf("empty-title subject = %q, want a non-dangling subject", strings.SplitN(bare, "\n", 2)[0])
	}
}

// ── Runner wiring ─────────────────────────────────────────────────────────

// TestRunner_ValidateSkipped_CommitsBeforePunting is the #1179 regression.
// Routing skipped feature-validate, so validate-{N}.json is absent and
// DecideCreate punts to the LLM skill — but the commit owner has already
// created the commit, so whichever path opens the PR, the branch carries the
// work. Before the fix the branch had zero commits and the PR opened empty.
func TestRunner_ValidateSkipped_CommitsBeforePunting(t *testing.T) {
	snap := richSnap()
	snap.HasValidate = false
	snap.ValidationStatus = ""

	git := &fakeGit{branch: "fix/42-x", status: " M src/foo.ts\n", ahead: 0}
	r := newTestRunner(snap, &fakePRClient{}, git)

	res, err := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != CreatePathPunt || res.Reason != ReasonMissingValidateContext {
		t.Fatalf("expected a missing-validate punt, got %q/%q", res.Path, res.Reason)
	}
	if git.commitCalls != 1 {
		t.Fatalf("commit owner did not commit on the skipped-validate path (commitCalls=%d, reason=%q)",
			git.commitCalls, res.CommitReason)
	}
	if !res.CommitPerformed || res.CommitSHA == "" || res.CommitReason != ReasonCommitted {
		t.Errorf("commit outcome not reported on the punt path: %+v", res)
	}
	if !strings.HasPrefix(git.commitMessage, "feat(#42):") {
		t.Errorf("commit message = %q, want a conventional subject", git.commitMessage)
	}
}

// TestRunner_NormalPath_NoCommit pins the non-skipped path: feature-validate
// committed, the branch is ahead of base, and the commit owner must not touch
// git at all.
func TestRunner_NormalPath_NoCommit(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 99, URL: "https://github.com/owner/repo/pull/99"}}
	git := &fakeGit{branch: "fix/42-x", status: "", ahead: 2}
	r := newTestRunner(richSnap(), prc, git)

	res, err := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != CreatePathCreated || res.PRNumber != 99 {
		t.Fatalf("normal path changed: %+v", res)
	}
	if git.commitCalls != 0 {
		t.Errorf("commit owner committed on the normal path (%d calls) — feature-validate already did", git.commitCalls)
	}
	if res.CommitPerformed || res.CommitReason != ReasonCommitNotNeededAhead {
		t.Errorf("CommitReason = %q (performed=%v), want %q", res.CommitReason, res.CommitPerformed, ReasonCommitNotNeededAhead)
	}
}

func TestRunner_CommitOwnerExcludesExhaust(t *testing.T) {
	git := &fakeGit{
		branch: "fix/42-x",
		status: " M src/foo.ts\n?? .nightgauge/pipeline/dev-42.json\n",
		ahead:  0,
	}
	r := newTestRunner(richSnap(), &fakePRClient{createdPR: &CreatedPR{Number: 5, URL: "u"}}, git)

	if _, err := r.Run(context.Background(), 42, "owner/repo", "/tmp"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if git.commitCalls != 1 {
		t.Fatalf("commitCalls = %d, want 1", git.commitCalls)
	}
	if len(git.commitExhaust) != 1 || git.commitExhaust[0] != ".nightgauge/pipeline/dev-42.json" {
		t.Errorf("exhaust passed to CommitAll = %v, want the untracked pipeline context file", git.commitExhaust)
	}
}

func TestRunner_CommitFailureIsRecordedNotFatal(t *testing.T) {
	git := &fakeGit{branch: "fix/42-x", status: " M src/foo.ts\n", ahead: 0, commitErr: errors.New("author identity unknown")}
	r := newTestRunner(richSnap(), &fakePRClient{createdPR: &CreatedPR{Number: 5, URL: "u"}}, git)

	res, err := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("a failed commit must not fail the stage: %v", err)
	}
	if res.CommitPerformed {
		t.Errorf("CommitPerformed should be false after a git failure")
	}
	if !strings.HasPrefix(res.CommitReason, ReasonCommitFailed) {
		t.Errorf("CommitReason = %q, want a %q prefix", res.CommitReason, ReasonCommitFailed)
	}
}

func TestRunner_NothingStagedIsBenign(t *testing.T) {
	git := &fakeGit{branch: "fix/42-x", status: " M src/foo.ts\n", ahead: 0, commitErr: ErrNothingStaged}
	r := newTestRunner(richSnap(), &fakePRClient{createdPR: &CreatedPR{Number: 5, URL: "u"}}, git)

	res, _ := r.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res.CommitReason != ReasonCommitNotNeededExhaustOnly {
		t.Errorf("CommitReason = %q, want %q", res.CommitReason, ReasonCommitNotNeededExhaustOnly)
	}
}

// ── Real git (end to end over the exec client) ────────────────────────────

// gitOut is gitRun (prmerge_worktree_test.go) with the trailing newline
// trimmed, so single-value outputs compare cleanly.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, dir, args...))
}

// initRepo builds a repo with a `main` commit and a feature branch checked out.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test")
	gitOut(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "base")
	gitOut(t, dir, "checkout", "-q", "-b", "fix/1179-x")
	return dir
}

// TestCommitOwner_RealGit_TrivialPathLeavesBranchNonEmpty drives the whole
// commit owner over a real repository with the production exec git client. The
// runner has no GitHub client, so it punts with pr-client-unavailable — the
// point is that the commit happened FIRST, so the branch handed to whoever
// opens the PR is not empty.
func TestCommitOwner_RealGit_TrivialPathLeavesBranchNonEmpty(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("implemented\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge", "pipeline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "pipeline", "dev-1179.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := PRCreateSnapshot{
		IssueNumber: 1179, IssueTitle: "Trivial-path commit owner", IssueType: "fix",
		Branch: "fix/1179-x", BaseBranch: "main", HasDev: true,
		FilesModified: []string{"feature.txt"},
	}
	r := NewDeterministicPRCreateRunner()
	r.git = NewExecGitClient()
	r.readContext = func(string, int) (PRCreateSnapshot, error) { return snap, nil }
	r.writeContext = func(string, prContextPayload) error { return nil }

	res, err := r.Run(context.Background(), 1179, "owner/repo", dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.CommitPerformed {
		t.Fatalf("no commit was created (reason=%q) — the branch would open an empty PR", res.CommitReason)
	}
	if ahead := gitOut(t, dir, "rev-list", "--count", "main..HEAD"); ahead != "1" {
		t.Fatalf("commits ahead of main = %s, want 1", ahead)
	}
	files := gitOut(t, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if !strings.Contains(files, "feature.txt") {
		t.Errorf("commit does not contain the implementation: %q", files)
	}
	if strings.Contains(files, ".nightgauge/") {
		t.Errorf("commit swept up pipeline exhaust: %q", files)
	}

	// Idempotent: a second pass finds the branch ahead of base and does nothing.
	res2, err := r.Run(context.Background(), 1179, "owner/repo", dir)
	if err != nil {
		t.Fatalf("unexpected err on second run: %v", err)
	}
	if res2.CommitPerformed || res2.CommitReason != ReasonCommitNotNeededAhead {
		t.Errorf("second run committed again: %+v", res2)
	}
	if ahead := gitOut(t, dir, "rev-list", "--count", "main..HEAD"); ahead != "1" {
		t.Errorf("commits ahead of main after second run = %s, want 1", ahead)
	}
}

// TestCommitOwner_RealGit_RefusesOnBaseBranch pins the safety property: the
// owner never creates a commit on the default branch.
func TestCommitOwner_RealGit_RefusesOnBaseBranch(t *testing.T) {
	dir := initRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewDeterministicPRCreateRunner()
	r.git = NewExecGitClient()
	r.readContext = func(string, int) (PRCreateSnapshot, error) {
		return PRCreateSnapshot{IssueNumber: 1179, Branch: "main", BaseBranch: "main", HasDev: true}, nil
	}
	r.writeContext = func(string, prContextPayload) error { return nil }

	res, err := r.Run(context.Background(), 1179, "owner/repo", dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.CommitPerformed || res.CommitReason != ReasonCommitRefusedBranchIsBase {
		t.Fatalf("commit owner acted on the base branch: %+v", res)
	}
	if head := gitOut(t, dir, "rev-list", "--count", "HEAD"); head != "1" {
		t.Errorf("main gained a commit: rev-list count = %s", head)
	}
}

// repoRootFromTest walks up from the test's working directory to the module
// root (the directory holding go.mod).
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate the module root from the test working directory")
	return ""
}

// TestFeatureDevStillDoesNotCommit pins issue 1608 against this change. The commit
// owner moved into pr-create BECAUSE feature-dev must not commit; a future edit
// that "fixes" the trivial path by making feature-dev commit instead reverses a
// deliberate decision, and this goes red when the skill stops saying so.
func TestFeatureDevStillDoesNotCommit(t *testing.T) {
	root := repoRootFromTest(t)
	for _, rel := range []string{
		filepath.Join("skills", "nightgauge-feature-dev", "SKILL.md"),
		filepath.Join("skills", "nightgauge-feature-dev", "_includes", "context-and-epilogue.md"),
		filepath.Join("claude-plugins", "nightgauge", "skills", "feature-dev", "SKILL.md"),
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		body := string(data)
		if !strings.Contains(body, "No commit or push in feature-dev.") {
			t.Errorf("%s no longer states the issue 1608 contract that feature-dev does not commit", rel)
		}
		if !strings.Contains(body, "stages.DecideCommit") {
			t.Errorf("%s does not name the structural commit owner for the skipped-validate route (#1179)", rel)
		}
	}
}
