// Package git provides in-process git operations using go-git,
// eliminating the need for the system git binary.
package git

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// Service provides git operations on a repository.
type Service struct {
	repo     *gogit.Repository
	repoPath string
	auth     transport.AuthMethod
}

// NewService opens a git repository at the given path.
//
// EnableDotGitCommonDir is required, not optional: the pipeline runs its stages
// inside linked worktrees, whose gitdir holds only HEAD, index, logs and refs —
// no objects, no config, no refs/remotes. Without common-dir support go-git
// chroots the storer to that directory and the repository reads as one with no
// remotes and no history.
//
// DetectDotGit stays OFF, and that is load-bearing for write containment
// (docs/MULTI_REPO_WORKSPACE.md#write-containment-issue-129) — not a
// consequence of callers always passing a repository root, which they do not
// (openGitService passes os.Getwd(), getGitRoot passes "."). With parent
// walking on, a path that is NOT a repository still opens: a stale or ghost
// worktree directory such as `<repo>/.worktrees/issue-999`, left behind when a
// run's worktree was removed, resolves upward to the ENCLOSING primary checkout
// and every mutation aimed at the dead worktree lands in the operator's own
// tree instead. Opening a non-repository path must fail, not widen; the
// negative test in service_test.go pins that and fails the moment DetectDotGit
// is turned on.
func NewService(repoPath string) (*Service, error) {
	repo, err := gogit.PlainOpenWithOptions(repoPath, &gogit.PlainOpenOptions{
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open repo at %s: %w", repoPath, err)
	}

	s := &Service{
		repo:     repo,
		repoPath: repoPath,
	}

	// Set up auth from GITHUB_TOKEN if available
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		s.auth = &http.BasicAuth{
			Username: "token",
			Password: token,
		}
	}

	return s, nil
}

// NewServiceFromRepo creates a service from an already-opened repository (for testing).
func NewServiceFromRepo(repo *gogit.Repository, repoPath string) *Service {
	return &Service{
		repo:     repo,
		repoPath: repoPath,
	}
}

// gitExec runs a git subcommand in this service's checkout and returns its
// combined output.
//
// Reads go through go-git; the MUTATIONS git itself guards go through git.
// That split is not a fallback and not a compat shim — it is which
// implementation is correct per operation. go-git implements none of git's
// worktree safety guards: it will move HEAD and create a branch ref and only
// then discover the tree is dirty (leaving both mutations behind), check out a
// branch a sibling worktree already holds, and delete a branch another worktree
// is sitting on. Every one of those became reachable the moment the service
// learned to resolve the common dir (#535), because before that a linked
// worktree simply read as an empty repository. Re-implementing git's guards in
// Go would be a second, weaker copy of rules git already enforces, so the
// mutating paths shell out and inherit them. This file already shells out for
// ls-remote, commitAll and the checkpoint anchor; this is that same idiom.
func (s *Service) gitExec(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// BranchInfo holds information about the current branch.
type BranchInfo struct {
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	IsHead bool   `json:"isHead"`
}

// CurrentBranch returns the name of the current branch.
func (s *Service) CurrentBranch() (string, error) {
	head, err := s.repo.Head()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	if !head.Name().IsBranch() {
		return "", fmt.Errorf("HEAD is detached at %s", head.Hash().String()[:8])
	}

	return head.Name().Short(), nil
}

// BranchCreate creates a new branch from current HEAD and checks it out.
//
// `git checkout -b` rather than SetReference + go-git checkout, for the
// atomicity reason spelled out on BranchCreateFrom.
func (s *Service) BranchCreate(name string) error {
	if _, err := s.gitExec("checkout", "-b", name); err != nil {
		return fmt.Errorf("create branch %s: %w", name, err)
	}
	return nil
}

// BranchCreateFrom creates a new branch from the specified base branch and checks it out.
//
// The base is resolved here (local branch first, then origin/<base>) and the
// resulting commit is handed to `git checkout -B`, which is atomic: git stages
// the working-tree update first and only writes the branch ref and HEAD once it
// succeeds. The go-git equivalent is not. In a linked worktree holding ANY
// modified tracked file — the normal state by the time a stage runs, since the
// extension runs `npm install` and codegen over tracked files before dispatch —
// go-git moved HEAD (worktree.go:190) and created refs/heads/<name> in the
// COMMON store, then failed the reset that rejects a dirty tree
// (worktree.go:201, ErrUnstagedChanges). The caller saw only a failure, while
// the worktree was left on a branch that half-existed and every retry took the
// "branch already exists" path and failed identically forever. git checks out a
// dirty tree the way `git checkout -B` always has: modifications that do not
// collide with the target are carried across and preserved.
func (s *Service) BranchCreateFrom(name, base string) error {
	baseHash, err := s.resolveBranchHash(base)
	if err != nil {
		return fmt.Errorf("resolve base branch %s: %w", base, err)
	}

	if _, err := s.gitExec("checkout", "-B", name, baseHash.String()); err != nil {
		return fmt.Errorf("create branch %s from %s: %w", name, base, err)
	}

	return nil
}

// LocalBranchExists reports whether the named local branch exists.
func (s *Service) LocalBranchExists(name string) (bool, error) {
	_, err := s.repo.Reference(plumbing.NewBranchReferenceName(name), true)
	if err == nil {
		return true, nil
	}
	if err == plumbing.ErrReferenceNotFound {
		return false, nil
	}
	return false, fmt.Errorf("lookup local branch %s: %w", name, err)
}

// RemoteBranchExists reports whether the named remote branch exists on origin.
func (s *Service) RemoteBranchExists(name string) (bool, error) {
	branches, err := s.ListRemoteBranches()
	if err != nil {
		return false, err
	}
	for _, branch := range branches {
		if branch == name {
			return true, nil
		}
	}
	return false, nil
}

// EnsureLocalBranchFromRemote creates a local branch reference from origin/<name> when needed.
func (s *Service) EnsureLocalBranchFromRemote(name string) error {
	exists, err := s.LocalBranchExists(name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	remoteRef, err := s.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+name), true)
	if err != nil {
		return fmt.Errorf("lookup remote branch origin/%s: %w", name, err)
	}

	localRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), remoteRef.Hash())
	if err := s.repo.Storer.SetReference(localRef); err != nil {
		return fmt.Errorf("create local branch %s from origin/%s: %w", name, name, err)
	}

	return nil
}

