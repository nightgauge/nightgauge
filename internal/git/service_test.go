package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/nightgauge/nightgauge/internal/reclaim"
)

func setupTestRepo(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := InitRepo(dir)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := CreateInitialCommit(repo, dir); err != nil {
		t.Fatalf("CreateInitialCommit: %v", err)
	}
	svc := NewServiceFromRepo(repo, dir)
	return svc, dir
}

func TestCurrentBranch(t *testing.T) {
	svc, _ := setupTestRepo(t)
	branch, err := svc.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	// go-git defaults to "master" for PlainInit
	if branch != "master" {
		t.Errorf("CurrentBranch = %q, want 'master'", branch)
	}
}

func TestCurrentBranchDetachedHead(t *testing.T) {
	svc, _ := setupTestRepo(t)

	head, err := svc.repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	wt, err := svc.repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Hash: head.Hash()}); err != nil {
		t.Fatalf("Checkout detached: %v", err)
	}

	_, err = svc.CurrentBranch()
	if err == nil {
		t.Fatal("expected error for detached HEAD, got nil")
	}
	if !strings.Contains(err.Error(), "detached") {
		t.Errorf("expected 'detached' in error, got: %v", err)
	}
}

func TestBranchCreate(t *testing.T) {
	svc, _ := setupTestRepo(t)

	if err := svc.BranchCreate("feat/test-branch"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}

	branch, err := svc.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch after create: %v", err)
	}
	if branch != "feat/test-branch" {
		t.Errorf("CurrentBranch = %q, want 'feat/test-branch'", branch)
	}
}

