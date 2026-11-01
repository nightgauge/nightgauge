package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// PreservedResume is the answer to "can this re-dispatch start at
// feature-validate?" (#2730). A killed run's work survives on its branch or
// under refs/nightgauge/wip/, and its dev-{N}.json survives in the shared state
// directory, so repeating planning and dev re-buys work that already exists.
type PreservedResume struct {
	Resume bool `json:"resume"`
	// Reason says why not, or what the resume rests on.
	Reason string `json:"reason"`
	// RestoredCommit is the preserved commit merged onto the branch, if one was.
	RestoredCommit string `json:"restored_commit,omitempty"`
}

// IssueBodyReader reads an issue's body. The fingerprint needs the live criteria.
type IssueBodyReader func(ctx context.Context, issueNumber int) (string, error)

// ResolvePreservedResume decides and, when it can, restores: it needs a
// dev-{N}.json, the worktree on the issue's own branch, an unchanged
// acceptance-criteria fingerprint (#2623), and commits beyond base, merging the
// newest preserved WIP ref in first when the branch has none. Every other case
// reports no resume and leaves the tree as found, so the run takes the normal
// route.
//
// dev-{N}.json is checked first and the forge last: a brand-new issue, the
// overwhelmingly common dispatch, must cost no API call (#1428).
func ResolvePreservedResume(ctx context.Context, workdir string, issueNumber int, readBody IssueBodyReader) PreservedResume {
	no := func(format string, args ...any) PreservedResume {
		return PreservedResume{Reason: fmt.Sprintf(format, args...)}
	}
	if _, err := os.Stat(pipelineContextPath(workdir, fmt.Sprintf("dev-%d.json", issueNumber))); err != nil {
		return no("no dev-%d.json", issueNumber)
	}
	branch, err := gitOutput(ctx, workdir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return no("HEAD is not on a branch")
	}
	if !regexp.MustCompile(fmt.Sprintf(`^[^/]+/%d-`, issueNumber)).MatchString(branch) {
		return no("branch %s is not this issue's", branch)
	}
	recorded := recordedACFingerprint(workdir, issueNumber)
	if recorded == "" {
		return no("no acceptance-criteria fingerprint recorded")
	}
	body, err := readBody(ctx, issueNumber)
	if err != nil {
		return no("cannot read the issue: %v", err)
	}
	if current := preflight.ACFingerprint(body); current != recorded {
		return no("acceptance criteria changed since dev ran")
	}

	base := "origin/" + recordedBaseBranch(workdir, issueNumber)
	ahead, err := commitsAhead(ctx, workdir, base)
	if err != nil {
		return no("cannot compare with %s: %v", base, err)
	}
	if ahead > 0 {
		return PreservedResume{Resume: true, Reason: fmt.Sprintf("%d commit(s) beyond %s", ahead, base)}
	}

	commit := newestWipCommit(workdir, issueNumber)
	if commit == "" {
		return no("no commits beyond %s and nothing preserved", base)
	}
	if status, err := gitOutput(ctx, workdir, "status", "--porcelain"); err != nil || status != "" {
		return no("worktree is not clean, so preserved work was not restored")
	}
	if _, err := gitOutput(ctx, workdir, "merge", "--no-edit", commit); err != nil {
		if _, abortErr := gitOutput(ctx, workdir, "merge", "--abort"); abortErr != nil {
			log.Printf("resume: could not abort the merge of %s in %s: %v", commit, workdir, abortErr)
		}
		return no("preserved commit %s does not merge cleanly", commit)
	}
	return PreservedResume{Resume: true, Reason: "restored preserved work", RestoredCommit: commit}
}

func gitOutput(ctx context.Context, workdir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func commitsAhead(ctx context.Context, workdir, base string) (int, error) {
	out, err := gitOutput(ctx, workdir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// newestWipCommit is the newest preserved commit for the issue, or "".
// ListWipRefs sorts newest first.
func newestWipCommit(workdir string, issueNumber int) string {
	refs, err := reclaim.ListWipRefs(workdir)
	if err != nil {
		log.Printf("resume: could not list preserved work for #%d: %v", issueNumber, err)
		return ""
	}
	for _, ref := range refs {
		if ref.Issue == issueNumber {
			return ref.Commit
		}
	}
	return ""
}

// recordedACFingerprint is the fingerprint ac-reconcile recorded for this run.
func recordedACFingerprint(workdir string, issueNumber int) string {
	data, err := readFile(pipelineContextPath(workdir, fmt.Sprintf("ac-reconcile-%d.json", issueNumber)))
	if err != nil {
		return ""
	}
	var rec preflight.ACReconcileResult
	if json.Unmarshal(data, &rec) != nil {
		return ""
	}
	return rec.ACFingerprint
}

// recordedBaseBranch is issue-{N}.json's base_branch, or main.
func recordedBaseBranch(workdir string, issueNumber int) string {
	if data, err := readFile(pipelineContextPath(workdir, fmt.Sprintf("issue-%d.json", issueNumber))); err == nil {
		var ctx struct {
			BaseBranch string `json:"base_branch"`
		}
		if json.Unmarshal(data, &ctx) == nil && ctx.BaseBranch != "" {
			return ctx.BaseBranch
		}
	}
	return "main"
}

// ExecIssueBodyReader reads the issue with `gh` from workdir.
func ExecIssueBodyReader(workdir string) IssueBodyReader {
	c := &execGhClient{workdir: workdir}
	return c.IssueBody
}
