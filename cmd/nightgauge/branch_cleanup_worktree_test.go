package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

// #541 AC5 — the branch-cleanup sweep's candidate set, and what makes it safe.
//
// The sweep enumerates repository-wide and does so even from inside a linked
// worktree (ListLocalBranches resolves the common store since #540). #640
// settled AC5 on the second of its two permitted routes: do not narrow the
// list, enforce occupancy at the point of deletion. These tests are that
// decision made executable at the CLI layer, where the sweep actually runs.

// cleanupSweepRepo is the topology the sweep has to survive: a primary checkout, two
// sibling linked worktrees, and a LOCAL bare repository standing in for origin
// so "the remote ref survived" is a real observation rather than an artefact of
// it never existing. Nothing here touches the network.
type cleanupSweepRepo struct {
	primary string
	origin  string
	// worktrees maps a branch to the linked worktree holding it.
	worktrees map[string]string
}

// setupCleanupSweepRepo builds that topology.
//
// Branch inventory, chosen so every filter the sweep relies on has a positive
// and a negative case:
//
//	main              — protected, never a candidate
//	fix/701-live      — held by a linked worktree on an ordinary checkout
//	fix/702-rebasing  — held by a linked worktree that is MID-REBASE
//	fix/703-stale     — held by nothing; the genuinely stale branch
//	wip/704-operator  — an operator branch, never a pipeline cleanup candidate
//
// fix/702-rebasing is the case the pre-#541-AC5 sweep got wrong, and it is not
// a race: a worktree stopped on a rebase conflict has a DETACHED HEAD, so
// `git worktree list --porcelain` prints `detached` and names no branch, while
// `git branch -D` still refuses with "used by worktree at …". An occupancy
// check consulted before the delete therefore says "not held" for a branch git
// will not let go of. That is an interrupted rebase — a state a pipeline run
// leaves behind every time it stops on a conflict — not a timing window.
//
// Everything except wip/704-operator is pushed to origin.
//
// GITHUB_TOKEN is cleared: NewService turns it into an *http.BasicAuth and this
// origin is a filesystem path, so leaving the developer's own token in the
// environment would make these tests pass or fail based on who ran them.
func setupCleanupSweepRepo(t *testing.T) cleanupSweepRepo {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(TempDir): %v", err)
	}
	primary := filepath.Join(root, "primary")
	origin := filepath.Join(root, "origin.git")

	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	gitIn(t, primary, "init", "-b", "main")
	gitIn(t, primary, "config", "user.email", "test@test")
	gitIn(t, primary, "config", "user.name", "test")
	writeFileT(t, filepath.Join(primary, "README.md"), "seed\n")
	gitIn(t, primary, "add", "README.md")
	gitIn(t, primary, "commit", "-m", "chore: seed")

	// Cloning rather than pushing main keeps the repository's own push guard
	// out of the fixture.
	gitIn(t, root, "clone", "--bare", primary, origin)
	gitIn(t, primary, "remote", "add", "origin", origin)
	gitIn(t, primary, "fetch", "origin")

	r := cleanupSweepRepo{primary: primary, origin: origin, worktrees: map[string]string{}}

	for _, b := range []string{"fix/701-live", "fix/702-rebasing", "fix/703-stale", "wip/704-operator"} {
		gitIn(t, primary, "branch", b, "main")
		if b != "wip/704-operator" {
			gitIn(t, primary, "push", "origin", b)
		}
	}
	gitIn(t, primary, "fetch", "origin")

	for _, b := range []string{"fix/701-live", "fix/702-rebasing"} {
		dir := filepath.Join(root, "wt-"+strings.ReplaceAll(b, "/", "-"))
		gitIn(t, primary, "worktree", "add", "--force", dir, b)
		gitIn(t, dir, "config", "user.email", "test@test")
		gitIn(t, dir, "config", "user.name", "test")
		r.worktrees[b] = dir
	}

	// Drive wt-fix-702-rebasing into a conflicted rebase so its HEAD detaches.
	rebasing := r.worktrees["fix/702-rebasing"]
	writeFileT(t, filepath.Join(rebasing, "conflict.txt"), "from the branch\n")
	gitIn(t, rebasing, "add", "conflict.txt")
	gitIn(t, rebasing, "commit", "-m", "feat: work on the branch")
	writeFileT(t, filepath.Join(primary, "conflict.txt"), "from main\n")
	gitIn(t, primary, "add", "conflict.txt")
	gitIn(t, primary, "commit", "-m", "chore: conflicting change on main")
	if _, err := gitTry(rebasing, "rebase", "main"); err == nil {
		t.Fatalf("fixture: `git rebase main` was expected to stop on a conflict but succeeded")
	}

	// The fixture is only meaningful if git really does hide the branch from
	// the worktree listing while still refusing to delete it. Assert the first
	// half here so a future git that reports the branch turns into an explicit
	// fixture failure rather than a silently vacuous test.
	if listing := gitIn(t, primary, "worktree", "list", "--porcelain"); strings.Contains(listing, "branch refs/heads/fix/702-rebasing") {
		t.Fatalf("fixture: expected the mid-rebase worktree to read as detached, got:\n%s", listing)
	}

	return r
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// gitTry runs git and returns its output and error instead of failing the test,
// for the commands the fixture EXPECTS to fail.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r cleanupSweepRepo) service(t *testing.T, dir string) *gitpkg.Service {
	t.Helper()
	svc, err := gitpkg.NewService(dir)
	if err != nil {
		t.Fatalf("NewService(%s): %v", dir, err)
	}
	return svc
}

