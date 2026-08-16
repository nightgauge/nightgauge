package git

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// liveRunRepo is the topology #541 was reproduced against: a primary checkout,
// one or more sibling linked worktrees, and a LOCAL bare repository standing in
// for origin so the remote half of a cleanup is really executed and really
// observable. Nothing here touches the network.
type liveRunRepo struct {
	primary string // primary checkout
	origin  string // bare repo acting as origin
	// worktrees maps a branch to the linked worktree that holds it.
	worktrees map[string]string
}

// setupLiveRunRepo builds that topology.
//
// Branch inventory, chosen so every filter in the fix has both a positive and a
// negative case:
//
//	main               — protected, present in every repo
//	feat/999-live      — held by a sibling worktree; a LIVE run
//	feat/777-other     — held by a SECOND sibling worktree; another live run
//	feat/123-stale     — held by nothing; the genuinely stale branch
//	wip/999-operator   — an operator branch, never a pipeline cleanup candidate
//
// Every branch except wip/999-operator is pushed to origin, so "the remote ref
// survived" is a real assertion rather than an artefact of it never existing.
//
// GITHUB_TOKEN is cleared: NewService turns it into an *http.BasicAuth, and the
// fixture's origin is a filesystem path. Leaving the developer's own token in
// the environment would make these tests pass or fail based on who ran them.
func setupLiveRunRepo(t *testing.T) liveRunRepo {
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
	gitExecTest(t, primary, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(primary, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	gitExecTest(t, primary, "add", "README.md")
	gitExecTest(t, primary, "commit", "-m", "chore: seed")

	// A bare clone is the origin; cloning avoids pushing the default branch,
	// which the repository's own push guard refuses.
	gitExecTest(t, root, "clone", "--bare", primary, origin)
	gitExecTest(t, primary, "remote", "add", "origin", origin)
	gitExecTest(t, primary, "fetch", "origin")

	repo := liveRunRepo{primary: primary, origin: origin, worktrees: map[string]string{}}

	for _, b := range []string{"feat/999-live", "feat/777-other", "feat/123-stale", "wip/999-operator"} {
		gitExecTest(t, primary, "branch", b, "main")
		if b != "wip/999-operator" {
			gitExecTest(t, primary, "push", "origin", b)
		}
	}
	gitExecTest(t, primary, "fetch", "origin")

	for _, b := range []string{"feat/999-live", "feat/777-other"} {
		dir := filepath.Join(root, "wt-"+strings.ReplaceAll(b, "/", "-"))
		gitExecTest(t, primary, "worktree", "add", "--force", dir, b)
		repo.worktrees[b] = dir
	}

	return repo
}

// service opens a Service rooted at dir.
func (r liveRunRepo) service(t *testing.T, dir string) *Service {
	t.Helper()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService(%s): %v", dir, err)
	}
	return svc
}

// localRefExists reports whether the primary checkout still carries the branch.
func (r liveRunRepo) localRefExists(t *testing.T, branch string) bool {
	t.Helper()
	return strings.TrimSpace(gitExecTest(t, r.primary, "branch", "--list", branch)) != ""
}

// remoteRefExists reads the BARE repository directly, so it cannot be fooled by
// a stale remote-tracking ref in the checkout.
func (r liveRunRepo) remoteRefExists(t *testing.T, branch string) bool {
	t.Helper()
	return strings.Contains(
		gitExecTest(t, r.origin, "for-each-ref", "--format=%(refname)", "refs/heads/"),
		"refs/heads/"+branch+"\n",
	)
}

// TestBranchCleanup_WorktreeHeldBranchKeepsLocalAndRemoteRefs is #541(a) and
// #541(b) in one assertion set, because the defect was one behaviour with two
// consequences: the local delete is refused (git's own guard, inherited via
// #540) and the caller carried on to the remote anyway, then reported success.
//
// A live run therefore lost the branch its PR is opened from and the branch a
// re-run's ResetLocalBranchToRemote fetches to recover state — while the
// command printed "deleted".
//
// Both invocation sites are exercised: from the primary checkout (the pipeline's
// own cleanup path) and from inside a sibling linked worktree (the shape #540's
// common-dir resolution made reachable).
func TestBranchCleanup_WorktreeHeldBranchKeepsLocalAndRemoteRefs(t *testing.T) {
	const branch = "feat/999-live"

	for _, tc := range []struct {
		name string
		from func(r liveRunRepo) string
	}{
		{"from-primary-checkout", func(r liveRunRepo) string { return r.primary }},
		{"from-a-sibling-worktree", func(r liveRunRepo) string { return r.worktrees["feat/777-other"] }},
		{"from-the-holding-worktree", func(r liveRunRepo) string { return r.worktrees[branch] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := setupLiveRunRepo(t)
			svc := r.service(t, tc.from(r))

			if !r.remoteRefExists(t, branch) {
				t.Fatalf("fixture setup failed: origin does not carry refs/heads/%s", branch)
			}

			err := svc.BranchCleanup(branch)

			// (a) BOTH refs survive. The remote one is the whole issue, and
			// it is asserted FIRST: what happened to the repository outranks
			// what the call reported about it.
			if !r.localRefExists(t, branch) {
				t.Errorf("local refs/heads/%s was deleted despite the worktree holding it", branch)
			}
			if !r.remoteRefExists(t, branch) {
				t.Errorf("origin refs/heads/%s was DELETED while a worktree still holds the branch — "+
					"the live run lost the branch its PR is opened from", branch)
			}

			// (b) A refused delete must never read as success.
			if err == nil {
				t.Fatal("BranchCleanup returned nil for a branch a live worktree holds — " +
					"a refused delete reported as a completed one")
			}
			// Three states, three returns: occupancy is distinguishable from
			// an ordinary failure at the call site.
			if !errors.Is(err, ErrBranchHeldByWorktree) {
				t.Errorf("error is not ErrBranchHeldByWorktree: %v", err)
			}
			var held *BranchHeldByWorktreeError
			if !errors.As(err, &held) {
				t.Fatalf("error is not a *BranchHeldByWorktreeError: %v", err)
			}
			if held.Branch != branch {
				t.Errorf("held.Branch = %q, want %q", held.Branch, branch)
			}
			if held.Worktree != r.worktrees[branch] {
				t.Errorf("held.Worktree = %q, want %q", held.Worktree, r.worktrees[branch])
			}
		})
	}
}

// TestBranchCleanup_PrimaryAndSiblingWorktreeBothOnTheBranch reproduces the
// fixture #541 was filed from, literally: the primary checkout and a sibling
// worktree are BOTH live on feat/999-live. It is the shape a re-run produces —
// the run's worktree on the branch and the operator's checkout left on it — and
// the one whose probe output recorded "remote ref after cleanup = DELETED".
func TestBranchCleanup_PrimaryAndSiblingWorktreeBothOnTheBranch(t *testing.T) {
	const branch = "feat/999-live"
	r := setupLiveRunRepo(t)
	gitExecTest(t, r.primary, "checkout", "--ignore-other-worktrees", branch)

	svc := r.service(t, r.primary)
	err := svc.BranchCleanup(branch)

	if !r.localRefExists(t, branch) {
		t.Errorf("local refs/heads/%s was deleted while two checkouts are live on it", branch)
	}
	if !r.remoteRefExists(t, branch) {
		t.Errorf("origin refs/heads/%s was DELETED while two checkouts are live on it", branch)
	}
	if !errors.Is(err, ErrBranchHeldByWorktree) {
		t.Fatalf("BranchCleanup err = %v, want ErrBranchHeldByWorktree", err)
	}
}

// TestBranchCleanup_DeletesBothHalvesWhenNothingHoldsTheBranch is the
// counterweight. Without it, a BranchCleanup that refused everything would pass
// every other test in this file while doing no cleanup at all.
func TestBranchCleanup_DeletesBothHalvesWhenNothingHoldsTheBranch(t *testing.T) {
	const branch = "feat/123-stale"
	r := setupLiveRunRepo(t)
	svc := r.service(t, r.primary)

	if !r.localRefExists(t, branch) || !r.remoteRefExists(t, branch) {
		t.Fatalf("fixture setup failed: %s must start present locally and on origin", branch)
	}

	if err := svc.BranchCleanup(branch); err != nil {
		t.Fatalf("BranchCleanup(%s): %v", branch, err)
	}
	if r.localRefExists(t, branch) {
		t.Errorf("local refs/heads/%s survived a cleanup that reported success", branch)
	}
	if r.remoteRefExists(t, branch) {
		t.Errorf("origin refs/heads/%s survived a cleanup that reported success", branch)
	}
}

// TestBranchCleanup_ReportsFailureWhenTheRemoteHalfFails pins the third state:
// the local half succeeded, the remote half genuinely could not run, and the
// branch is therefore NOT gone. That must surface as an error — and as an
// ordinary one, not as the occupancy error, or a caller that treats occupancy
// as "skipped, try later" would silently swallow a real failure.
func TestBranchCleanup_ReportsFailureWhenTheRemoteHalfFails(t *testing.T) {
	const branch = "feat/123-stale"
	r := setupLiveRunRepo(t)

	// Point origin at a path that does not exist: ls-remote and the delete
	// push both fail, and neither can prove the ref is gone.
	gitExecTest(t, r.primary, "remote", "set-url", "origin",
		filepath.Join(filepath.Dir(r.primary), "no-such-origin.git"))

	svc := r.service(t, r.primary)
	err := svc.BranchCleanup(branch)
	if err == nil {
		t.Fatal("BranchCleanup returned nil although the remote half could not run — " +
			"the branch is still on origin")
	}
	if errors.Is(err, ErrBranchHeldByWorktree) {
		t.Errorf("a transport failure was classified as worktree occupancy: %v", err)
	}
	if !r.remoteRefExists(t, branch) {
		t.Errorf("fixture is not discriminating: origin lost refs/heads/%s anyway", branch)
	}
}

// TestBranchCleanup_RefusesProtectedAndOptionLikeNames keeps the two guards that
// must run BEFORE either half. The option-like name matters specifically because
// the remote half used to go first: BranchDeleteRemote builds a refspec rather
// than an argv, so it would happily push a delete for a name `git branch -D`
// would have refused as a flag.
func TestBranchCleanup_RefusesProtectedAndOptionLikeNames(t *testing.T) {
	r := setupLiveRunRepo(t)
	svc := r.service(t, r.primary)

	for _, name := range []string{"main", "master"} {
		if err := svc.BranchCleanup(name); err == nil {
			t.Errorf("BranchCleanup(%q) returned nil, want a refusal", name)
		}
	}
	// An option-like name must be refused BY US. Without the up-front
	// validateRefArg the name reaches neither ref (no branch is called
	// "--all"), both halves quietly report "already absent", and the call
	// returns nil — a delete that never happened, reported as done.
	for _, name := range []string{"--all", "-D"} {
		err := svc.BranchCleanup(name)
		if err == nil {
			t.Errorf("BranchCleanup(%q) returned nil, want a refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), "makes git parse it as an option") {
			t.Errorf("BranchCleanup(%q) err = %v, want the leading-dash refusal", name, err)
		}
	}
	if !r.remoteRefExists(t, "main") {
		t.Error("origin refs/heads/main was deleted by a refused cleanup")
	}
}

// TestListCleanupCandidateBranches_ScopesTheSetFromInsideAWorktree is #541(c).
//
// Post-#540 ListLocalBranches resolves the COMMON store, so from inside a linked
// worktree it enumerates every branch in the repository. That is correct git
// behaviour and this test pins it unchanged — the scoping belongs to the
// cleanup candidate set, not to the general-purpose enumerator.
func TestListCleanupCandidateBranches_ScopesTheSetFromInsideAWorktree(t *testing.T) {
	r := setupLiveRunRepo(t)
	svc := r.service(t, r.worktrees["feat/999-live"])

	// The unchanged, deliberately repo-wide primitive.
	all, err := svc.ListLocalBranches()
	if err != nil {
		t.Fatalf("ListLocalBranches: %v", err)
	}
	wantAll := []string{"feat/123-stale", "feat/777-other", "feat/999-live", "main", "wip/999-operator"}
	sort.Strings(all)
	if strings.Join(all, ",") != strings.Join(wantAll, ",") {
		t.Errorf("ListLocalBranches from a linked worktree = %v, want the whole common store %v", all, wantAll)
	}

	// The scoped set the cleanup sweep may act on.
	got, err := svc.ListCleanupCandidateBranches()
	if err != nil {
		t.Fatalf("ListCleanupCandidateBranches: %v", err)
	}
	want := []string{"feat/123-stale"}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ListCleanupCandidateBranches from a linked worktree = %v, want %v\n"+
			"  main/wip/999-operator are not pipeline branches; "+
			"feat/999-live and feat/777-other are held by live worktrees", got, want)
	}
}