// ResetLocalBranchToRemote points the local branch ref at origin/<name>,
// creating it when absent and force-updating it when it already exists — e.g.
// a stale ref left by a prior pipeline run that has since diverged from the
// pushed tip. The caller must Fetch beforehand so refs/remotes/origin/<name>
// is current.
//
// This makes the remote authoritative on re-runs: the worktree starts from the
// already-pushed (and previously validated) commit, so later commits
// fast-forward and push cleanly. Without it, a stale diverged local branch is
// checked out as-is, the push is rejected as non-fast-forward, the force-push
// safety hook blocks the overwrite, and pr-create dead-ends with no PR.
func (s *Service) ResetLocalBranchToRemote(name string) error {
	remoteRef, err := s.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+name), true)
	if err != nil {
		return fmt.Errorf("lookup remote branch origin/%s: %w", name, err)
	}

	localRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), remoteRef.Hash())
	if err := s.repo.Storer.SetReference(localRef); err != nil {
		return fmt.Errorf("reset local branch %s to origin/%s: %w", name, name, err)
	}

	return nil
}

// ListLocalBranches returns all local branch names (excluding HEAD).
func (s *Service) ListLocalBranches() ([]string, error) {
	refs, err := s.repo.References()
	if err != nil {
		return nil, fmt.Errorf("list references: %w", err)
	}

	var branches []string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		if strings.HasPrefix(name, "refs/heads/") {
			branches = append(branches, strings.TrimPrefix(name, "refs/heads/"))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate branches: %w", err)
	}

	sort.Strings(branches)
	return branches, nil
}

// ListRemoteBranches returns all remote branch names (without the origin/ prefix).
// Uses git ls-remote via exec to leverage the system's SSH agent and credential helpers,
// avoiding go-git's auth limitations with SSH remotes.
func (s *Service) ListRemoteBranches() ([]string, error) {
	cmd := exec.Command("git", "ls-remote", "--heads", "origin")
	cmd.Dir = s.repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote: %w", err)
	}

	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		ref := parts[1]
		// Strip refs/heads/ prefix
		name := strings.TrimPrefix(ref, "refs/heads/")
		branches = append(branches, name)
	}

	sort.Strings(branches)
	return branches, nil
}

// BranchDelete deletes a local branch.
//
// `git branch -D`, so a branch another worktree has checked out is refused
// ("cannot delete branch 'x' used by worktree at …") instead of orphaning that
// worktree on an unborn HEAD with its commits reachable only through the
// reflog. Removing the ref through go-git had no such guard, and with
// common-dir resolution the branch being removed is the SHARED one every
// sibling worktree points at. git also drops the branch.<name>.* config as part
// of the delete, so nothing here has to rewrite the config file — which is why
// the go-git SetConfig round-trip (and the operator comments it discards) is
// gone.
func (s *Service) BranchDelete(name string) error {
	if _, err := s.gitExec("branch", "-D", name); err != nil {
		return fmt.Errorf("delete branch %s: %w", name, err)
	}

	return nil
}

// BranchDeleteRemote deletes a branch on the remote (origin) using a zero-hash push refspec.
func (s *Service) BranchDeleteRemote(name string) error {
	remote, err := s.repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("get remote: %w", err)
	}

	refSpec := config.RefSpec(":refs/heads/" + name)
	if err := remote.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       s.auth,
	}); err != nil {
		return fmt.Errorf("delete remote branch %s: %w", name, err)
	}

	return nil
}

