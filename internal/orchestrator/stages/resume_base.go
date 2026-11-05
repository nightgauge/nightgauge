package stages

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

// BaseRefresh is what RefreshResumedBase did to a resumed worktree (#2897).
type BaseRefresh struct {
	// Base is the branch merged in, without the origin/ prefix.
	Base string `json:"base"`
	// Behind and Ahead are counted against origin/<Base> after the fetch and
	// before the merge.
	Behind int `json:"behind"`
	Ahead  int `json:"ahead"`
	// Outcome is one of the git.BaseMerge* values.
	Outcome string `json:"outcome"`
	// ConflictFiles names the conflicted paths: the files that stopped the
	// merge, or the CHANGELOGs that were resolved.
	ConflictFiles []string `json:"conflict_files,omitempty"`
	// Reason is git's own explanation when the merge stopped without
	// conflicted paths (an untracked file in the way, an autostash that would
	// not reapply).
	Reason string `json:"reason,omitempty"`
}

// Advanced reports whether the refresh moved the branch onto a newer base.
func (r BaseRefresh) Advanced() bool {
	return r.Outcome == gitpkg.BaseMergeMerged || r.Outcome == gitpkg.BaseMergeChangelogResolved
}

// RefreshResumedBase brings a resumed run worktree up to date with
// origin/<base> before its stages run (#2897). A re-dispatch reuses the
// surviving worktree as it was, so a blocker that has since merged to the base
// stays invisible and the run re-blocks on work that already landed.
//
// It fetches origin/<base>, counts how far HEAD is behind and ahead, and when
// behind merges in place with --autostash, so uncommitted work survives
// without touching the stash list every worktree shares. A branch with no
// commits of its own fast-forwards: a merge commit there would read as one
// commit of work to ResolvePreservedResume and skip planning and dev on the
// next dispatch. It never rebases and never pushes (#2699).
//
// Conflicts confined to CHANGELOG.md files are resolved as a union
// (git.ResolveChangelogUnion, #2560). Any other conflict aborts the merge,
// which restores HEAD and the autostashed edits, and returns
// git.BaseMergeConflict naming the files. An error means the refresh could not
// be attempted (no branch, another issue's branch, a failed fetch); the tree is
// then as it was found.
func RefreshResumedBase(ctx context.Context, workdir string, issueNumber int) (BaseRefresh, error) {
	res := BaseRefresh{Base: recordedBaseBranch(workdir, issueNumber)}
	branch, err := gitOutput(ctx, workdir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return res, errors.New("HEAD is not on a branch")
	}
	if !regexp.MustCompile(fmt.Sprintf(`^[^/]+/%d-`, issueNumber)).MatchString(branch) {
		return res, fmt.Errorf("branch %s is not issue #%d's", branch, issueNumber)
	}
	if mergeInProgress(ctx, workdir) {
		return res, errors.New("a merge is already in progress")
	}
	if out, err := gitCombined(ctx, workdir, "fetch", "--quiet", "origin", res.Base); err != nil {
		return res, fmt.Errorf("fetch origin %s: %v: %s", res.Base, err, firstLine(out))
	}
	remote := "origin/" + res.Base
	if res.Behind, err = revCount(ctx, workdir, "HEAD.."+remote); err != nil {
		return res, fmt.Errorf("count commits behind %s: %w", remote, err)
	}
	if res.Ahead, err = revCount(ctx, workdir, remote+"..HEAD"); err != nil {
		return res, fmt.Errorf("count commits ahead of %s: %w", remote, err)
	}
	if res.Behind == 0 {
		res.Outcome = gitpkg.BaseMergeUpToDate
		return res, nil
	}

	// An autostash that cannot reapply leaves the edits in a stash entry on
	// the list every worktree shares, so a dirty file the merge would touch
	// stops the refresh before anything is stashed.
	if overlap, err := dirtyOverlap(ctx, workdir, remote); err != nil {
		return res, fmt.Errorf("compare uncommitted work with %s: %w", remote, err)
	} else if len(overlap) > 0 {
		res.ConflictFiles = overlap
		return stopped(res, "uncommitted changes touch files the base also changed"), nil
	}

	msg := fmt.Sprintf("Merge %s into %s", remote, branch)
	args := []string{"merge", "--autostash", "--no-edit", "-m", msg, remote}
	if res.Ahead == 0 {
		args = []string{"merge", "--autostash", "--ff-only", remote}
	}
	out, mergeErr := gitCombined(ctx, workdir, args...)
	if mergeErr == nil {
		if why := autostashLeftover(out); why != "" {
			return stopped(res, why), nil
		}
		res.Outcome = gitpkg.BaseMergeMerged
		return res, nil
	}

	conflicts, err := unmergedPaths(ctx, workdir)
	if err != nil || len(conflicts) == 0 {
		abortMerge(ctx, workdir)
		if err != nil {
			return res, fmt.Errorf("list conflicts: %w", err)
		}
		return stopped(res, firstLine(out)), nil
	}
	res.ConflictFiles = conflicts
	for _, p := range conflicts {
		if filepath.Base(p) != "CHANGELOG.md" {
			abortMerge(ctx, workdir)
			res.Outcome = gitpkg.BaseMergeConflict
			return res, nil
		}
	}
	for _, p := range conflicts {
		if err := resolveChangelog(ctx, workdir, p); err != nil {
			abortMerge(ctx, workdir)
			res.Outcome = gitpkg.BaseMergeConflict
			res.Reason = err.Error()
			return res, nil
		}
	}
	out, err = gitCombined(ctx, workdir, "commit", "--no-edit", "-m", msg)
	if err != nil {
		abortMerge(ctx, workdir)
		return stopped(res, firstLine(out)), nil
	}
	if why := autostashLeftover(out); why != "" {
		return stopped(res, why), nil
	}
	res.Outcome = gitpkg.BaseMergeChangelogResolved
	return res, nil
}