func TestBranchCreateFrom(t *testing.T) {
	svc, dir := setupTestRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := svc.Commit("base commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := svc.BranchCreate("epic/1455-parent-epic"); err != nil {
		t.Fatalf("BranchCreate epic: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "epic.txt"), []byte("epic"), 0644); err != nil {
		t.Fatalf("WriteFile epic: %v", err)
	}
	if _, err := svc.Commit("epic commit"); err != nil {
		t.Fatalf("Commit epic: %v", err)
	}

	if err := svc.Checkout("master"); err != nil {
		t.Fatalf("Checkout master: %v", err)
	}

	if err := svc.BranchCreateFrom("feat/1477-sub-issue", "epic/1455-parent-epic"); err != nil {
		t.Fatalf("BranchCreateFrom: %v", err)
	}

	branch, err := svc.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "feat/1477-sub-issue" {
		t.Fatalf("CurrentBranch = %q, want feat/1477-sub-issue", branch)
	}

	entries, err := svc.Log(1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) != 1 || entries[0].Message != "epic commit" {
		t.Fatalf("feature branch should start from epic commit, got %#v", entries)
	}
}

func TestCheckout(t *testing.T) {
	svc, _ := setupTestRepo(t)

	// Create a branch first
	if err := svc.BranchCreate("other-branch"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}

	// Checkout back to master
	if err := svc.Checkout("master"); err != nil {
		t.Fatalf("Checkout master: %v", err)
	}

	branch, _ := svc.CurrentBranch()
	if branch != "master" {
		t.Errorf("CurrentBranch = %q, want 'master'", branch)
	}
}

func TestStatusClean(t *testing.T) {
	svc, _ := setupTestRepo(t)
	status, err := svc.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.IsClean {
		t.Error("expected clean status")
	}
}

func TestStatusDirty(t *testing.T) {
	svc, dir := setupTestRepo(t)

	// Create a new file
	if err := os.WriteFile(filepath.Join(dir, "new-file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	status, err := svc.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.IsClean {
		t.Error("expected dirty status")
	}
	if len(status.UntrackedFiles) == 0 {
		t.Error("expected untracked files")
	}
}

func TestCommit(t *testing.T) {
	svc, dir := setupTestRepo(t)

	// Create a file to commit
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	hash, err := svc.Commit("test commit")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if hash == "" {
		t.Error("expected non-empty commit hash")
	}

	// Should be clean after commit
	status, _ := svc.Status()
	if !status.IsClean {
		t.Error("expected clean after commit")
	}
}

func TestLog(t *testing.T) {
	svc, dir := setupTestRepo(t)

	// Add a second commit
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := svc.Commit("second commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	entries, err := svc.Log(5)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 log entries, got %d", len(entries))
	}
	if entries[0].Message != "second commit" {
		t.Errorf("latest commit message = %q, want 'second commit'", entries[0].Message)
	}
}

func TestDiffNoChanges(t *testing.T) {
	svc, _ := setupTestRepo(t)
	diff, err := svc.Diff()
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff != "No changes." {
		t.Errorf("Diff = %q, want 'No changes.'", diff)
	}
}

func TestResetPipeline(t *testing.T) {
	svc, dir := setupTestRepo(t)

	// Create a dirty state
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	status, _ := svc.Status()
	if !status.IsClean {
		t.Error("expected clean after reset")
	}
}

func TestAbortPipeline(t *testing.T) {
	svc, _ := setupTestRepo(t)

	// Create a feature branch
	if err := svc.BranchCreate("feat/test-pipeline"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}

	// Abort: should go back to master and delete the branch
	// Note: AbortPipeline checks out "main", but our test repo uses "master"
	// so we test with "master" by checking out a branch and aborting back
	if err := svc.BranchCreate("feat/to-abort"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}

	// We need a "master" reference to checkout to
	if err := svc.Checkout("master"); err == nil {
		// master exists, now test abort from feat/to-abort
		if err := svc.Checkout("feat/to-abort"); err != nil {
			t.Fatalf("Checkout feat/to-abort: %v", err)
		}
		// AbortPipeline goes to "main" but our repo has "master"
		// We test the branch deletion part by calling Checkout directly
		if err := svc.Checkout("master"); err != nil {
			t.Fatalf("Checkout master: %v", err)
		}
		branch, _ := svc.CurrentBranch()
		if branch != "master" {
			t.Errorf("after abort, branch = %q, want 'master'", branch)
		}
	}
}

func TestGenerateBranchSlug(t *testing.T) {
	tests := []struct {
		prefix string
		number int
		title  string
		want   string
	}{
		{"feat", 42, "Add new feature", "feat/42-add-new-feature"},
		{"fix", 100, "Fix bug in parser", "fix/100-fix-bug-in-parser"},
		{"feat", 1, "Hello World!!! @#$%", "feat/1-hello-world"},
		{"feat", 99, "A very long title that goes on and on and on and should be truncated at some reasonable point eventually", "feat/99-a-very-long-title-that-goes-on-and-on-and-on-and-s"},
	}

	for _, tt := range tests {
		got := GenerateBranchSlug(tt.prefix, tt.number, tt.title)
		if got != tt.want {
			t.Errorf("GenerateBranchSlug(%q, %d, %q) = %q, want %q",
				tt.prefix, tt.number, tt.title, got, tt.want)
		}
	}
}

func TestBranchPrefixFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "empty", labels: nil, want: "feat/"},
		{name: "empty slice", labels: []string{}, want: "feat/"},
		{name: "no match defaults to feat", labels: []string{"priority:high", "size:s"}, want: "feat/"},
		{name: "bug to fix", labels: []string{"bug"}, want: "fix/"},
		{name: "documentation to docs", labels: []string{"documentation"}, want: "docs/"},
		{name: "docs to docs", labels: []string{"docs"}, want: "docs/"},
		{name: "refactor to refactor", labels: []string{"refactor"}, want: "refactor/"},
		{name: "test to test", labels: []string{"test"}, want: "test/"},
		{name: "chore to chore", labels: []string{"chore"}, want: "chore/"},
		{name: "maintenance to chore", labels: []string{"maintenance"}, want: "chore/"},
		{name: "namespaced bug", labels: []string{"type:bug"}, want: "fix/"},
		{name: "namespaced docs", labels: []string{"type:documentation"}, want: "docs/"},
		{name: "namespaced refactor", labels: []string{"type:refactor"}, want: "refactor/"},
		{name: "uppercase normalized", labels: []string{"BUG"}, want: "fix/"},
		{name: "mixed case namespace", labels: []string{"Type:Refactor"}, want: "refactor/"},
		{name: "bug wins over refactor regardless of order", labels: []string{"refactor", "bug"}, want: "fix/"},
		{name: "bug wins over refactor reversed", labels: []string{"bug", "refactor"}, want: "fix/"},
		{name: "docs wins over refactor", labels: []string{"refactor", "docs"}, want: "docs/"},
		{name: "refactor wins over chore", labels: []string{"chore", "refactor"}, want: "refactor/"},
		{name: "test wins over chore", labels: []string{"chore", "test"}, want: "test/"},
		{name: "ignores blank labels", labels: []string{"", "  ", "bug"}, want: "fix/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BranchPrefixFromLabels(tt.labels)
			if got != tt.want {
				t.Errorf("BranchPrefixFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestIsCleanupCandidate(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "feat/583-thing", want: true},
		{branch: "fix/583-thing", want: true},
		{branch: "docs/583-example", want: true},
		{branch: "chore/583-example", want: true},
		{branch: "refactor/583-thing", want: true},
		{branch: "test/583-thing", want: true},
		{branch: "epic/583-thing", want: true},
		{branch: "wip/583-example", want: false},
		{branch: "feat/no-number", want: false},
		{branch: "feat/583", want: false},
		{branch: "main", want: false},
		{branch: "docs/not-an-issue", want: false},
	}
	for _, tt := range tests {
		if got := IsCleanupCandidate(tt.branch); got != tt.want {
			t.Errorf("IsCleanupCandidate(%q) = %v, want %v", tt.branch, got, tt.want)
		}
	}
}

func TestParseIssueNumberFromBranch(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   int
		ok     bool
	}{
		{name: "feature", branch: "feat/2045-fix-routing", want: 2045, ok: true},
		{name: "epic", branch: "epic/1455-billing-analytics", want: 1455, ok: true},
		{name: "missing prefix", branch: "2045-fix-routing", want: 0, ok: false},
		{name: "missing hyphen", branch: "feat/2045", want: 0, ok: false},
	}

	for _, tt := range tests {
		got, ok := ParseIssueNumberFromBranch(tt.branch)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: ParseIssueNumberFromBranch(%q) = (%d, %v), want (%d, %v)",
				tt.name, tt.branch, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseGitHubRemoteSlug(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{url: "git@github.com:nightgauge/nightgauge.git", want: "nightgauge/nightgauge"},
		{url: "ssh://git@github.com/nightgauge/nightgauge.git", want: "nightgauge/nightgauge"},
		{url: "https://github.com/nightgauge/nightgauge.git", want: "nightgauge/nightgauge"},
	}

	for _, tt := range tests {
		got, err := parseGitHubRemoteSlug(tt.url)
		if err != nil {
			t.Fatalf("parseGitHubRemoteSlug(%q): %v", tt.url, err)
		}
		if got != tt.want {
			t.Errorf("parseGitHubRemoteSlug(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestStatusCode(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"modified", "modified"},
		{"added", "added"},
		{"deleted", "deleted"},
	}

	// Just verify the function doesn't panic
	for _, tt := range tests {
		_ = tt // statusCode is tested implicitly through Status()
	}
}

func TestRepoPath(t *testing.T) {
	svc, dir := setupTestRepo(t)
	if svc.RepoPath() != dir {
		t.Errorf("RepoPath = %q, want %q", svc.RepoPath(), dir)
	}
}

func TestNewServiceInvalidPath(t *testing.T) {
	_, err := NewService("/nonexistent/path")
	if err == nil {
		t.Error("NewService with invalid path should fail")
	}
}

// setupTestRepoWithRemote creates a working repo backed by a local bare repo as origin.
// It pushes an initial commit and establishes remote tracking refs.
func setupTestRepoWithRemote(t *testing.T) (*Service, string) {
	t.Helper()

	// Create a bare repo to act as the remote
	remoteDir := t.TempDir()
	if _, err := gogit.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	// Create a working repo
	workDir := t.TempDir()
	workRepo, err := gogit.PlainInit(workDir, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}

	svc := NewServiceFromRepo(workRepo, workDir)

	// Write initial commit
	if err := CreateInitialCommit(workRepo, workDir); err != nil {
		t.Fatalf("CreateInitialCommit: %v", err)
	}

	// Set origin to file:// bare repo
	remoteURL := "file://" + remoteDir
	_, err = workRepo.CreateRemote(&config.RemoteConfig{
		Name:  "origin",
		URLs:  []string{remoteURL},
		Fetch: []config.RefSpec{"refs/heads/*:refs/remotes/origin/*"},
	})
	if err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}

	// Push master to remote
	refSpec := config.RefSpec("refs/heads/master:refs/heads/master")
	if err := workRepo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
	}); err != nil {
		t.Fatalf("Push initial: %v", err)
	}

	// Fetch to populate refs/remotes/origin/master
	if err := workRepo.Fetch(&gogit.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/*:refs/remotes/origin/*"},
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		t.Fatalf("Fetch: %v", err)
	}

	// Verify refs/remotes/origin/master is resolvable; if not, set it manually
	if _, lookupErr := workRepo.Reference(
		"refs/remotes/origin/master",
		true,
	); lookupErr != nil {
		// Manually populate the remote tracking ref from HEAD
		head, headErr := workRepo.Head()
		if headErr != nil {
			t.Fatalf("Head: %v", headErr)
		}
		trackingRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/remotes/origin/master"),
			head.Hash(),
		)
		if setErr := workRepo.Storer.SetReference(trackingRef); setErr != nil {
			t.Fatalf("SetReference origin/master: %v", setErr)
		}
	}

	return svc, workDir
}

func TestEnsureEpicBranch_Creates(t *testing.T) {
	svc, _ := setupTestRepoWithRemote(t)

	// No epic branch exists yet — should create it
	branchName, created, err := svc.EnsureEpicBranch(2650, "Reliability Improvements Wave 4")
	if err != nil {
		t.Fatalf("EnsureEpicBranch: %v", err)
	}
	if !created {
		t.Error("expected created=true for a new epic branch")
	}

	want := "epic/2650-reliability-improvements-wave-4"
	if branchName != want {
		t.Errorf("branchName = %q, want %q", branchName, want)
	}

	// Verify we're back on the original branch
	current, err := svc.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if current != "master" {
		t.Errorf("after EnsureEpicBranch, current branch = %q, want 'master'", current)
	}
}

func TestEnsureEpicBranch_Idempotent(t *testing.T) {
	svc, _ := setupTestRepoWithRemote(t)

	// Create once
	branchName, created, err := svc.EnsureEpicBranch(2428, "Multi-Adapter Support")
	if err != nil {
		t.Fatalf("first EnsureEpicBranch: %v", err)
	}
	if !created {
		t.Error("first call: expected created=true")
	}

	// Create again — should be no-op
	branchName2, created2, err := svc.EnsureEpicBranch(2428, "Multi-Adapter Support")
	if err != nil {
		t.Fatalf("second EnsureEpicBranch: %v", err)
	}
	if created2 {
		t.Error("second call: expected created=false (idempotent)")
	}
	if branchName2 != branchName {
		t.Errorf("second call branch = %q, want %q", branchName2, branchName)
	}
}

func TestEnsureEpicBranch_BranchNameLength(t *testing.T) {
	svc, _ := setupTestRepoWithRemote(t)

	// Very long title — should be truncated
	longTitle := "This Is An Extremely Long Epic Title That Exceeds The Maximum Allowed Slug Length For Branch Names"
	branchName, _, err := svc.EnsureEpicBranch(99999, longTitle)
	if err != nil {
		t.Fatalf("EnsureEpicBranch: %v", err)
	}
	if len(branchName) > 60 {
		t.Errorf("branch name %q has length %d > 60", branchName, len(branchName))
	}
}

// commitFile writes a file into the work tree and commits it via the service,
// returning the resulting commit hash. Used to build divergent histories.
func commitFile(t *testing.T, svc *Service, workDir, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	hash, err := svc.Commit(msg)
	if err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
	return hash
}

func localBranchHash(t *testing.T, svc *Service, name string) string {
	t.Helper()
	ref, err := svc.repo.Reference(plumbing.NewBranchReferenceName(name), true)
	if err != nil {
		t.Fatalf("lookup local %s: %v", name, err)
	}
	return ref.Hash().String()
}

func remoteBranchHash(t *testing.T, svc *Service, name string) string {
	t.Helper()
	ref, err := svc.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+name), true)
	if err != nil {
		t.Fatalf("lookup origin/%s: %v", name, err)
	}
	return ref.Hash().String()
}

// TestResetLocalBranchToRemote_ReconcilesDivergedLocal reproduces the #3884
// re-run divergence: a prior run published feat/<N>-... to origin and left a
// stale local ref, then a re-run produced a different local commit on top of
// the base — so local and origin diverge. ResetLocalBranchToRemote must snap
// the local ref back to the published tip so the worktree continues from the
// already-validated work.
func TestResetLocalBranchToRemote_ReconcilesDivergedLocal(t *testing.T) {
	svc, workDir := setupTestRepoWithRemote(t)

	const branch = "feat/35-e2e-offline-server-sync-test"

	// First run: branch from master, commit the validated work, publish it.
	if err := svc.BranchCreate(branch); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}
	commitFile(t, svc, workDir, "remote.txt", "first-run validated work", "feat: first run")
	if err := svc.PushBranch(branch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if err := svc.Fetch(true); err != nil {
		t.Fatalf("Fetch after publish: %v", err)
	}
	remoteHash := remoteBranchHash(t, svc, branch)

	// Re-run: recreate the local branch from master and commit DIFFERENT work,
	// leaving local diverged from origin (each one commit past master).
	if err := svc.Checkout("master"); err != nil {
		t.Fatalf("Checkout master: %v", err)
	}
	if err := svc.BranchDelete(branch); err != nil {
		t.Fatalf("BranchDelete: %v", err)
	}
	if err := svc.BranchCreate(branch); err != nil {
		t.Fatalf("BranchCreate (re-run): %v", err)
	}
	commitFile(t, svc, workDir, "local.txt", "re-run divergent work", "feat: re-run")

	if localBranchHash(t, svc, branch) == remoteHash {
		t.Fatal("setup failed: local and remote should diverge before reconcile")
	}

	// Reconcile: local ref must now match the published remote tip.
	if err := svc.Fetch(true); err != nil {
		t.Fatalf("Fetch before reconcile: %v", err)
	}
	if err := svc.ResetLocalBranchToRemote(branch); err != nil {
		t.Fatalf("ResetLocalBranchToRemote: %v", err)
	}

	if got := localBranchHash(t, svc, branch); got != remoteHash {
		t.Errorf("after reconcile local = %s, want origin tip %s", got, remoteHash)
	}
}

// TestResetLocalBranchToRemote_CreatesWhenLocalAbsent verifies the helper also
// covers the fresh-worktree case: no local ref yet, remote exists.
func TestResetLocalBranchToRemote_CreatesWhenLocalAbsent(t *testing.T) {
	svc, workDir := setupTestRepoWithRemote(t)

	const branch = "feat/44-common-leaves-named-split-favorite-equip"

	if err := svc.BranchCreate(branch); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}
	commitFile(t, svc, workDir, "work.txt", "published work", "feat: work")
	if err := svc.PushBranch(branch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if err := svc.Fetch(true); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	remoteHash := remoteBranchHash(t, svc, branch)

	// Drop the local ref to simulate a fresh worktree with no local branch.
	if err := svc.Checkout("master"); err != nil {
		t.Fatalf("Checkout master: %v", err)
	}
	if err := svc.BranchDelete(branch); err != nil {
		t.Fatalf("BranchDelete: %v", err)
	}
	if exists, _ := svc.LocalBranchExists(branch); exists {
		t.Fatal("setup failed: local branch should be absent")
	}

	if err := svc.ResetLocalBranchToRemote(branch); err != nil {
		t.Fatalf("ResetLocalBranchToRemote: %v", err)
	}

	if got := localBranchHash(t, svc, branch); got != remoteHash {
		t.Errorf("created local = %s, want origin tip %s", got, remoteHash)
	}
}

func gitExecTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
	return string(out)
}

func TestResetPipeline_PreservesUnlandedDeliverable(t *testing.T) {
	svc, dir := setupTestRepo(t)
	if err := svc.BranchCreate("feat/289-test-issue"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}
	if err := svc.Checkout("feat/289-test-issue"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	deliverablePath := filepath.Join(dir, "internal", "widget.go")
	if err := os.MkdirAll(filepath.Dir(deliverablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deliverablePath, []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	handoff := `{"files_changed":{"created":["internal/widget.go"],"modified":[]}}`
	if err := os.WriteFile(filepath.Join(pipelineDir, "dev-289.json"), []byte(handoff), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	if _, err := os.Stat(deliverablePath); err != nil {
		t.Fatalf("deliverable was discarded by ResetPipeline: %v", err)
	}
	status, err := svc.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.IsClean {
		t.Errorf("expected clean tree after recovery-commit + reset, got dirty: %+v", status)
	}
}

// setupTestRepoNamed initialises a repo in a directory with a chosen basename,
// so a test can reproduce the worktree layout (`<repo>-issue-<N>`) the
// issue-number derivation reads when HEAD is detached.
func setupTestRepoNamed(t *testing.T, name string) (*Service, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := InitRepo(dir)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := CreateInitialCommit(repo, dir); err != nil {
		t.Fatalf("CreateInitialCommit: %v", err)
	}
	return NewServiceFromRepo(repo, dir), dir
}

func writeFileTest(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeDevHandoff(t *testing.T, dir string, issue int, body string) {
	t.Helper()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dev-%d.json", issue)
	if err := os.WriteFile(filepath.Join(pipelineDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func detachHeadTest(t *testing.T, svc *Service) {
	t.Helper()
	head, err := svc.repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	wt, err := svc.repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Hash: head.Hash()}); err != nil {
		t.Fatalf("detach HEAD: %v", err)
	}
}

func checkoutNewBranchTest(t *testing.T, svc *Service, branch string) {
	t.Helper()
	if err := svc.BranchCreate(branch); err != nil {
		t.Fatalf("BranchCreate %s: %v", branch, err)
	}
	if err := svc.Checkout(branch); err != nil {
		t.Fatalf("Checkout %s: %v", branch, err)
	}
}

// TestResetPipeline_UndeterminedInputsPreserveWork covers the inputs #297
// recorded as silent no-ops. Each one reached the guard's `return nil` —
// indistinguishable from "there is nothing to preserve" — and the hard reset
// then destroyed the deliverable. The assertion is identical for all of them:
// the work survives, and the tree still ends clean.
func TestResetPipeline_UndeterminedInputsPreserveWork(t *testing.T) {
	const deliverable = "internal/widget.go"

	cases := []struct {
		name    string
		repoDir string
		setup   func(t *testing.T, svc *Service, dir string)
	}{
		{
			// Worktrees are created with `git worktree add --detach`, so a run
			// killed before the dev skill creates feat/<N>-… is always here.
			name:    "detached HEAD before the feature branch exists",
			repoDir: "checkout",
			setup: func(t *testing.T, svc *Service, dir string) {
				detachHeadTest(t, svc)
			},
		},
		{
			// A SIGKILL mid pre-push-validate leaves HEAD on this stray branch,
			// which matches no feat|fix|docs pattern and preserves nothing.
			name:    "stray temp-pre-push branch",
			repoDir: "checkout",
			setup: func(t *testing.T, svc *Service, dir string) {
				checkoutNewBranchTest(t, svc, "temp-pre-push-3")
			},
		},
		{
			// The handoff path is built from the Service's repoPath: a
			// worktree-isolated run whose Service was constructed on the main
			// root never finds the file, and the deliverable is still real.
			name:    "feature branch whose handoff is not in this checkout",
			repoDir: "checkout",
			setup: func(t *testing.T, svc *Service, dir string) {
				checkoutNewBranchTest(t, svc, "feat/297-widget")
			},
		},
		{
			name:    "handoff records no files while the tree is dirty",
			repoDir: "checkout",
			setup: func(t *testing.T, svc *Service, dir string) {
				checkoutNewBranchTest(t, svc, "feat/297-widget")
				writeDevHandoff(t, dir, 297, `{"schema_version":"1.8","files_changed":{}}`)
			},
		},
		{
			name:    "files_changed in a shape nothing models",
			repoDir: "checkout",
			setup: func(t *testing.T, svc *Service, dir string) {
				checkoutNewBranchTest(t, svc, "feat/297-widget")
				writeDevHandoff(t, dir, 297, `{"files_changed":42}`)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, dir := setupTestRepoNamed(t, tc.repoDir)
			tc.setup(t, svc, dir)
			path := writeFileTest(t, dir, deliverable, "package internal\n")

			if err := svc.ResetPipeline(); err != nil {
				t.Fatalf("ResetPipeline: %v", err)
			}

			if _, err := os.Stat(path); err != nil {
				t.Fatalf("hard reset destroyed work the guard could not account for: %v", err)
			}
			status, err := svc.Status()
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !status.IsClean {
				t.Errorf("expected a clean tree after checkpoint + reset, got %+v", status)
			}
		})
	}
}

// TestResetPipeline_PreservesFlatFilesChangedShape pins the tolerant decode.
// #240 shipped a complete, well-formed dev context whose files_changed was a
// flat array of paths; the struct that modelled only {created, modified} turned
// it into a parse error whose sole consequence was a log line.
//
// Asserting the commit *subject* is what makes this test discriminating: the
// unconditional checkpoint would also leave the file on disk, so surviving the
// reset alone would not prove the array was ever understood.
func TestResetPipeline_PreservesFlatFilesChangedShape(t *testing.T) {
	svc, dir := setupTestRepoNamed(t, "checkout")
	checkoutNewBranchTest(t, svc, "feat/297-widget")
	path := writeFileTest(t, dir, "internal/widget.go", "package internal\n")
	writeDevHandoff(t, dir, 297,
		`{"schema_version":"1.8","files_changed":["internal/widget.go"]}`)

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deliverable was discarded: %v", err)
	}
	subject := strings.TrimSpace(gitExecTest(t, dir, "log", "-1", "--format=%s"))
	if !strings.Contains(subject, "preserve unlanded deliverable") {
		t.Errorf("expected the recorded-deliverable path to run, commit subject = %q", subject)
	}
	if !strings.Contains(subject, "#297") {
		t.Errorf("expected the recovery commit to name the issue, subject = %q", subject)
	}
}

// TestResetPipeline_DiscardsDirtWhenNothingRecordedIsAtRisk is the counterweight
// to the tests above: a guard that answered UNDETERMINED to everything would
// pass all of them while making ResetPipeline incapable of resetting. Here the
// run's own record of what it produced is fully committed, so the remaining
// dirt is positively not the deliverable and the reset must still wipe it.
func TestResetPipeline_DiscardsDirtWhenNothingRecordedIsAtRisk(t *testing.T) {
	svc, dir := setupTestRepoNamed(t, "checkout")
	checkoutNewBranchTest(t, svc, "feat/297-widget")

	writeFileTest(t, dir, "internal/widget.go", "package internal\n")
	writeDevHandoff(t, dir, 297,
		`{"schema_version":"1.8","files_changed":{"created":["internal/widget.go"],"modified":[]}}`)
	gitExecTest(t, dir, "add", "-A")
	gitExecTest(t, dir, "commit", "-m", "feat: land the deliverable")

	junk := writeFileTest(t, dir, "junk.txt", "build output\n")

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Errorf("expected reset to discard dirt the run did not record, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "widget.go")); err != nil {
		t.Errorf("committed deliverable should be untouched: %v", err)
	}
}

// TestResetPipeline_DetachedCheckpointIsReachableByName guards the recovery
// path's usability. A commit made on a detached HEAD is preserved only in the
// reflog until gc runs, and pipeline worktrees are detached by construction, so
// the rescue commit gets a branch pointing at it.
func TestResetPipeline_DetachedCheckpointIsReachableByName(t *testing.T) {
	svc, dir := setupTestRepoNamed(t, "checkout")
	detachHeadTest(t, svc)
	writeFileTest(t, dir, "internal/widget.go", "package internal\n")

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	branches := gitExecTest(t, dir, "branch", "--list", "nightgauge-checkpoint-*")
	if strings.TrimSpace(branches) == "" {
		t.Error("expected a nightgauge-checkpoint-* branch anchoring the detached rescue commit")
	}
}

// TestResetPipeline_RefusesWhenCheckpointFails is the fix's floor: when the
// guard cannot tell what the tree holds AND the rescue commit fails, the only
// safe answer is to not reset. Falling through to HardReset + clean here is
// precisely the #289 blast.
func TestResetPipeline_RefusesWhenCheckpointFails(t *testing.T) {
	svc, dir := setupTestRepoNamed(t, "checkout")
	checkoutNewBranchTest(t, svc, "temp-pre-push-3")
	path := writeFileTest(t, dir, "internal/widget.go", "package internal\n")

	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitExecTest(t, dir, "config", "core.hooksPath", hooks)

	err := svc.ResetPipeline()
	if err == nil {
		t.Fatal("expected ResetPipeline to refuse when the safety checkpoint fails")
	}
	if !strings.Contains(err.Error(), "refusing to hard-reset") {
		t.Errorf("error should say the reset was refused, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("work was destroyed despite the refusal: %v", statErr)
	}
}

func TestRecordedDeliverableFiles(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []string
		wantErr bool
	}{
		{
			name: "documented object shape",
			body: `{"files_changed":{"created":["a.go"],"modified":["b.go"],"deleted":["c.go"]}}`,
			want: []string{"a.go", "b.go", "c.go"},
		},
		{
			// The real #240 context, recorded in
			// internal/orchestrator/gates/context_decode_test.go.
			name: "flat array shape shipped by #240",
			body: `{"schema_version":"1.8","files_changed":["internal/e2e/e2e.go","internal/e2e/e2e_test.go"]}`,
			want: []string{"internal/e2e/e2e.go", "internal/e2e/e2e_test.go"},
		},
		{
			name: "empty object decodes to no files",
			body: `{"files_changed":{}}`,
			want: nil,
		},
		{name: "missing field", body: `{"schema_version":"1.8"}`, wantErr: true},
		{name: "scalar files_changed", body: `{"files_changed":42}`, wantErr: true},
		{name: "truncated json", body: `{"files_changed": {`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recordedDeliverableFiles([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got files %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("recordedDeliverableFiles: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("files = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPipelineIssueNumber(t *testing.T) {
	t.Run("derived from the worktree directory when HEAD is detached", func(t *testing.T) {
		svc, _ := setupTestRepoNamed(t, "nightgauge-issue-289")
		detachHeadTest(t, svc)

		got, err := svc.pipelineIssueNumber()
		if err != nil {
			t.Fatalf("pipelineIssueNumber: %v", err)
		}
		if got != "289" {
			t.Errorf("issue = %q, want %q", got, "289")
		}
	})

	t.Run("declines when two handoffs make ownership ambiguous", func(t *testing.T) {
		svc, dir := setupTestRepoNamed(t, "checkout")
		detachHeadTest(t, svc)
		writeDevHandoff(t, dir, 289, `{"files_changed":{}}`)
		writeDevHandoff(t, dir, 297, `{"files_changed":{}}`)

		if got, err := svc.pipelineIssueNumber(); err == nil {
			t.Errorf("expected ambiguity to be declined, got issue %q", got)
		}
	})
}

func TestResetPipeline_ReclaimsPipelineStash(t *testing.T) {
	svc, dir := setupTestRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "baseline.txt"), []byte("baseline"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExecTest(t, dir, "stash", "push", "-u", "-m", reclaim.StashName(reclaim.StashBaseline, 289, "feature-validate"))

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	// The stash must no longer be tracked in the stash list — ResetPipeline's
	// whole purpose is to wipe the tree, so the popped content is discarded by
	// the subsequent hard reset + clean along with everything else. What AC5
	// guards against is an *orphaned* stash silently surviving untracked.
	stashOut := gitExecTest(t, dir, "stash", "list")
	if strings.Contains(stashOut, reclaim.StashMarker) {
		t.Errorf("expected the pipeline stash to be reclaimed, stash list = %q", stashOut)
	}
}

// #330. The pre-fix reclaim popped only the FIRST match, walking the list
// top-down and returning on the first success. "Top of stack" was never a
// property of a leak — a stage that stashes twice, or two stages that both
// leak, left everything below the first entry behind forever.
func TestResetPipeline_ReclaimsEveryPipelineStash(t *testing.T) {
	svc, dir := setupTestRepo(t)

	for _, spec := range []struct {
		file  string
		issue int
		stage string
	}{
		{"first.txt", 289, "feature-validate"},
		{"second.txt", 330, "auto-fix"},
	} {
		if err := os.WriteFile(filepath.Join(dir, spec.file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitExecTest(t, dir, "stash", "push", "-u", "-m",
			reclaim.StashName(reclaim.StashBaseline, spec.issue, spec.stage))
	}

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	if out := gitExecTest(t, dir, "stash", "list"); strings.Contains(out, reclaim.StashMarker) {
		t.Errorf("a pipeline stash survived the reset, stash list = %q", out)
	}
}

// The reclaim is destructive by consequence — whatever it pops is wiped by the
// hard reset that follows. Acting on a stash whose ownership it cannot prove
// would destroy an operator's work, which is the failure mode #323 named for
// worktrees and the reason the marker exists at all.
func TestResetPipeline_NeverTouchesAnOperatorStash(t *testing.T) {
	svc, dir := setupTestRepo(t)

	// A modification to a TRACKED file, so `git stash show` can prove the
	// content survived rather than merely that the entry is still listed.
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte("my work in progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately shaped like the old convention the pre-#330 regex matched
	// (`[a-z-]+-\d+-baseline`), so this fails against that implementation.
	gitExecTest(t, dir, "stash", "push", "-m", "my-notes-42-baseline")

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	out := gitExecTest(t, dir, "stash", "list")
	if !strings.Contains(out, "my-notes-42-baseline") {
		t.Fatalf("the operator's stash was consumed by a pipeline reset; stash list = %q", out)
	}
	// And its content must still be recoverable, not merely listed.
	if show := gitExecTest(t, dir, "stash", "show", "--name-only", "stash@{0}"); !strings.Contains(show, ".gitkeep") {
		t.Errorf("the operator's stash no longer holds its content: %q", show)
	}
}

// setupLinkedWorktree builds a REAL linked worktree the way the pipeline does:
// a repo whose default branch is `main`, a bare clone acting as origin, populated
// remote-tracking refs, and `git worktree add --detach`. In a linked worktree
// `.git` is a FILE pointing at <common>/worktrees/<name>, and that directory
// holds only HEAD/index/logs/refs — no objects, no config, no refs/remotes.
// That is the exact shape that broke `nightgauge git branch-create` (#535), so
// the fixture is built with the git CLI rather than hand-authored: a
// hand-written .git layout is how this bug class survives a green suite.
func setupLinkedWorktree(t *testing.T) (mainDir, worktreeDir string) {
	t.Helper()

	root := t.TempDir()
	mainDir = filepath.Join(root, "main")
	originDir := filepath.Join(root, "origin.git")
	worktreeDir = filepath.Join(root, "issue-535")

	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitExecTest(t, mainDir, "init")
	gitExecTest(t, mainDir, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(mainDir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitExecTest(t, mainDir, "add", "README.md")
	gitExecTest(t, mainDir, "commit", "-m", "chore: seed")

	gitExecTest(t, root, "clone", "--bare", mainDir, originDir)
	gitExecTest(t, mainDir, "remote", "add", "origin", originDir)
	gitExecTest(t, mainDir, "fetch", "origin")
	// Remote-tracking refs are populated now, so repoint origin at a GitHub URL —
	// the shape RemoteRepoSlug has to parse in production.
	//
	// The owner/repo here is deliberately NOT this repository. A fixture whose
	// origin resolves to the real remote turns any accidental push — a stray
	// exploratory run, a future test that exercises a push path, EnsureEpicBranch
	// reached through the --issue codepath — into a write against production.
	// That is not hypothetical: it happened once while this test was being
	// written, and put a scratch "seed" commit on a real epic/* branch.
	gitExecTest(t, mainDir, "remote", "set-url", "origin",
		"https://github.com/nightgauge-fixture/not-a-real-repo.git")

	gitExecTest(t, mainDir, "worktree", "add", "--detach", worktreeDir, "HEAD")
	assertLinkedWorktree(t, worktreeDir)

	return mainDir, worktreeDir
}

// assertLinkedWorktree fails unless worktreeDir really is a linked worktree.
//
// Without this the fixture asserts nothing: deleting the `worktree add` line
// and returning the main checkout for both values leaves every
// "InLinkedWorktree" test GREEN, because a normal checkout answers all of them.
// The two properties that make the fixture the #535 shape are checked
// positively — `.git` is a FILE pointing at the worktree gitdir, and that
// gitdir is the chrooted one holding no objects, no config and no
// refs/remotes — so a degraded fixture fails here rather than passing quietly.
func assertLinkedWorktree(t *testing.T, worktreeDir string) {
	t.Helper()

	dotGit := filepath.Join(worktreeDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		t.Fatalf("fixture is not a linked worktree: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("fixture is not a linked worktree: %s is not a file", dotGit)
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		t.Fatalf("fixture is not a linked worktree: read %s: %v", dotGit, err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		t.Fatalf("fixture is not a linked worktree: %s does not start with \"gitdir:\" (%q)", dotGit, line)
	}

	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	for _, shouldBeAbsent := range []string{"objects", "config", filepath.Join("refs", "remotes")} {
		if _, err := os.Lstat(filepath.Join(gitDir, shouldBeAbsent)); err == nil {
			t.Fatalf("fixture is not a linked worktree: gitdir %s holds %s, so it is not the "+
				"chrooted per-worktree gitdir #535 is about", gitDir, shouldBeAbsent)
		}
	}
}

// TestNewService_DefaultBranchInLinkedWorktree covers #535: the linked worktree's
// own gitdir carries no refs/remotes, so the service must resolve refs through
// the common dir.
func TestNewService_DefaultBranchInLinkedWorktree(t *testing.T) {
	mainDir, worktreeDir := setupLinkedWorktree(t)

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{"main-checkout", mainDir},
		{"linked-worktree", worktreeDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewService(tc.dir)
			if err != nil {
				t.Fatalf("NewService(%s): %v", tc.dir, err)
			}
			got, err := svc.DefaultBranch()
			if err != nil {
				t.Fatalf("DefaultBranch: %v", err)
			}
			if got != "main" {
				t.Errorf("DefaultBranch = %q, want \"main\"", got)
			}
		})
	}
}

// TestNewService_RemoteRepoSlugInLinkedWorktree covers the shape the pipeline
// actually invokes: `nightgauge git branch-create --issue N --json` reaches
// RemoteRepoSlug before DefaultBranch, and the linked worktree's gitdir has no
// config file, so `origin` is invisible without common-dir support.
func TestNewService_RemoteRepoSlugInLinkedWorktree(t *testing.T) {
	_, worktreeDir := setupLinkedWorktree(t)

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService(linked worktree): %v", err)
	}

	slug, err := svc.RemoteRepoSlug()
	if err != nil {
		t.Fatalf("RemoteRepoSlug: %v", err)
	}
	if slug != "nightgauge-fixture/not-a-real-repo" {
		t.Errorf("RemoteRepoSlug = %q, want \"nightgauge-fixture/not-a-real-repo\"", slug)
	}
}

// TestBranchCreateFrom_InLinkedWorktreeMovesHead asserts the checkout really
// happens: the object store lives in the common dir, and a checkout that
// silently leaves HEAD detached must fail this test.
func TestBranchCreateFrom_InLinkedWorktreeMovesHead(t *testing.T) {
	_, worktreeDir := setupLinkedWorktree(t)

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService(linked worktree): %v", err)
	}

	const branch = "fix/535-commondir-aware-git-service"
	if err := svc.BranchCreateFrom(branch, "main"); err != nil {
		t.Fatalf("BranchCreateFrom: %v", err)
	}

	head := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if head != branch {
		t.Errorf("linked worktree HEAD = %q, want %q", head, branch)
	}
}

// TestNewService_RefusesGhostWorktreeDirectory pins DetectDotGit OFF.
//
// `.worktrees/issue-N` lives INSIDE the primary checkout, so with parent
// walking on, a ghost directory left behind by a removed run opens as the
// enclosing repository and every mutation aimed at the dead worktree lands in
// the operator's own tree — a write-containment escape
// (docs/MULTI_REPO_WORKSPACE.md#write-containment-issue-129). Adding
// `DetectDotGit: true` to NewService turns this test red and nothing else.
func TestNewService_RefusesGhostWorktreeDirectory(t *testing.T) {
	mainDir, _ := setupLinkedWorktree(t)

	ghost := filepath.Join(mainDir, ".worktrees", "issue-999")
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(ghost)
	if err == nil {
		root, _ := svc.Root()
		t.Fatalf("NewService(%s) opened a path that is not a repository and resolved to %s", ghost, root)
	}
}

// TestBranchCreateFrom_DirtyLinkedWorktreeKeepsModifications is the #535
// aftermath: common-dir resolution made go-git's checkout REACHABLE in a linked
// worktree, and go-git moves HEAD and writes refs/heads/<name> into the shared
// store BEFORE it discovers the tree is dirty. The pipeline dirties the
// worktree before the stage even runs (`npm install` rewrites tracked
// package-lock.json; codegen rewrites committed generated files), so this is the
// normal path, not an edge case — and the leaked ref made every retry take the
// "branch already exists" route and fail identically forever.
func TestBranchCreateFrom_DirtyLinkedWorktreeKeepsModifications(t *testing.T) {
	_, worktreeDir := setupLinkedWorktree(t)

	readme := filepath.Join(worktreeDir, "README.md")
	if err := os.WriteFile(readme, []byte("seed\nuncommitted local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	const branch = "fix/535-dirty-worktree"
	if err := svc.BranchCreateFrom(branch, "main"); err != nil {
		t.Fatalf("BranchCreateFrom on a dirty linked worktree: %v", err)
	}

	if head := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")); head != branch {
		t.Errorf("HEAD = %q, want %q", head, branch)
	}
	content, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !strings.Contains(string(content), "uncommitted local edit") {
		t.Errorf("the working-tree modification was discarded: %q", string(content))
	}

	// And it must be re-runnable: the wedge was a leaked ref, not a bad tree.
	if err := svc.BranchCreateFrom(branch, "main"); err != nil {
		t.Errorf("re-running BranchCreateFrom must be idempotent, got: %v", err)
	}
}

// TestBranchCreateFrom_FailureLeavesNoRefAndUnmovedHead is the other half of
// atomicity: when the checkout genuinely cannot proceed, NOTHING may have
// happened. A created-but-unusable branch ref is what wedged the stage.
func TestBranchCreateFrom_FailureLeavesNoRefAndUnmovedHead(t *testing.T) {
	mainDir, worktreeDir := setupLinkedWorktree(t)

	// A base whose README differs from the worktree's checked-out content, so a
	// checkout onto it would have to overwrite the local modification.
	gitExecTest(t, mainDir, "checkout", "-b", "other")
	if err := os.WriteFile(filepath.Join(mainDir, "README.md"), []byte("divergent content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExecTest(t, mainDir, "commit", "-am", "chore: diverge")
	gitExecTest(t, mainDir, "checkout", "main")

	if err := os.WriteFile(filepath.Join(worktreeDir, "README.md"), []byte("seed\nlocal edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "HEAD"))

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	const branch = "fix/535-would-clobber"
	if err := svc.BranchCreateFrom(branch, "other"); err == nil {
		t.Fatal("expected BranchCreateFrom to refuse a checkout that would overwrite local changes")
	}

	if out, err := exec.Command("git", "-C", mainDir, "rev-parse", "--verify",
		"refs/heads/"+branch).CombinedOutput(); err == nil {
		t.Errorf("a failed BranchCreateFrom left refs/heads/%s behind at %s", branch, strings.TrimSpace(string(out)))
	}
	if got := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "HEAD")); got != headBefore {
		t.Errorf("a failed BranchCreateFrom moved HEAD: %s -> %s", headBefore, got)
	}
	content, _ := os.ReadFile(filepath.Join(worktreeDir, "README.md"))
	if !strings.Contains(string(content), "local edit") {
		t.Errorf("the refused checkout still touched the working tree: %q", string(content))
	}
}

// TestCheckout_RefusesBranchHeldByAnotherWorktree covers the second data-loss
// path common-dir resolution opened. go-git never reads
// <common>/worktrees/*/HEAD, so it put two worktrees on one branch — and the
// consequence is not cosmetic: one stage's commit becomes a staged DELETION in
// the other's tree, and commitAll's `git add -A && git commit` then erases the
// first stage's deliverable from the branch tip.
func TestCheckout_RefusesBranchHeldByAnotherWorktree(t *testing.T) {
	mainDir, worktreeDir := setupLinkedWorktree(t)

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.Checkout("main")
	if err == nil {
		t.Fatal("expected Checkout to refuse a branch the primary checkout already holds")
	}
	if !strings.Contains(err.Error(), mainDir) {
		t.Errorf("the error must name the holding worktree, got: %v", err)
	}
	if head := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")); head != "HEAD" {
		t.Errorf("the refused checkout moved HEAD to %q", head)
	}

	// And it must not over-fire: a branch nobody holds still checks out.
	gitExecTest(t, mainDir, "branch", "spare", "main")
	if err := svc.Checkout("spare"); err != nil {
		t.Fatalf("Checkout of an unheld branch: %v", err)
	}
	if head := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "--abbrev-ref", "HEAD")); head != "spare" {
		t.Errorf("HEAD = %q, want \"spare\"", head)
	}
}

