// Package git provides in-process git operations using go-git,
// eliminating the need for the system git binary.
package git

import (
	"fmt"
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
)

// Service provides git operations on a repository.
type Service struct {
	repo     *gogit.Repository
	repoPath string
	auth     transport.AuthMethod
}

// NewService opens a git repository at the given path.
func NewService(repoPath string) (*Service, error) {
	repo, err := gogit.PlainOpen(repoPath)
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
func (s *Service) BranchCreate(name string) error {
	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	ref := plumbing.NewBranchReferenceName(name)
	branchRef := plumbing.NewHashReference(ref, head.Hash())
	if err := s.repo.Storer.SetReference(branchRef); err != nil {
		return fmt.Errorf("create branch %s: %w", name, err)
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: ref}); err != nil {
		return fmt.Errorf("checkout %s: %w", name, err)
	}

	return nil
}

// BranchCreateFrom creates a new branch from the specified base branch and checks it out.
func (s *Service) BranchCreateFrom(name, base string) error {
	baseHash, err := s.resolveBranchHash(base)
	if err != nil {
		return fmt.Errorf("resolve base branch %s: %w", base, err)
	}

	ref := plumbing.NewBranchReferenceName(name)
	branchRef := plumbing.NewHashReference(ref, baseHash)
	if err := s.repo.Storer.SetReference(branchRef); err != nil {
		return fmt.Errorf("create branch %s from %s: %w", name, base, err)
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: ref}); err != nil {
		return fmt.Errorf("checkout %s: %w", name, err)
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
func (s *Service) BranchDelete(name string) error {
	ref := plumbing.NewBranchReferenceName(name)
	if err := s.repo.Storer.RemoveReference(ref); err != nil {
		return fmt.Errorf("delete branch %s: %w", name, err)
	}

	// Also remove from branch config
	cfg, err := s.repo.Config()
	if err == nil {
		delete(cfg.Branches, name)
		_ = s.repo.SetConfig(cfg)
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
func (s *Service) Checkout(branch string) error {
	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	ref := plumbing.NewBranchReferenceName(branch)
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: ref}); err != nil {
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
func (s *Service) AbortPipeline(featureBranch string) error {
	// Checkout main first
	if err := s.Checkout("main"); err != nil {
		return fmt.Errorf("checkout main: %w", err)
	}

	// Delete the feature branch locally
	ref := plumbing.NewBranchReferenceName(featureBranch)
	if err := s.repo.Storer.RemoveReference(ref); err != nil {
		return fmt.Errorf("delete branch %s: %w", featureBranch, err)
	}

	// Also remove from branch config
	cfg, err := s.repo.Config()
	if err == nil {
		delete(cfg.Branches, featureBranch)
		_ = s.repo.SetConfig(cfg)
	}

	return nil
}

// ResetPipeline resets the working tree to a clean state (hard reset to HEAD).
func (s *Service) ResetPipeline() error {
	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	if err := wt.Reset(&gogit.ResetOptions{
		Mode:   gogit.HardReset,
		Commit: head.Hash(),
	}); err != nil {
		return fmt.Errorf("reset: %w", err)
	}

	// Clean untracked files
	if err := wt.Clean(&gogit.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean: %w", err)
	}

	return nil
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
