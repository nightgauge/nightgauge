package git

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Epic integration primitives (#2560).
//
// An epic branch collects its sub-issues' PRs with the MERGE strategy, so it is
// shared history. Bringing it up to date with its base therefore MERGES the
// base into it rather than rebasing it: a rebase would rewrite the sub-issue
// merge commits and need a force-push of a branch other runs read.
//
// The merge happens in a temporary detached linked worktree, never in the
// operator's checkout, and the worktree is removed on every return path.

// Base-merge outcomes reported by MergeBaseIntoBranch.
const (
	// BaseMergeUpToDate: the branch already contains the base; nothing pushed.
	BaseMergeUpToDate = "up_to_date"
	// BaseMergeMerged: the base merged cleanly and the result was pushed.
	BaseMergeMerged = "merged"
	// BaseMergeChangelogResolved: only CHANGELOG.md files conflicted; they were
	// resolved by union and the result was pushed.
	BaseMergeChangelogResolved = "changelog_resolved"
	// BaseMergeConflict: a non-CHANGELOG path conflicted; the merge was aborted
	// and nothing was pushed.
	BaseMergeConflict = "conflict"
)

// BaseMergeResult is the outcome of MergeBaseIntoBranch.
type BaseMergeResult struct {
	Outcome string
	// ConflictFiles lists the paths that conflicted, sorted as git reports
	// them. Set for BaseMergeConflict and BaseMergeChangelogResolved.
	ConflictFiles []string
}

// EpicBranchAhead reports how many commits origin/<branch> has that
// origin/<base> does not, after fetching origin. Zero means the branch is
// already contained in the base.
func (s *Service) EpicBranchAhead(branch, base string) (int, error) {
	for _, ref := range []string{branch, base} {
		if err := validateRefArg("branch", ref); err != nil {
			return 0, err
		}
	}
	if err := s.Fetch(false); err != nil {
		return 0, err
	}
	out, err := s.gitExec("rev-list", "--count", "origin/"+base+"..origin/"+branch)
	if err != nil {
		return 0, fmt.Errorf("count %s commits not on %s: %w", branch, base, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("count %s commits not on %s: unexpected output %q", branch, base, out)
	}
	return n, nil
}

// MergeBaseIntoBranch merges origin/<base> into origin/<branch> and pushes the
// result to refs/heads/<branch>. Conflicts confined to files named
// CHANGELOG.md are resolved with ResolveChangelogUnion; any other conflict
// aborts the merge, pushes nothing, and returns BaseMergeConflict with the
// conflicting paths. Call EpicBranchAhead (or Fetch) first so the
// remote-tracking refs are current.
func (s *Service) MergeBaseIntoBranch(branch, base string) (*BaseMergeResult, error) {
	for _, ref := range []string{branch, base} {
		if err := validateRefArg("branch", ref); err != nil {
			return nil, err
		}
	}

	dir, err := os.MkdirTemp("", "nightgauge-epic-integrate-")
	if err != nil {
		return nil, fmt.Errorf("epic integrate: temp dir: %w", err)
	}
	// git worktree add wants a path that does not exist yet.
	wt := filepath.Join(dir, "wt")
	if _, err := s.gitExec("worktree", "add", "--detach", wt, "origin/"+branch); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("epic integrate: add worktree: %w", err)
	}
	defer func() {
		_, _ = s.gitExec("worktree", "remove", "--force", wt)
		_ = os.RemoveAll(dir)
		_, _ = s.gitExec("worktree", "prune")
	}()
	w := &Service{repo: s.repo, repoPath: wt}

	before, err := w.gitExec("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("epic integrate: %w", err)
	}
	msg := fmt.Sprintf("Merge %s into %s", base, branch)
	result := &BaseMergeResult{}
	if _, mergeErr := w.gitExec("merge", "--no-ff", "--no-edit", "-m", msg, "origin/"+base); mergeErr != nil {
		conflicts, err := w.unmergedPaths()
		if err != nil || len(conflicts) == 0 {
			_, _ = w.gitExec("merge", "--abort")
			if err != nil {
				return nil, fmt.Errorf("epic integrate: list conflicts: %w", err)
			}
			return nil, fmt.Errorf("epic integrate: merge %s: %w", base, mergeErr)
		}
		result.ConflictFiles = conflicts
		for _, p := range conflicts {
			if filepath.Base(p) != "CHANGELOG.md" {
				_, _ = w.gitExec("merge", "--abort")
				result.Outcome = BaseMergeConflict
				return result, nil
			}
		}
		for _, p := range conflicts {
			if err := w.resolveChangelogFile(p); err != nil {
				_, _ = w.gitExec("merge", "--abort")
				return nil, err
			}
		}
		if _, err := w.gitExec("commit", "--no-edit", "-m", msg); err != nil {
			_, _ = w.gitExec("merge", "--abort")
			return nil, fmt.Errorf("epic integrate: commit resolution: %w", err)
		}
		result.Outcome = BaseMergeChangelogResolved
	}

	after, err := w.gitExec("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("epic integrate: %w", err)
	}
	if strings.TrimSpace(after) == strings.TrimSpace(before) {
		result.Outcome = BaseMergeUpToDate
		return result, nil
	}
	if result.Outcome == "" {
		result.Outcome = BaseMergeMerged
	}
	// A plain (non-forced) push: a concurrent writer makes it fail instead of
	// being overwritten.
	if _, err := w.gitExec("push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("epic integrate: push %s: %w", branch, err)
	}
	return result, nil
}

func (s *Service) unmergedPaths() ([]string, error) {
	out, err := s.gitExec("diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func (s *Service) resolveChangelogFile(rel string) error {
	path := filepath.Join(s.repoPath, rel)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("epic integrate: read %s: %w", rel, err)
	}
	merged, err := ResolveChangelogUnion(content)
	if err != nil {
		return fmt.Errorf("epic integrate: %s: %w", rel, err)
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return fmt.Errorf("epic integrate: write %s: %w", rel, err)
	}
	if _, err := s.gitExec("add", "--", rel); err != nil {
		return fmt.Errorf("epic integrate: stage %s: %w", rel, err)
	}
	return nil
}

// ResolveChangelogUnion resolves every conflict hunk in a CHANGELOG that git
// left with merge markers. For each hunk it keeps the second side ("theirs",
// the base being merged in) first, then the lines of the first side ("ours",
// the epic branch) that the base side does not already contain. A diff3 base
// section (|||||||) is dropped. Keep-a-Changelog bullet lists under the same
// heading therefore end up holding both sides' entries. Malformed markers
// return an error rather than a guess.
func ResolveChangelogUnion(content []byte) ([]byte, error) {
	const (
		outside = iota
		inOurs
		inBase
		inTheirs
	)
	lines := bytes.SplitAfter(content, []byte("\n"))
	var out bytes.Buffer
	var ours, theirs [][]byte
	state := outside
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		switch {
		case bytes.HasPrefix(trimmed, []byte("<<<<<<<")):
			if state != outside {
				return nil, fmt.Errorf("line %d: nested conflict start marker", i+1)
			}
			state, ours, theirs = inOurs, nil, nil
		case bytes.HasPrefix(trimmed, []byte("|||||||")):
			if state != inOurs {
				return nil, fmt.Errorf("line %d: unexpected base marker", i+1)
			}
			state = inBase
		case bytes.Equal(trimmed, []byte("=======")) && (state == inOurs || state == inBase):
			state = inTheirs
		case bytes.HasPrefix(trimmed, []byte(">>>>>>>")):
			if state != inTheirs {
				return nil, fmt.Errorf("line %d: unexpected conflict end marker", i+1)
			}
			seen := make(map[string]bool, len(theirs))
			for _, l := range theirs {
				out.Write(l)
				seen[string(bytes.TrimRight(l, "\r\n"))] = true
			}
			for _, l := range ours {
				key := string(bytes.TrimRight(l, "\r\n"))
				if seen[key] {
					continue
				}
				seen[key] = true
				out.Write(l)
			}
			state = outside
		default:
			switch state {
			case outside:
				out.Write(line)
			case inOurs:
				ours = append(ours, line)
			case inTheirs:
				theirs = append(theirs, line)
			}
		}
	}
	if state != outside {
		return nil, fmt.Errorf("unterminated conflict hunk")
	}
	return out.Bytes(), nil
}