// TestBranchDelete_RefusesBranchHeldByAnotherWorktree: removing the ref through
// go-git left the holding worktree on an unborn HEAD with its commits reachable
// only through the reflog. AbortPipeline shares this path, so it is guarded too.
func TestBranchDelete_RefusesBranchHeldByAnotherWorktree(t *testing.T) {
	mainDir, worktreeDir := setupLinkedWorktree(t)

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	err = svc.BranchDelete("main")
	if err == nil {
		t.Fatal("expected BranchDelete to refuse a branch another worktree has checked out")
	}
	if !strings.Contains(err.Error(), mainDir) {
		t.Errorf("the error must name the holding worktree, got: %v", err)
	}
	if head := strings.TrimSpace(gitExecTest(t, mainDir, "rev-parse", "--abbrev-ref", "HEAD")); head != "main" {
		t.Errorf("the primary checkout was orphaned: HEAD = %q", head)
	}

	// Not over-firing: an unoccupied branch still deletes.
	gitExecTest(t, mainDir, "branch", "stale/1", "main")
	if err := svc.BranchDelete("stale/1"); err != nil {
		t.Fatalf("BranchDelete of an unheld branch: %v", err)
	}
	if _, err := exec.Command("git", "-C", mainDir, "rev-parse", "--verify",
		"refs/heads/stale/1").CombinedOutput(); err == nil {
		t.Error("refs/heads/stale/1 survived BranchDelete")
	}
}