// stopped books a merge git refused without naming a conflicted path.
func stopped(res BaseRefresh, why string) BaseRefresh {
	res.Outcome = gitpkg.BaseMergeConflict
	res.Reason = why
	return res
}

// autostashLeftover names an autostash git could not reapply. git then keeps
// the edits in a stash entry, which is shared with every worktree, so the run
// must stop rather than continue on a tree missing them.
func autostashLeftover(out string) string {
	if strings.Contains(out, "Applying autostash resulted in conflicts") {
		return "the uncommitted changes conflict with the merged base and were left in a stash entry"
	}
	return ""
}

// dirtyOverlap lists the uncommitted or untracked paths that remote changed
// since it forked from HEAD.
func dirtyOverlap(ctx context.Context, workdir, remote string) ([]string, error) {
	// Untrimmed: a porcelain entry's leading status column may be a space.
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z", "--untracked-files=all")
	cmd.Dir = workdir
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	dirty := map[string]bool{}
	entries := strings.Split(string(raw), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		dirty[e[3:]] = true
		if e[0] == 'R' || e[0] == 'C' {
			i++ // the rename's source path follows
			if i < len(entries) {
				dirty[entries[i]] = true
			}
		}
	}
	changed, err := gitOutput(ctx, workdir, "diff", "--name-only", "-z", "HEAD..."+remote)
	if err != nil {
		return nil, err
	}
	var overlap []string
	for _, p := range strings.Split(changed, "\x00") {
		if p != "" && dirty[p] {
			overlap = append(overlap, p)
		}
	}
	return overlap, nil
}

func mergeInProgress(ctx context.Context, workdir string) bool {
	path, err := gitOutput(ctx, workdir, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func abortMerge(ctx context.Context, workdir string) {
	if !mergeInProgress(ctx, workdir) {
		return
	}
	if out, err := gitCombined(ctx, workdir, "merge", "--abort"); err != nil {
		log.Printf("resume: could not abort the base merge in %s: %v: %s", workdir, err, firstLine(out))
	}
}

func unmergedPaths(ctx context.Context, workdir string) ([]string, error) {
	out, err := gitOutput(ctx, workdir, "diff", "--name-only", "--diff-filter=U", "-z")
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

func resolveChangelog(ctx context.Context, workdir, rel string) error {
	path := filepath.Join(workdir, rel)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	merged, err := gitpkg.ResolveChangelogUnion(content)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if out, err := gitCombined(ctx, workdir, "add", "--", rel); err != nil {
		return fmt.Errorf("stage %s: %v: %s", rel, err, firstLine(out))
	}
	return nil
}

func revCount(ctx context.Context, workdir, rng string) (int, error) {
	out, err := gitOutput(ctx, workdir, "rev-list", "--count", rng)
	if err != nil {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func gitCombined(ctx context.Context, workdir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