// BranchCleanup deletes a branch both locally and on the remote.
// Skips errors for branches that don't exist (idempotent).
// Protected branches (main, master) are never deleted.
func (s *Service) BranchCleanup(name string) error {
	if name == "main" || name == "master" {
		return fmt.Errorf("refusing to delete protected branch %q", name)
	}

	var errs []string

	// Delete remote first (while local ref still exists for reference)
	if err := s.BranchDeleteRemote(name); err != nil {
		// Not fatal — branch may already be deleted on remote
		errs = append(errs, fmt.Sprintf("remote: %v", err))
	}

	// Delete local
	if err := s.BranchDelete(name); err != nil {
		errs = append(errs, fmt.Sprintf("local: %v", err))
	}

	// Prune stale remote-tracking refs
	_ = s.Fetch(true)

	if len(errs) == 2 {
		// Both failed — branch likely doesn't exist anywhere
		return fmt.Errorf("branch cleanup %s: %s", name, strings.Join(errs, "; "))
	}

	return nil
}

// FindEpicBranch searches remote branches for one matching the epic/<number>-* pattern.
func (s *Service) FindEpicBranch(epicNumber int) (string, error) {
	branches, err := s.ListRemoteBranches()
	if err != nil {
		return "", err
	}

	prefix := fmt.Sprintf("epic/%d-", epicNumber)
	for _, b := range branches {
		if strings.HasPrefix(b, prefix) {
			return b, nil
		}
	}

	return "", fmt.Errorf("no epic branch found matching epic/%d-*", epicNumber)
}

// Checkout switches to the specified branch.
//
// Through git, so the worktree-occupancy guard applies: a branch another linked
// worktree already has checked out is refused, naming the holding worktree.
// go-git has no such check — it never reads <common>/worktrees/*/HEAD — so it
// happily put two worktrees on one branch, and that is silent data loss rather
// than an oddity: the second stage's `git add -A && git commit` (commitAll)
// sees the first stage's files as deletions and erases its deliverable from the
// branch tip. The re-run paths of `nightgauge git branch-create` (the
// remoteExists and localExists cases) reach this on every retry.
func (s *Service) Checkout(branch string) error {
	if _, err := s.gitExec("checkout", branch); err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}

	return nil
}

// DefaultBranch returns the repository default branch, preferring origin/HEAD when available.
func (s *Service) DefaultBranch() (string, error) {
	if ref, err := s.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), false); err == nil {
		target := ref.Target().String()
		target = strings.TrimPrefix(target, "refs/remotes/origin/")
		if target != "" {
			return target, nil
		}
	}

	for _, candidate := range []string{"main", "master"} {
		if _, err := s.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+candidate), true); err == nil {
			return candidate, nil
		}
		if _, err := s.repo.Reference(plumbing.NewBranchReferenceName(candidate), true); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("cannot determine default branch: no origin/HEAD, main, or master ref found")
}

// Fetch fetches from remote with optional prune.
func (s *Service) Fetch(prune bool) error {
	opts := &gogit.FetchOptions{
		RemoteName: "origin",
		Auth:       s.auth,
	}
	if prune {
		opts.RefSpecs = []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
		}
		opts.Prune = true
	}

	if err := s.repo.Fetch(opts); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("fetch: %w", err)
	}

	return nil
}

// Push pushes the current branch to origin.
func (s *Service) Push() error {
	if err := s.repo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		Auth:       s.auth,
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("push: %w", err)
	}

	return nil
}

// PushBranch pushes the named branch to origin and sets upstream tracking.
func (s *Service) PushBranch(name string) error {
	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", name, name))
	if err := s.repo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       s.auth,
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("push branch %s: %w", name, err)
	}
	return nil
}

// StatusResult holds working tree status.
type StatusResult struct {
	IsClean        bool         `json:"isClean"`
	StagedFiles    []FileChange `json:"stagedFiles,omitempty"`
	UnstagedFiles  []FileChange `json:"unstagedFiles,omitempty"`
	UntrackedFiles []string     `json:"untrackedFiles,omitempty"`
}

// FileChange represents a file change in the working tree.
type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "modified", "added", "deleted", "renamed"
}

// Status returns the working tree status.
func (s *Service) Status() (*StatusResult, error) {
	wt, err := s.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}

	result := &StatusResult{
		IsClean: status.IsClean(),
	}

	for path, fileStatus := range status {
		switch {
		case fileStatus.Staging == gogit.Untracked:
			result.UntrackedFiles = append(result.UntrackedFiles, path)
		case fileStatus.Staging != gogit.Unmodified:
			result.StagedFiles = append(result.StagedFiles, FileChange{
				Path:   path,
				Status: statusCode(fileStatus.Staging),
			})
		}
		if fileStatus.Worktree != gogit.Unmodified && fileStatus.Worktree != gogit.Untracked {
			result.UnstagedFiles = append(result.UnstagedFiles, FileChange{
				Path:   path,
				Status: statusCode(fileStatus.Worktree),
			})
		}
	}

	sort.Strings(result.UntrackedFiles)
	sort.Slice(result.StagedFiles, func(i, j int) bool { return result.StagedFiles[i].Path < result.StagedFiles[j].Path })
	sort.Slice(result.UnstagedFiles, func(i, j int) bool { return result.UnstagedFiles[i].Path < result.UnstagedFiles[j].Path })

	return result, nil
}