// localRefExists reads the primary checkout, which shares the common store with
// every linked worktree.
func (r cleanupSweepRepo) localRefExists(t *testing.T, branch string) bool {
	t.Helper()
	return strings.TrimSpace(gitIn(t, r.primary, "branch", "--list", branch)) != ""
}

// remoteRefExists reads the BARE repository directly, so a stale
// remote-tracking ref in any checkout cannot fool it.
func (r cleanupSweepRepo) remoteRefExists(t *testing.T, branch string) bool {
	t.Helper()
	return strings.Contains(
		gitIn(t, r.origin, "for-each-ref", "--format=%(refname)", "refs/heads/"),
		"refs/heads/"+branch+"\n",
	)
}

// TestCleanupClosedIssueBranch_MidRebaseWorktreeKeepsLocalAndRemoteRefs is the
// #541 defect, still live on the CLI path after #640 and now closed.
//
// The sweep used to sequence the two halves itself, remote first, with an
// occupancy pre-check in front to make that order safe. A mid-rebase worktree
// defeats the pre-check without any race (see setupCleanupSweepRepo), so the sweep
// deleted origin's ref, THEN had `git branch -D` refused, matched "used by
// worktree" in the refusal text and returned "skipped" with a nil error.
//
// Net effect: a live run lost the branch its PR is opened from and the branch a
// re-run's ResetLocalBranchToRemote fetches to recover state, and the sweep
// reported it had touched nothing. The remote assertion below is the one that
// goes red against that implementation — "skipped" was already correct, which
// is precisely why the loss was invisible.
func TestCleanupClosedIssueBranch_MidRebaseWorktreeKeepsLocalAndRemoteRefs(t *testing.T) {
	r := setupCleanupSweepRepo(t)
	branch := "fix/702-rebasing"
	svc := r.service(t, r.primary)

	if !r.remoteRefExists(t, branch) {
		t.Fatalf("fixture: origin must carry %s before the sweep runs", branch)
	}

	action, reason, err := cleanupClosedIssueBranch(svc, branch)
	if err != nil {
		t.Fatalf("cleanupClosedIssueBranch(%s) err = %v, want nil", branch, err)
	}
	if action != "skipped" {
		t.Errorf("action = %q (reason=%q), want %q", action, reason, "skipped")
	}
	if !strings.Contains(reason, "worktree") {
		t.Errorf("reason = %q, want it to say a worktree holds the branch", reason)
	}
	if !r.localRefExists(t, branch) {
		t.Errorf("local refs/heads/%s was deleted although a worktree holds it", branch)
	}
	if !r.remoteRefExists(t, branch) {
		t.Errorf("origin refs/heads/%s was deleted although the local delete was refused — "+
			"the remote half must be gated on the local half having actually succeeded", branch)
	}
}

// cleanupSweepOutcome is one branch's result from walkCleanupSweep.
type cleanupSweepOutcome struct {
	action string
	reason string
}

