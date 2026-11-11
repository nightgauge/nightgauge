package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/execution"
	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

// reviewCapGitTimeout bounds each git call the review-cap probe makes, so a
// slow checkout costs the card at most a few seconds and never a dispatch.
const reviewCapGitTimeout = 10 * time.Second

// reviewCapLocation is where a capped run's work is when its card is raised
// (#3059). The review records the worktree; cleanup can remove it and the run
// can have committed to the issue branch, so the card reads both at card time.
type reviewCapLocation struct {
	Worktree       string
	WorktreeExists bool
	Uncommitted    int
	Branches       []branchAhead
	Base           string
	Err            error // first git failure; the card then claims no contents
}

type branchAhead struct {
	Name  string
	Ahead int
}

func reviewCapGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reviewCapGitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// probeReviewCapLocation reads the worktree, issue branch(es) and their
// commits beyond origin/<default> in checkout. It is best-effort: any git
// failure is recorded in Err and the rest is left unclaimed.
func probeReviewCapLocation(checkout, worktree string, issue int) reviewCapLocation {
	loc := reviewCapLocation{Worktree: worktree}
	if worktree != "" {
		if st, err := os.Stat(worktree); err == nil && st.IsDir() {
			if _, err := reviewCapGit(worktree, "rev-parse", "--show-toplevel"); err == nil {
				loc.WorktreeExists = true
			}
		}
	}
	var names []string
	if loc.WorktreeExists {
		status, err := reviewCapGit(worktree, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			loc.Err = err
			return loc
		}
		if status != "" {
			loc.Uncommitted = len(strings.Split(status, "\n"))
		}
		branch, err := reviewCapGit(worktree, "branch", "--show-current")
		if err != nil {
			loc.Err = err
			return loc
		}
		if branch != "" {
			names = []string{branch}
		}
	}
	if len(names) == 0 {
		// No worktree, or one on a detached HEAD: find the issue branch(es).
		found, err := issueBranches(checkout, issue)
		if err != nil {
			loc.Err = err
			return loc
		}
		names = found
	}
	loc.Base = "origin/" + execution.DetectDefaultBranch(checkout)
	for _, name := range names {
		count, err := reviewCapGit(checkout, "rev-list", "--count", loc.Base+".."+name)
		if err != nil {
			loc.Err = err
			return loc
		}
		ahead, err := strconv.Atoi(strings.TrimSpace(count))
		if err != nil {
			loc.Err = fmt.Errorf("git rev-list --count %s..%s: %w", loc.Base, name, err)
			return loc
		}
		loc.Branches = append(loc.Branches, branchAhead{Name: name, Ahead: ahead})
	}
	return loc
}

// issueBranches lists the local branches in checkout named for issue.
func issueBranches(checkout string, issue int) ([]string, error) {
	refs, err := reviewCapGit(checkout, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(refs, "\n") {
		if name == "" || strings.HasPrefix(name, "epic/") || !gitpkg.IsCleanupCandidate(name) {
			continue
		}
		if n, ok := gitpkg.ParseIssueNumberFromBranch(name); ok && n == issue {
			names = append(names, name)
		}
	}
	return names, nil
}

// writeReviewCapLocation states where the work is, as probed at card time.
func writeReviewCapLocation(b *strings.Builder, loc reviewCapLocation) {
	wt := attention.CodeSpan(loc.Worktree)
	if loc.Err != nil {
		if loc.Worktree != "" {
			fmt.Fprintf(b, "The run's worktree was %s. ", wt)
		}
		fmt.Fprintf(b, "Where the work is could not be read: %s.\n\n", attention.EscapeMarkdown(loc.Err.Error()))
		return
	}
	base := attention.CodeSpan(loc.Base)
	switch {
	case loc.WorktreeExists:
		fmt.Fprintf(b, "The run's worktree %s still exists and holds %d uncommitted path(s).", wt, loc.Uncommitted)
	case loc.Worktree != "":
		fmt.Fprintf(b, "The run's worktree %s no longer exists, so the work is not there.", wt)
	}
	total := 0
	for _, br := range loc.Branches {
		total += br.Ahead
	}
	switch {
	case len(loc.Branches) == 0:
		b.WriteString(" No local branch for this issue was found")
		if !loc.WorktreeExists {
			b.WriteString(", so no work was found")
		}
		b.WriteString(".")
	case total == 0 && !loc.WorktreeExists:
		b.WriteString(" No work was found:")
		for i, br := range loc.Branches {
			sep := ","
			if i == len(loc.Branches)-1 {
				sep = ""
			}
			fmt.Fprintf(b, " %s holds no commits beyond %s%s", attention.CodeSpan(br.Name), base, sep)
		}
		b.WriteString(".")
	default:
		for _, br := range loc.Branches {
			fmt.Fprintf(b, " Branch %s holds %d commit(s) beyond %s.", attention.CodeSpan(br.Name), br.Ahead, base)
		}
	}
	b.WriteString("\n\n")
}