// Commit stages all changes and creates a commit.
func (s *Service) Commit(message string) (string, error) {
	wt, err := s.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	// Stage all changes
	if _, err := wt.Add("."); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Nightgauge Pipeline",
			Email: "pipeline@nightgauge.dev",
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return hash.String(), nil
}

// LogEntry represents a single commit log entry.
type LogEntry struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// Log returns recent commit entries.
func (s *Service) Log(limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 10
	}

	iter, err := s.repo.Log(&gogit.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("get log: %w", err)
	}
	defer iter.Close()

	var entries []LogEntry
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if count >= limit {
			return fmt.Errorf("stop") // ForEach doesn't support early termination cleanly
		}
		entries = append(entries, LogEntry{
			Hash:    c.Hash.String()[:8],
			Message: strings.SplitN(c.Message, "\n", 2)[0],
			Author:  c.Author.Name,
			Date:    c.Author.When.Format("2006-01-02 15:04"),
		})
		count++
		return nil
	})
	// Ignore the "stop" error used for early termination
	if err != nil && err.Error() != "stop" {
		return nil, err
	}

	return entries, nil
}

// Diff returns the diff of unstaged changes.
func (s *Service) Diff() (string, error) {
	wt, err := s.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("get status: %w", err)
	}

	var diffParts []string
	for path, fileStatus := range status {
		if fileStatus.Worktree != gogit.Unmodified && fileStatus.Worktree != gogit.Untracked {
			diffParts = append(diffParts, fmt.Sprintf("--- %s [%s]", path, statusCode(fileStatus.Worktree)))
		}
	}

	sort.Strings(diffParts)
	if len(diffParts) == 0 {
		return "No changes.", nil
	}

	return strings.Join(diffParts, "\n"), nil
}

// AbortPipeline cleans up a pipeline branch: checks out main and deletes the feature branch.
//
// The delete goes through BranchDelete rather than a second inline copy of it,
// so the worktree-occupancy guard covers this path too: aborting one run must
// never yank the branch out from under a concurrent run's worktree.
func (s *Service) AbortPipeline(featureBranch string) error {
	// Checkout main first
	if err := s.Checkout("main"); err != nil {
		return fmt.Errorf("checkout main: %w", err)
	}

	return s.BranchDelete(featureBranch)
}

// ResetPipeline resets the working tree to a clean state (hard reset to HEAD).
//
// Before resetting, it preserves any unlanded deliverable a pipeline stage
// recorded: if `.nightgauge/pipeline/dev-{N}.json` lists created/modified
// files that are still present on disk and uncommitted, those changes are
// committed to the current branch as a recovery commit FIRST, so the reset
// below cannot discard them. A hard reset+clean called on a worktree holding
// a completed-but-uncommitted implementation is exactly the destroy-on-revert
// mechanism from Issue #289. Any baseline stash this branch is carrying
// (`<stage>-<issue>-baseline`, taken by the CI-gate baseline-failure
// detector) is also popped first so it is never silently left behind.
//
// The deliverable guard answers in three states, not two (#297). "There is
// nothing to preserve" and "I could not tell what this tree holds" used to
// share one `return nil` and therefore one consequence — reset anyway — so
// every input the guard failed to understand read as permission to destroy: a
// detached HEAD (which is how every pipeline worktree starts), a stray
// `temp-pre-push-<n>` branch, a handoff path resolved against the wrong root,
// a handoff written in a shape the struct did not model. An UNDETERMINED
// verdict now commits the whole tree as a checkpoint instead, and if even that
// fails ResetPipeline refuses to run rather than falling through to the
// destructive path.
func (s *Service) ResetPipeline() error {
	verdict, err := s.preserveUnlandedDeliverable()
	if err != nil {
		log.Printf("git: ResetPipeline: could not establish what the working tree holds: %v", err)
	}
	if verdict == preserveUndetermined {
		// Fail toward keeping the work. A checkpoint commit costs an operator
		// one `git reset HEAD~1`; the alternative cost #289 a completed
		// implementation and $14.84 of the run that produced it.
		if cerr := s.checkpointUncommitted(); cerr != nil {
			return fmt.Errorf("refusing to hard-reset %s: the working tree may hold unlanded pipeline "+
				"work and the safety checkpoint failed: %w", s.repoPath, cerr)
		}
	}
	if err := s.reclaimPipelineStashes(); err != nil {
		log.Printf("git: ResetPipeline: failed to reclaim pipeline stashes: %v", err)
	}

	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	// Reset and clean through git, because "untracked" and "IGNORED" are
	// different sets and only git knows the difference. go-git honours neither
	// .gitignore nor .git/info/exclude in either operation: its hard reset
	// diffs the tree with excludeIgnoredChanges=false and deletes every "extra"
	// path it finds, and its Clean(Dir:true) walks the same way — so both wipe
	// node_modules/, build caches and .env along with the junk. That is the
	// stage's own execution environment and possibly an operator secret, and it
	// was inert only while a linked worktree read as an empty repository; with
	// common-dir resolution it is reachable on every reset. `git reset --hard`
	// touches tracked content only and `git clean -fd` removes exactly what the
	// reset is for — untracked files and directories — leaving ignored paths
	// alone. Adding -x to the clean would restore the bug.
	if _, err := s.gitExec("reset", "--hard", head.Hash().String()); err != nil {
		return fmt.Errorf("reset: %w", err)
	}

	if _, err := s.gitExec("clean", "-fd"); err != nil {
		return fmt.Errorf("clean: %w", err)
	}

	return nil
}