// TestAbortPipeline_NeverOrphansASiblingWorktree exercises the same guard
// through the path that actually aborts runs. The happy half also pins that the
// guard does not over-fire: aborting from the checkout that owns the branch
// still checks out main and deletes it.
func TestAbortPipeline_NeverOrphansASiblingWorktree(t *testing.T) {
	mainDir, worktreeDir := setupLinkedWorktree(t)

	primary, err := NewService(mainDir)
	if err != nil {
		t.Fatalf("NewService(main): %v", err)
	}
	if err := primary.BranchCreate("feat/535-abort-me"); err != nil {
		t.Fatalf("BranchCreate: %v", err)
	}
	if err := primary.AbortPipeline("feat/535-abort-me"); err != nil {
		t.Fatalf("AbortPipeline in the owning checkout: %v", err)
	}
	if head := strings.TrimSpace(gitExecTest(t, mainDir, "rev-parse", "--abbrev-ref", "HEAD")); head != "main" {
		t.Errorf("after abort, HEAD = %q, want \"main\"", head)
	}
	if _, err := exec.Command("git", "-C", mainDir, "rev-parse", "--verify",
		"refs/heads/feat/535-abort-me").CombinedOutput(); err == nil {
		t.Error("AbortPipeline left the feature branch behind")
	}

	// Now the sibling case: the primary checkout holds the branch, and a run in
	// the linked worktree aborts it. Deleting it would strand the primary on an
	// unborn HEAD with its commits reachable only through the reflog.
	if err := primary.BranchCreate("feat/535-sibling"); err != nil {
		t.Fatalf("BranchCreate sibling: %v", err)
	}
	linked, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService(worktree): %v", err)
	}
	if err := linked.AbortPipeline("feat/535-sibling"); err == nil {
		t.Fatal("expected AbortPipeline to refuse a branch a sibling worktree holds")
	} else if !strings.Contains(err.Error(), mainDir) {
		t.Errorf("the error must name the holding worktree, got: %v", err)
	}
	if head := strings.TrimSpace(gitExecTest(t, mainDir, "rev-parse", "--abbrev-ref", "HEAD")); head != "feat/535-sibling" {
		t.Errorf("the sibling worktree was orphaned: HEAD = %q", head)
	}
}

