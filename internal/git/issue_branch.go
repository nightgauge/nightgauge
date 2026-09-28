package git

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// IssueBranchResult reports what EnsureIssueBranch did.
type IssueBranchResult struct {
	Branch      string
	BaseBranch  string
	Action      string // "created" | "reused-remote" | "already-exists"
	ParentIssue int
	EpicBranch  string
}

// EnsureIssueBranch creates (or reuses) branchName and checks it out. It is
// the one implementation behind `nightgauge git branch-create` and the
// scheduler's deterministic issue-pickup runner (#1904), so both produce the
// same branch from the same base.
//
// parentIssue != 0 bases the branch on the parent's epic branch; when no epic
// branch exists yet, epicTitle is asked for the parent's title and the epic
// branch is created from the default branch and pushed.
//
// The REMOTE is authoritative: when a prior run already pushed branchName, the
// local ref is reset to origin/<branch> even when a stale local ref exists, so
// a re-run continues from the pushed tip (issue 3881 in the pre-rename tracker).
//
// One issue has one branch (#1901): when a local or remote branch for the same
// issue already exists under another name — an earlier dispatch composed it
// from a different title or rule — that branch is reused instead of a second
// one being created beside it. See ExistingIssueBranch.
func (s *Service) EnsureIssueBranch(branchName string, parentIssue int, epicTitle func() (string, error)) (IssueBranchResult, error) {
	res := IssueBranchResult{Branch: branchName, Action: "created", ParentIssue: parentIssue}

	existing, err := s.ExistingIssueBranch(branchName)
	if err != nil {
		return res, err
	}
	branchName = existing
	res.Branch = branchName

	baseBranch, err := s.CurrentBranch()
	if err != nil {
		// Worktrees use detached HEAD by design; fall back to repo default.
		baseBranch, err = s.DefaultBranch()
		if err != nil {
			return res, err
		}
	}

	if parentIssue != 0 {
		if err := s.Fetch(true); err != nil {
			return res, err
		}
		epicBranch, err := s.FindEpicBranch(parentIssue)
		if err != nil {
			if epicTitle == nil {
				return res, err
			}
			title, titleErr := epicTitle()
			if titleErr != nil {
				return res, titleErr
			}
			epicBranch, err = GenerateBranchSlug("epic", parentIssue, title)
			if err != nil {
				return res, err
			}
			defaultBranch, defaultErr := s.DefaultBranch()
			if defaultErr != nil {
				return res, defaultErr
			}
			localExists, localErr := s.LocalBranchExists(epicBranch)
			if localErr != nil {
				return res, localErr
			}
			if !localExists {
				if err := s.BranchCreateFrom(epicBranch, defaultBranch); err != nil {
					return res, err
				}
			} else if err := s.Checkout(epicBranch); err != nil {
				return res, err
			}
			if err := s.PushBranch(epicBranch); err != nil {
				return res, err
			}
		}
		res.EpicBranch = epicBranch
		baseBranch = epicBranch
	}
	res.BaseBranch = baseBranch

	remoteExists, err := s.RemoteBranchExists(branchName)
	if err != nil {
		return res, err
	}
	localExists, err := s.LocalBranchExists(branchName)
	if err != nil {
		return res, err
	}

	switch {
	case remoteExists:
		if err := s.Fetch(true); err != nil {
			return res, err
		}
		if err := s.ResetLocalBranchToRemote(branchName); err != nil {
			// #1499: refused when another worktree has the branch checked out.
			var held *BranchHeldByWorktreeError
			if errors.As(err, &held) {
				return res, fmt.Errorf(
					"cannot reuse remote branch %s: %w — that checkout must move its own tree "+
						"(git -C %s pull --ff-only), or the run must use a different branch",
					branchName, err, held.Worktree)
			}
			return res, err
		}
		if err := s.Checkout(branchName); err != nil {
			return res, err
		}
		res.Action = "reused-remote"
	case localExists:
		if err := s.Checkout(branchName); err != nil {
			return res, err
		}
		res.Action = "already-exists"
	default:
		// Always use the resolved base. In a detached pipeline worktree,
		// BranchCreate would branch from the detached checkout even though
		// DefaultBranch above resolved origin's base branch.
		if err := s.BranchCreateFrom(branchName, baseBranch); err != nil {
			return res, err
		}
	}
	return res, nil
}

// ExistingIssueBranch returns the branch a dispatch for composed's issue must
// use. composed is the freshly composed name (`<prefix>/<N>-<slug>`).
//
// Every local and origin branch under an issue-numbered prefix
// (IssueNumberedCleanupPrefixes, minus epic/) whose number is N is a branch
// for this issue. The composed name wins when it is among them or when there
// are none; a single existing branch under another name is returned so the
// re-dispatch continues on it (#1901: a retried dispatch used to create a
// second, differently-truncated branch and orphan the first on origin).
// Several differently-named branches for one issue is an error naming them —
// picking one silently would strand the others' commits.
func (s *Service) ExistingIssueBranch(composed string) (string, error) {
	number, ok := ParseIssueNumberFromBranch(composed)
	if !ok {
		return composed, nil
	}
	local, err := s.ListLocalBranches()
	if err != nil {
		return "", err
	}
	remote, err := s.ListRemoteBranches()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, name := range append(local, remote...) {
		if !isIssueBranchFor(name, number) {
			continue
		}
		seen[name] = true
	}
	if len(seen) == 0 || seen[composed] {
		return composed, nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("issue #%d already has %d branches (%s): reconcile them to one before "+
		"re-dispatching — a new branch %s would be a third", number, len(names),
		strings.Join(names, ", "), composed)
}

func isIssueBranchFor(name string, number int) bool {
	if strings.HasPrefix(name, "epic/") || !IsCleanupCandidate(name) {
		return false
	}
	n, ok := ParseIssueNumberFromBranch(name)
	return ok && n == number
}