// preserveVerdict is the guard's three-state answer to "does this working tree
// hold pipeline work that a hard reset would destroy?" (#297).
//
// The two-state version shipped with #289 collapsed "nothing is at risk" and
// "I could not tell" into a single `return nil`, and the caller read both as
// consent to hard-reset. Splitting them is the whole fix: only a positive
// determination may authorise destruction.
//
// preserveUndetermined is the zero value deliberately, so a path added later
// that returns without deciding fails toward keeping the work.
type preserveVerdict int

const (
	// preserveUndetermined: the guard could not establish what the tree holds.
	// Nothing may be destroyed on this verdict.
	preserveUndetermined preserveVerdict = iota
	// preserveNothingAtRisk: positively determined that no recorded deliverable
	// is sitting uncommitted. Resetting is safe.
	preserveNothingAtRisk
	// preservePreserved: a recorded deliverable was uncommitted and has been
	// committed as a recovery commit.
	preservePreserved
)

// issueNumberFromBranchRE extracts the issue number from the pipeline's
// standard feature-branch naming convention (feat/<N>-slug, fix/<N>-slug).
var issueNumberFromBranchRE = regexp.MustCompile(`^(?:feat|fix|docs)/(\d+)-`)

// issueNumberFromWorktreeRE matches the directory layout the worktree manager
// creates (`.nightgauge/worktrees/<repo>-issue-<N>`, see
// internal/execution/worktree.go). Worktrees are created with
// `git worktree add --detach`, so from creation until the dev skill creates
// `feat/<N>-…` the branch name carries no issue number at all and the path is
// the only thing that does — the exact window #289 was killed in.
var issueNumberFromWorktreeRE = regexp.MustCompile(`-issue-(\d+)$`)

// devHandoffFileRE extracts the issue number from a dev handoff filename.
var devHandoffFileRE = regexp.MustCompile(`^dev-(\d+)\.json$`)

// pipelineIssueNumber identifies which issue's handoff describes this checkout,
// trying every source that carries the number rather than the branch name
// alone. It reports an error — never a silent empty string — when no source
// answers, because "I don't know which run owns this tree" and "no run owns
// this tree" must not reach the caller as the same value.
func (s *Service) pipelineIssueNumber() (string, error) {
	branch, err := s.currentBranchName()
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}
	if m := issueNumberFromBranchRE.FindStringSubmatch(branch); m != nil {
		return m[1], nil
	}
	if m := issueNumberFromWorktreeRE.FindStringSubmatch(filepath.Base(s.repoPath)); m != nil {
		return m[1], nil
	}
	// Last resort: an unambiguous handoff in this checkout. Two or more and we
	// cannot say which run produced the dirty tree, so we decline rather than
	// guess — guessing wrong here commits one run's work under another's name.
	matches, globErr := filepath.Glob(filepath.Join(s.repoPath, ".nightgauge", "pipeline", "dev-*.json"))
	if globErr == nil && len(matches) == 1 {
		if m := devHandoffFileRE.FindStringSubmatch(filepath.Base(matches[0])); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("cannot identify the pipeline issue for checkout %s (branch %q, %d dev handoff(s) present)",
		s.repoPath, branch, len(matches))
}