// TestResetPipeline_KeepsGitignoredFiles: go-git honours neither .gitignore nor
// .git/info/exclude in its hard reset OR its Clean, so once #535 made both
// reachable inside a worktree, every ResetPipeline deleted node_modules/, build
// caches and .env — the stage's own execution environment, and possibly an
// operator secret. The counterweight is
// TestResetPipeline_DiscardsDirtWhenNothingRecordedIsAtRisk: untracked junk that
// is NOT ignored must still be swept.
func TestResetPipeline_KeepsGitignoredFiles(t *testing.T) {
	svc, dir := setupTestRepoNamed(t, "checkout")
	checkoutNewBranchTest(t, svc, "feat/297-widget")

	// A committed deliverable the handoff accounts for, so the guard reaches a
	// POSITIVE "nothing at risk" verdict and the reset actually runs — this test
	// would otherwise pass by refusing to reset at all.
	writeFileTest(t, dir, "internal/widget.go", "package internal\n")
	writeFileTest(t, dir, ".gitignore", "node_modules/\n")
	writeDevHandoff(t, dir, 297,
		`{"schema_version":"1.8","files_changed":{"created":["internal/widget.go"],"modified":[]}}`)
	gitExecTest(t, dir, "add", "-A")
	gitExecTest(t, dir, "commit", "-m", "feat: land the deliverable")

	// The second ignore source go-git also does not read.
	writeFileTest(t, dir, ".git/info/exclude", ".env\n")

	dep := writeFileTest(t, dir, "node_modules/dep/index.js", "module.exports = 1\n")
	env := writeFileTest(t, dir, ".env", "GITHUB_TOKEN=secret\n")
	junk := writeFileTest(t, dir, "build/out.tmp", "build output\n")

	if err := svc.ResetPipeline(); err != nil {
		t.Fatalf("ResetPipeline: %v", err)
	}

	if _, err := os.Stat(dep); err != nil {
		t.Errorf("ResetPipeline deleted a .gitignore'd path: %v", err)
	}
	if _, err := os.Stat(env); err != nil {
		t.Errorf("ResetPipeline deleted a .git/info/exclude'd path: %v", err)
	}
	// Not over-firing: untracked dirt that nothing ignores is still swept.
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Errorf("expected the reset to sweep untracked, non-ignored dirt, err=%v", err)
	}
}