// TestBranchesHeldByWorktrees_SeesEverySiblingFromAnywhere pins the occupancy
// read itself. Both public guards above are built on it, so a version that
// only ever saw the worktree it was invoked from would weaken them both while
// leaving the single-holder tests green.
func TestBranchesHeldByWorktrees_SeesEverySiblingFromAnywhere(t *testing.T) {
	r := setupLiveRunRepo(t)

	for _, dir := range []string{r.primary, r.worktrees["feat/999-live"], r.worktrees["feat/777-other"]} {
		svc := r.service(t, dir)
		held, err := svc.branchesHeldByWorktrees()
		if err != nil {
			t.Fatalf("branchesHeldByWorktrees from %s: %v", dir, err)
		}
		for _, branch := range []string{"main", "feat/999-live", "feat/777-other"} {
			if _, ok := held[branch]; !ok {
				t.Errorf("from %s: %s missing from the occupancy map %v", dir, branch, held)
			}
		}
		if _, ok := held["feat/123-stale"]; ok {
			t.Errorf("from %s: feat/123-stale is held by no worktree but appears in %v", dir, held)
		}
		if got := held["feat/999-live"]; got != r.worktrees["feat/999-live"] {
			t.Errorf("from %s: feat/999-live held by %q, want %q", dir, got, r.worktrees["feat/999-live"])
		}
	}
}