// recordedDeliverableFiles reads the paths a dev handoff claims it produced.
//
// It accepts both shapes the pipeline has actually shipped: the documented
// object (`{"created": …, "modified": …, "deleted": …}`, see
// docs/CONTEXT_ARCHITECTURE.md) and the flat array of paths #240 wrote, which
// is recorded as a real production context in
// internal/orchestrator/gates/context_decode_test.go. Modelling only the object
// shape turned the flat variant into a parse error whose sole consequence was a
// log line — the reset then proceeded and took the deliverable with it.
func recordedDeliverableFiles(data []byte) ([]string, error) {
	var envelope struct {
		FilesChanged json.RawMessage `json:"files_changed"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode handoff: %w", err)
	}
	if len(envelope.FilesChanged) == 0 {
		return nil, fmt.Errorf("handoff has no files_changed field")
	}

	var obj struct {
		Created  []string `json:"created"`
		Modified []string `json:"modified"`
		Deleted  []string `json:"deleted"`
	}
	if err := json.Unmarshal(envelope.FilesChanged, &obj); err == nil {
		files := append([]string{}, obj.Created...)
		files = append(files, obj.Modified...)
		files = append(files, obj.Deleted...)
		return files, nil
	}

	var flat []string
	if err := json.Unmarshal(envelope.FilesChanged, &flat); err == nil {
		return flat, nil
	}

	return nil, fmt.Errorf("files_changed is neither an object nor an array of paths")
}

// preserveUnlandedDeliverable decides whether the current working tree holds
// pipeline work a hard reset would destroy, and commits it to the current
// branch as a recovery commit when it does (Issue #289 AC4).
//
// Every path that cannot answer returns preserveUndetermined with the reason,
// never a bare "nothing to preserve" (#297).
func (s *Service) preserveUnlandedDeliverable() (preserveVerdict, error) {
	status, err := s.Status()
	if err != nil {
		return preserveUndetermined, fmt.Errorf("status: %w", err)
	}
	if status.IsClean {
		// The one unconditionally honest "nothing to preserve": there is no
		// uncommitted content in this tree at all, whatever the branch name or
		// the handoff say.
		return preserveNothingAtRisk, nil
	}

	issue, err := s.pipelineIssueNumber()
	if err != nil {
		return preserveUndetermined, err
	}
	handoffPath := filepath.Join(s.repoPath, ".nightgauge", "pipeline", fmt.Sprintf("dev-%s.json", issue))
	data, err := os.ReadFile(handoffPath)
	if err != nil {
		// A dirty tree with no readable handoff is not "nothing to preserve" —
		// it is a tree whose contents nothing accounts for. A worktree-isolated
		// run whose Service was constructed on the main root looks exactly like
		// this, and its deliverable is real.
		return preserveUndetermined, fmt.Errorf("read %s: %w", handoffPath, err)
	}
	recorded, err := recordedDeliverableFiles(data)
	if err != nil {
		return preserveUndetermined, fmt.Errorf("parse %s: %w", handoffPath, err)
	}
	if len(recorded) == 0 {
		return preserveUndetermined, fmt.Errorf(
			"%s records no changed files, yet the working tree is dirty", handoffPath)
	}

	dirty := map[string]bool{}
	for _, f := range status.StagedFiles {
		dirty[f.Path] = true
	}
	for _, f := range status.UnstagedFiles {
		dirty[f.Path] = true
	}
	for _, f := range status.UntrackedFiles {
		dirty[f] = true
	}
	hasRecordedDeliverable := false
	for _, f := range recorded {
		if dirty[f] {
			hasRecordedDeliverable = true
			break
		}
	}
	if !hasRecordedDeliverable {
		// Positive determination: the run's own record of what it produced is
		// fully committed, so the dirt still in the tree is not the deliverable.
		return preserveNothingAtRisk, nil
	}

	if err := s.commitAll(fmt.Sprintf(
		"chore(#%s): preserve unlanded deliverable before pipeline reset", issue)); err != nil {
		return preserveUndetermined, err
	}
	log.Printf("git: ResetPipeline: preserved %d dev-context file(s) into a recovery commit for #%s before reset",
		len(recorded), issue)
	return preservePreserved, nil
}

// checkpointUncommitted commits the entire working tree so the hard reset that
// follows has nothing left to destroy. It is what an UNDETERMINED verdict buys:
// the tree still ends clean, which is ResetPipeline's contract, but the content
// ends up in history instead of in the bit bucket.
func (s *Service) checkpointUncommitted() error {
	return s.commitAll("chore: checkpoint uncommitted work before pipeline reset")
}

// commitAll stages and commits everything in the working tree, then makes sure
// the resulting commit is reachable by name.
func (s *Service) commitAll(message string) error {
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = s.repoPath
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add -A: %w: %s", err, string(out))
	}
	commitCmd := exec.Command("git", "commit", "-m", message)
	commitCmd.Dir = s.repoPath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, string(out))
	}
	return s.anchorDetachedHead()
}

// anchorDetachedHead points a branch at HEAD when the checkout is detached, so
// a commit made to rescue work is findable by name rather than surviving only
// in the reflog until gc collects it. Pipeline worktrees are created with
// `git worktree add --detach`, so for the reset path this is the common case,
// not the exotic one.
func (s *Service) anchorDetachedHead() error {
	branch, err := s.currentBranchName()
	if err != nil {
		return fmt.Errorf("read current branch: %w", err)
	}
	if branch != "" {
		return nil
	}
	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}
	name := fmt.Sprintf("nightgauge-checkpoint-%s", head.Hash().String()[:12])
	cmd := exec.Command("git", "branch", name)
	cmd.Dir = s.repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch %s: %w: %s", name, err, string(out))
	}
	log.Printf("git: anchored detached rescue commit on branch %s", name)
	return nil
}

// reclaimPipelineStashes discards every stash the pipeline created in this
// repo, so a reset never silently leaves one behind (Issue #289 AC5).
//
// Drop, not pop, and the distinction is only apparent here: ResetPipeline's
// next act is a hard reset + clean, so anything popped into the tree is wiped
// milliseconds later. What AC5 guards against is an ORPHANED stash surviving
// the reset untracked, and dropping achieves that without the failure mode
// popping has — restoring stash A dirties the tree, which makes stash B
// unpoppable, so a pop-based reclaim can only ever clear one of them. (The
// reconcile sweep, which runs when no reset follows, defaults to restore for
// exactly the opposite reason: there, the content is all there is.)
//
// This used to match `[a-z-]+-\d+-baseline` against the raw stash list — a
// convention NOTHING wrote. The two producers in the tree created a stash with
// no message at all (`git stash --include-untracked`) and one messaged
// `pre-baseline`, so the reclaim had been dead code against its own producers
// since it was written, and every stash they took leaked by construction
// (#330). The producers now emit reclaim.StashName and this asks the one
// classifier who owns what, rather than re-deriving a naming rule that can
// drift from the code that writes it.
//
// Scoped to pipeline-owned stashes: an operator's own `git stash` is never
// popped by a pipeline reset. All of them are reclaimed, not just the first —
// "top of stack" was never a property of a leak.
func (s *Service) reclaimPipelineStashes() error {
	res, err := reclaim.SweepPipelineStashes(reclaim.StashSweepOptions{
		RepoRoot: s.repoPath,
		Action:   reclaim.StashDrop,
	})
	if err != nil {
		return err
	}
	for _, r := range res.Reclaimed {
		log.Printf("git: ResetPipeline: dropped pipeline stash %s (%s) before reset", r.Ref, r.Message)
	}
	for _, sk := range res.Skipped {
		if sk.Reason == reclaim.StashSkipUnowned {
			continue // an operator's stash is not ours to report on every reset
		}
		log.Printf("git: ResetPipeline: left pipeline stash %s in place (%s) — run `nightgauge stash sweep`",
			sk.Ref, sk.Reason)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("stash reclaim: %s", strings.Join(res.Errors, "; "))
	}
	return nil
}

// currentBranchName returns the short name of the currently checked-out
// branch, or "" for a detached HEAD.
func (s *Service) currentBranchName() (string, error) {
	head, err := s.repo.Head()
	if err != nil {
		return "", err
	}
	if !head.Name().IsBranch() {
		return "", nil
	}
	return head.Name().Short(), nil
}

// InitRepo initializes a new git repository at the given path (for testing).
func InitRepo(path string) (*gogit.Repository, error) {
	return gogit.PlainInit(path, false)
}

// SetRemote adds an origin remote (for testing).
func (s *Service) SetRemote(url string) error {
	_, err := s.repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	return err
}

// EnsureEpicBranch creates the epic branch from origin/main if it does not already
// exist on the remote. Returns (branchName, created, error). It is idempotent:
// if any remote branch matching epic/<epicNumber>-* already exists, it returns
// that branch name with created=false. After creating and pushing, the original
// branch is restored so the caller's working tree is unaffected.
func (s *Service) EnsureEpicBranch(epicNumber int, epicTitle string) (string, bool, error) {
	// Check for existing remote epic branch (no-op if already there)
	existing, err := s.FindEpicBranch(epicNumber)
	if err == nil {
		// Branch already exists
		return existing, false, nil
	}

	// Generate the target branch name
	branchName := GenerateBranchSlug("epic", epicNumber, epicTitle)

	// Remember current branch to restore after checkout
	originalBranch, err := s.CurrentBranch()
	if err != nil {
		return "", false, fmt.Errorf("get current branch: %w", err)
	}

	// Determine base branch (DefaultBranch uses remote tracking refs)
	defaultBranch, err := s.DefaultBranch()
	if err != nil {
		defaultBranch = "main"
	}

	// Create branch locally from the default branch
	// resolveBranchHash accepts a plain branch name and checks local then remote refs
	if err := s.BranchCreateFrom(branchName, defaultBranch); err != nil {
		// BranchCreateFrom may check out the branch — ensure we restore even on failure
		_ = s.Checkout(originalBranch)
		return "", false, fmt.Errorf("create epic branch %s: %w", branchName, err)
	}

	// Push to remote
	if err := s.PushBranch(branchName); err != nil {
		_ = s.Checkout(originalBranch)
		return "", false, fmt.Errorf("push epic branch %s: %w", branchName, err)
	}

	// Restore original branch
	if err := s.Checkout(originalBranch); err != nil {
		return branchName, true, fmt.Errorf("restore branch %s after epic branch creation: %w", originalBranch, err)
	}

	return branchName, true, nil
}

// branchPrefixPriority is the deterministic priority list mapping label
// tokens to branch prefixes. The first token from this list found in the
// input label set wins, so the result is stable regardless of how GitHub
// orders labels in the API response.
var branchPrefixPriority = []struct {
	label  string
	prefix string
}{
	{"bug", "fix/"},
	{"documentation", "docs/"},
	{"docs", "docs/"},
	{"refactor", "refactor/"},
	{"test", "test/"},
	{"chore", "chore/"},
	{"maintenance", "chore/"},
}

// BranchPrefixFromLabels returns a branch prefix (with trailing slash)
// derived from the supplied labels. A leading "type:" namespace is stripped
// before matching, so both "bug" and "type:bug" resolve to "fix/". Returns
// "feat/" when no priority label matches.
func BranchPrefixFromLabels(labels []string) string {
	have := make(map[string]bool, len(labels))
	for _, raw := range labels {
		token := strings.ToLower(strings.TrimSpace(raw))
		token = strings.TrimPrefix(token, "type:")
		if token != "" {
			have[token] = true
		}
	}
	for _, entry := range branchPrefixPriority {
		if have[entry.label] {
			return entry.prefix
		}
	}
	return "feat/"
}

// GenerateBranchSlug creates a branch name from an issue number and title.
func GenerateBranchSlug(prefix string, number int, title string) string {
	slug := strings.ToLower(title)
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, slug)

	// Collapse multiple dashes
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")

	// Truncate to reasonable length
	if len(slug) > 50 {
		slug = slug[:50]
		slug = strings.TrimRight(slug, "-")
	}

	return fmt.Sprintf("%s/%d-%s", prefix, number, slug)
}

var branchIssuePattern = regexp.MustCompile(`^[^/]+/(\d+)-`)

// ParseIssueNumberFromBranch extracts the issue number from a branch name like feat/123-title.
func ParseIssueNumberFromBranch(name string) (int, bool) {
	match := branchIssuePattern.FindStringSubmatch(name)
	if len(match) != 2 {
		return 0, false
	}
	var number int
	for _, ch := range match[1] {
		number = number*10 + int(ch-'0')
	}
	return number, true
}

// RemoteRepoSlug returns the GitHub owner/repo from the origin remote URL.
func (s *Service) RemoteRepoSlug() (string, error) {
	remote, err := s.repo.Remote("origin")
	if err != nil {
		return "", fmt.Errorf("get remote: %w", err)
	}
	if remote.Config() == nil || len(remote.Config().URLs) == 0 {
		return "", fmt.Errorf("origin remote has no configured URLs")
	}
	return parseGitHubRemoteSlug(remote.Config().URLs[0])
}

// RepoPath returns the repository root path.
func (s *Service) RepoPath() string {
	return s.repoPath
}

// Root returns the git repository root directory (equivalent to `git rev-parse --show-toplevel`).
// It resolves the worktree root from go-git, falling back to the stored repoPath.
func (s *Service) Root() (string, error) {
	wt, err := s.repo.Worktree()
	if err != nil {
		// Bare repo or error — fall back to repoPath
		return s.repoPath, nil
	}
	return wt.Filesystem.Root(), nil
}

func (s *Service) resolveBranchHash(name string) (plumbing.Hash, error) {
	if ref, err := s.repo.Reference(plumbing.NewBranchReferenceName(name), true); err == nil {
		return ref.Hash(), nil
	}
	if ref, err := s.repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+name), true); err == nil {
		return ref.Hash(), nil
	}
	return plumbing.ZeroHash, fmt.Errorf("branch %s not found locally or on origin", name)
}

func parseGitHubRemoteSlug(url string) (string, error) {
	normalized := strings.TrimSuffix(url, ".git")

	switch {
	case strings.HasPrefix(normalized, "git@github.com:"):
		return strings.TrimPrefix(normalized, "git@github.com:"), nil
	case strings.HasPrefix(normalized, "ssh://git@github.com/"):
		return strings.TrimPrefix(normalized, "ssh://git@github.com/"), nil
	case strings.HasPrefix(normalized, "https://github.com/"):
		return strings.TrimPrefix(normalized, "https://github.com/"), nil
	case strings.HasPrefix(normalized, "http://github.com/"):
		return strings.TrimPrefix(normalized, "http://github.com/"), nil
	default:
		return "", fmt.Errorf("unsupported GitHub remote URL: %s", url)
	}
}

// statusCode converts a go-git status code to a human-readable string.
func statusCode(code gogit.StatusCode) string {
	switch code {
	case gogit.Modified:
		return "modified"
	case gogit.Added:
		return "added"
	case gogit.Deleted:
		return "deleted"
	case gogit.Renamed:
		return "renamed"
	case gogit.Copied:
		return "copied"
	case gogit.Untracked:
		return "untracked"
	default:
		return "unknown"
	}
}

// CreateInitialCommit creates an initial commit with a .gitkeep file (for testing).
func CreateInitialCommit(repo *gogit.Repository, repoPath string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}

	keepPath := filepath.Join(repoPath, ".gitkeep")
	if err := os.WriteFile(keepPath, []byte(""), 0644); err != nil {
		return err
	}

	if _, err := wt.Add(".gitkeep"); err != nil {
		return err
	}

	_, err = wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	return err
}