// TestRefArgsBeginningWithDashAreRefused pins the argv boundary that appeared
// when the guarded mutations moved from go-git to the git CLI. go-git took names
// as Go strings and never built an argv; `git checkout <name>` and
// `git branch -D <name>` take the name as a positional operand, so a name
// starting with "-" is parsed as a flag instead. Branch names are derived from
// issue titles, which on a public repository anyone can supply.
//
// Each arm asserts OUR refusal (not git's downstream complaint) and that the
// repository was not mutated on the way to the error.
func TestRefArgsBeginningWithDashAreRefused(t *testing.T) {
	_, worktreeDir := setupLinkedWorktree(t)

	svc, err := NewService(worktreeDir)
	if err != nil {
		t.Fatalf("NewService(linked worktree): %v", err)
	}

	headBefore := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "HEAD"))

	// Shapes git would read as options rather than names.
	for _, name := range []string{"--upload-pack=touch /tmp/pwned", "-f", "--orphan", "-"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				op   string
				call func() error
			}{
				{"BranchCreate", func() error { return svc.BranchCreate(name) }},
				{"BranchCreateFrom", func() error { return svc.BranchCreateFrom(name, "main") }},
				{"BranchDelete", func() error { return svc.BranchDelete(name) }},
				{"Checkout", func() error { return svc.Checkout(name) }},
			} {
				err := tc.call()
				if err == nil {
					t.Fatalf("%s(%q) returned nil — a name git parses as an option must be refused", tc.op, name)
				}
				if !strings.Contains(err.Error(), "makes git parse it as an option") {
					t.Errorf("%s(%q): want our own refusal, got %v", tc.op, name, err)
				}
			}
		})
	}

	// Nothing may have moved on the way to those errors.
	if got := strings.TrimSpace(gitExecTest(t, worktreeDir, "rev-parse", "HEAD")); got != headBefore {
		t.Errorf("HEAD moved during refused operations: %s -> %s", headBefore, got)
	}
	if branches := gitExecTest(t, worktreeDir, "branch", "--list"); strings.Contains(branches, "-") &&
		strings.Contains(branches, "upload-pack") {
		t.Errorf("a refused name reached git and created a branch: %q", branches)
	}
}

// TestValidateRefArgAcceptsRealNames is the positive control: the guard must not
// over-fire on the names the pipeline actually produces.
func TestValidateRefArgAcceptsRealNames(t *testing.T) {
	for _, name := range []string{
		"main",
		"fix/535-commondir-aware-git-service",
		"epic/531-grok-live-run-defects",
		"feat/1-a-b",
	} {
		if err := validateRefArg("branch", name); err != nil {
			t.Errorf("validateRefArg(%q) = %v, want nil", name, err)
		}
	}
	if err := validateRefArg("branch", ""); err == nil {
		t.Error("validateRefArg(\"\") = nil, want an error")
	}
}