// walkCleanupSweep reproduces gitBranchCleanupCmd's candidate enumeration and runs the
// deletion step over it as if EVERY issue read CLOSED.
//
// It deliberately over-approximates the real loop: it does not exempt the
// current branch and it does not consult GitHub, so it offers the deletion path
// strictly more branches than the command ever can. A branch that survives this
// walk survives the command. What it does share exactly is the part under test
// — the enumeration (ListLocalBranches plus remote-only stragglers), the shape
// filter (IsCleanupCandidate) and the deletion step (cleanupClosedIssueBranch).
func walkCleanupSweep(t *testing.T, svc *gitpkg.Service) (enumerated []string, outcomes map[string]cleanupSweepOutcome) {
	t.Helper()

	branches, err := svc.ListLocalBranches()
	if err != nil {
		t.Fatalf("ListLocalBranches: %v", err)
	}
	enumerated = append(enumerated, branches...)
	sort.Strings(enumerated)

	localSet := map[string]bool{}
	for _, b := range branches {
		localSet[b] = true
	}
	remoteBranches, _ := svc.ListRemoteBranches()
	for _, b := range remoteBranches {
		if !localSet[b] && gitpkg.IsCleanupCandidate(b) {
			branches = append(branches, b)
		}
	}

	outcomes = map[string]cleanupSweepOutcome{}
	for _, b := range branches {
		if b == "main" || b == "master" {
			continue
		}
		if !gitpkg.IsCleanupCandidate(b) {
			continue
		}
		if n, ok := gitpkg.ParseIssueNumberFromBranch(b); !ok || n == 0 {
			continue
		}
		action, reason, _ := cleanupClosedIssueBranch(svc, b)
		outcomes[b] = cleanupSweepOutcome{action: action, reason: reason}
	}
	return enumerated, outcomes
}

// TestGitBranchCleanupSweep_FromLinkedWorktreeAndFromPrimary is #541's AC5
// candidate-set question, answered where the sweep runs.
//
// The candidate set is repository-wide from a linked worktree — pinned exactly,
// so narrowing it silently has to break a test — and that is safe because every
// branch a worktree holds is refused at the point of deletion, including the
// mid-rebase worktree no candidate-list filter could have excluded. The stale
// branch is still deleted, so the surviving branches cannot be an artefact of a
// sweep that does nothing.
//
// Both invocation sites are walked against fresh fixtures and asserted to
// produce the SAME outcome for every branch: where the command is invoked from
// changes nothing about what it deletes.
func TestGitBranchCleanupSweep_FromLinkedWorktreeAndFromPrimary(t *testing.T) {
	// The whole common store, from anywhere in the repository.
	wantEnumerated := []string{"fix/701-live", "fix/702-rebasing", "fix/703-stale", "main", "wip/704-operator"}

	// wip/704-operator is absent because IsCleanupCandidate never offers an
	// operator prefix to the deletion path; main is absent for the same reason.
	wantActions := map[string]string{
		"fix/701-live":     "skipped", // held by an ordinary linked worktree
		"fix/702-rebasing": "skipped", // held by a MID-REBASE linked worktree
		"fix/703-stale":    "deleted", // held by nothing: genuinely stale
	}

	for _, site := range []string{"linked-worktree", "primary-checkout"} {
		t.Run(site, func(t *testing.T) {
			r := setupCleanupSweepRepo(t)
			from := r.primary
			if site == "linked-worktree" {
				from = r.worktrees["fix/701-live"]
			}

			enumerated, outcomes := walkCleanupSweep(t, r.service(t, from))

			if strings.Join(enumerated, ",") != strings.Join(wantEnumerated, ",") {
				t.Fatalf("candidate enumeration from %s = %v, want the whole common store %v",
					site, enumerated, wantEnumerated)
			}
			if len(outcomes) != len(wantActions) {
				t.Fatalf("deletion path was offered %v, want exactly %v", outcomes, wantActions)
			}
			for branch, want := range wantActions {
				got, ok := outcomes[branch]
				if !ok {
					t.Fatalf("%s never reached the deletion path", branch)
				}
				if got.action != want {
					t.Errorf("%s: action = %q (reason=%q), want %q", branch, got.action, got.reason, want)
				}
			}

			// A live run keeps BOTH refs, whichever worktree state it is in.
			for _, branch := range []string{"fix/701-live", "fix/702-rebasing"} {
				if !r.localRefExists(t, branch) {
					t.Errorf("local refs/heads/%s was deleted although a worktree holds it", branch)
				}
				if !r.remoteRefExists(t, branch) {
					t.Errorf("origin refs/heads/%s was deleted although a worktree holds it", branch)
				}
			}

			// ...and the sweep still does its job, so the assertions above are
			// not satisfied by a sweep that deletes nothing at all.
			if r.localRefExists(t, "fix/703-stale") {
				t.Error("local refs/heads/fix/703-stale survived although no worktree holds it")
			}
			if r.remoteRefExists(t, "fix/703-stale") {
				t.Error("origin refs/heads/fix/703-stale survived although no worktree holds it")
			}

			// Never offered to the deletion path at all.
			for _, branch := range []string{"main", "wip/704-operator"} {
				if _, reached := outcomes[branch]; reached {
					t.Errorf("%s reached the deletion path; it must be filtered out first", branch)
				}
				if !r.localRefExists(t, branch) {
					t.Errorf("local refs/heads/%s was deleted", branch)
				}
			}
		})
	}
}
