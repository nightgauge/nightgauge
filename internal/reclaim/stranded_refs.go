package reclaim

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Stranded fork refs (#2753). When a run ends branch_forked, the orchestrator
// pins its verified tip at refs/nightgauge/stranded/<issue> (#2699) so branch
// and worktree cleanup cannot orphan it. The ref lives outside refs/heads/, so
// nothing that cleans branches ever sees it. This file is its reader and its
// only deleter.

// StrandedRefNamespace is the ref prefix the orchestrator's
// PreserveStrandedTip writes to. Writer and reader share this one constant.
const StrandedRefNamespace = "refs/nightgauge/stranded/"

// StrandedRef is one pinned tip of a branch_forked run.
type StrandedRef struct {
	// Ref is the full ref name, e.g. refs/nightgauge/stranded/2753.
	Ref string `json:"ref"`
	// Commit is the pinned commit's SHA.
	Commit string `json:"commit"`
	// Issue is the issue the suffix names, 0 when it is not a positive integer.
	Issue int `json:"issue,omitempty"`
}

// StrandedRefName names the ref that pins issue's stranded tip.
func StrandedRefName(issue int) string {
	return fmt.Sprintf("%s%d", StrandedRefNamespace, issue)
}

// ListStrandedRefs enumerates every stranded ref in a repository, sorted by
// ref name. No refs is an empty slice and no error; a failing git is an error,
// which callers must read as "could not find out", never as "none" (#323).
func ListStrandedRefs(repoRoot string) ([]StrandedRef, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("list stranded refs: repo root is required")
	}
	out, err := exec.Command("git", "-C", repoRoot, "for-each-ref", "--sort=refname",
		"--format=%(refname) %(objectname)", StrandedRefNamespace).Output()
	if err != nil {
		return nil, fmt.Errorf("list stranded refs in %s: %w", repoRoot, err)
	}
	refs := []StrandedRef{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		r := StrandedRef{Ref: fields[0], Commit: fields[1]}
		// Only a ref the writer could have produced is issue-bound: "+5" or
		// "0005" parse as 5 but were never written by PreserveStrandedTip.
		if n, err := strconv.Atoi(strings.TrimPrefix(fields[0], StrandedRefNamespace)); err == nil && n > 0 &&
			StrandedRefName(n) == fields[0] {
			r.Issue = n
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// ResolveStrandedRef returns the commit a stranded ref points at, "" when the
// ref does not exist. A ref outside the namespace is an error.
func ResolveStrandedRef(repoRoot, ref string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("repo root is required")
	}
	if !strings.HasPrefix(ref, StrandedRefNamespace) || ref == StrandedRefNamespace {
		return "", fmt.Errorf("%s is not under %s", ref, StrandedRefNamespace)
	}
	out, err := exec.Command("git", "-C", repoRoot, "for-each-ref", "--format=%(refname) %(objectname)", ref).Output()
	if err != nil {
		return "", fmt.Errorf("resolve %s in %s: %w", ref, repoRoot, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == ref {
			return f[1], nil
		}
	}
	return "", nil
}

// DeleteStrandedRef deletes ref only while it still points at expectSHA
// (git's compare-and-delete), and refuses a ref outside the namespace.
func DeleteStrandedRef(repoRoot, ref, expectSHA string) error {
	if repoRoot == "" {
		return fmt.Errorf("repo root is required")
	}
	if !strings.HasPrefix(ref, StrandedRefNamespace) || ref == StrandedRefNamespace {
		return fmt.Errorf("refusing to delete %s: not under %s", ref, StrandedRefNamespace)
	}
	if strings.TrimSpace(expectSHA) == "" {
		return fmt.Errorf("refusing to delete %s: no expected commit", ref)
	}
	out, err := exec.Command("git", "-C", repoRoot, "update-ref", "-d", ref, expectSHA).CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete %s at %s: %v: %s", ref, expectSHA, err, strings.TrimSpace(string(out)))
	}
	return nil
}
