package reclaim

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Preserved WIP refs — the third leak in the same family as worktrees (#332)
// and stashes (#330), and the only one that had a writer and no reader at all.
//
// When a guard kill lands on a dirty worktree, `preserveWorkInProgress`
// commits the tree to the stage branch and anchors that commit under
// `refs/nightgauge/wip/` so the re-dispatch teardown (`worktree remove
// --force` + `branch -D` + fresh `worktree add`) cannot orphan it. That part
// works. What did not exist was anything on the other side of it: no verb
// listed the namespace, no `doctor` arm reported it, nothing ever removed a
// ref once its work had landed (#1105).
//
// The consequence is worse than untidy. On 2026-08-28 a killed run preserved
// 13 paths as `641b9c8f`; the branch and worktree were cleaned up, leaving the
// commit reachable ONLY through the WIP ref; the next day the same issue was
// re-run and started from scratch without mentioning it. The feature's whole
// promise — "the work is committed, not discarded" — reduced to "recoverable
// by someone who already knows the namespace exists". Meanwhile every ref
// pins its object graph against `git gc` forever.
//
// This file is the reader. Listing is unconditional and cheap; pruning is
// deliberately narrow — see PruneWipRefs for why "landed" is decided by
// CONTENT and why nothing else is ever removed without an explicit ask.

// WipRefNamespace is the ref namespace `preserveWorkInProgress` writes to.
// It must stay in lockstep with WIP_REF_NAMESPACE in
// packages/nightgauge-vscode/src/utils/preserveWorkInProgress.ts.
const WipRefNamespace = "refs/nightgauge/wip"

// wipCommitTrailer marks a commit as machine-authored WIP preservation. Same
// contract: it mirrors WIP_COMMIT_TRAILER on the writer side, and is the only
// place the stage name survives — the ref name does not carry it.
const wipCommitTrailer = "Nightgauge-WIP"

// wipRefIssuePattern extracts the issue from the commit body's `Refs: #N`
// line. Deliberately read from the COMMIT and not from the ref name: the ref
// component is a sanitized BRANCH name, and a branch is not required to carry
// its issue number (`hotfix-flaky-test` carries none, `feat/12-fix-338` two).
var wipRefIssuePattern = regexp.MustCompile(`(?m)^Refs:\s*#(\d+)\s*$`)

// wipRefTimestampSuffix is the `-<unix seconds>` the writer appends to make
// each ref unique. Stripped to recover the sanitized branch component.
var wipRefTimestampSuffix = regexp.MustCompile(`-\d{9,}$`)

// wipRefUnsafe matches everything the writer collapses when sanitizing a
// branch into a single ref path component.
var wipRefUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// WipRef is one preserved-work anchor: a ref under WipRefNamespace and
// everything about the commit it points at that an operator needs to decide
// whether to salvage it.
type WipRef struct {
	// Ref is the full ref name, e.g.
	// refs/nightgauge/wip/feat-338-guest-auth-1787939337.
	Ref string `json:"ref"`
	// Commit is the preserved commit's SHA — the thing to `git show`. Carried
	// explicitly so a report stays actionable after the ref is pruned.
	Commit string `json:"commit"`
	// Branch is the stage branch the work was committed to. Resolved to the
	// real local branch when one still sanitizes to the ref's component;
	// otherwise the sanitized component itself, which is what a deleted
	// branch leaves behind.
	Branch string `json:"branch"`
	// BranchExists distinguishes "the branch is still here, look there" from
	// "this ref is the only way back to the work".
	BranchExists bool `json:"branchExists"`
	// Issue is the issue the run belonged to, 0 when the commit body carried
	// no `Refs:` line.
	Issue int `json:"issue,omitempty"`
	// Stage is the terminated stage, e.g. feature-validate. Empty when the
	// trailer is missing.
	Stage string `json:"stage,omitempty"`
	// FilesChanged is how many paths the preserved commit touches — the
	// magnitude of what is at stake, and the number the kill-time log line
	// quoted.
	FilesChanged int `json:"filesChanged"`
	// CommittedAt is the preserved commit's committer date.
	CommittedAt time.Time `json:"committedAt"`
}

// Age is how long the preserved work has been sitting unclaimed.
func (w WipRef) Age(now time.Time) time.Duration {
	if w.CommittedAt.IsZero() {
		return 0
	}
	if d := now.Sub(w.CommittedAt); d > 0 {
		return d
	}
	return 0
}

// AgeDays is Age in whole days, the unit every other leak report uses.
func (w WipRef) AgeDays(now time.Time) int {
	return int(w.Age(now).Hours() / 24)
}

// ListWipRefs enumerates every preserved-WIP anchor in a repository, newest
// first.
//
// A repo with no preserved work returns an empty slice and no error — there is
// nothing to be wrong about. A failing git returns an error, and callers must
// read that as "I could not find out", never as "there are none" (#323).
//
// Per-ref detail (issue, stage, path count) is best-effort: a ref whose commit
// object cannot be read still appears, with the fields it could not fill left
// zero. Dropping it would hide the one case where salvage matters most.
func ListWipRefs(repoRoot string) ([]WipRef, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("list wip refs: repo root is required")
	}
	// Space-separated, not %x1f: `for-each-ref` does not expand the hex
	// escapes `git log --pretty` does, and would emit the literal "%x1f" —
	// which parses as one field and reports an empty namespace on a repo full
	// of preserved work. A space is unambiguous here because no ref name and
	// no object name may contain one.
	out, err := gitWipOutput(repoRoot, "for-each-ref",
		"--format=%(refname) %(objectname) %(committerdate:unix)", WipRefNamespace)
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref %s in %s: %w", WipRefNamespace, repoRoot, err)
	}

	branches := localBranchSet(repoRoot)

	var refs []WipRef
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		ref := WipRef{Ref: parts[0], Commit: parts[1]}
		if secs, convErr := strconv.ParseInt(parts[2], 10, 64); convErr == nil {
			ref.CommittedAt = time.Unix(secs, 0)
		}
		ref.Branch, ref.BranchExists = resolveWipBranch(ref.Ref, branches)
		if body, bodyErr := gitWipOutput(repoRoot, "show", "-s", "--format=%B", ref.Commit); bodyErr == nil {
			ref.Issue, ref.Stage = parseWipCommitBody(body)
		}
		ref.FilesChanged = wipCommitFileCount(repoRoot, ref.Commit)
		refs = append(refs, ref)
	}

	sort.Slice(refs, func(i, j int) bool {
		if !refs[i].CommittedAt.Equal(refs[j].CommittedAt) {
			return refs[i].CommittedAt.After(refs[j].CommittedAt)
		}
		return refs[i].Ref < refs[j].Ref
	})
	return refs, nil
}

// WipRefsForIssue narrows a listing to one issue. issue <= 0 means every
// issue, matching PipelineStashes.
func WipRefsForIssue(refs []WipRef, issue int) []WipRef {
	if issue <= 0 {
		return refs
	}
	var out []WipRef
	for _, r := range refs {
		if r.Issue == issue {
			out = append(out, r)
		}
	}
	return out
}

// parseWipCommitBody pulls the issue and the stage out of a preservation
// commit message. Both are optional; a commit written before either existed,
// or by a future writer that changes the shape, degrades to zero rather than
// making the ref invisible.
func parseWipCommitBody(body string) (issue int, stage string) {
	if m := wipRefIssuePattern.FindStringSubmatch(body); len(m) == 2 {
		issue, _ = strconv.Atoi(m[1])
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, wipCommitTrailer+":"); ok {
			stage = strings.TrimSpace(rest)
		}
	}
	return issue, stage
}

// wipCommitFileCount counts the paths a preserved commit touches. Zero on any
// failure — the count is evidence of magnitude, never part of a decision, so a
// missing one must not stop the ref from being reported.
func wipCommitFileCount(repoRoot, commit string) int {
	out, err := gitWipOutput(repoRoot, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	if err != nil {
		return 0
	}
	return len(nonEmptyLines(out))
}

// resolveWipBranch recovers the stage branch from a ref name.
//
// The writer sanitizes the branch into one path component and appends a
// timestamp, so `feat/338-guest-auth` becomes `feat-338-guest-auth-17879...`.
// That transformation is lossy and not invertible: `feat/338-x` and
// `feat-338-x` collapse to the same component. So rather than guessing, the
// component is compared against the sanitization of every live local branch —
// an exact round-trip match names the real branch and proves it still exists.
// No match returns the component itself, which is the honest rendering of a
// branch that has since been deleted (and the case where the ref is the only
// path back to the work).
func resolveWipBranch(ref string, branches []string) (string, bool) {
	component := ref
	if idx := strings.LastIndex(component, "/"); idx >= 0 {
		component = component[idx+1:]
	}
	component = wipRefTimestampSuffix.ReplaceAllString(component, "")
	for _, b := range branches {
		if sanitizeWipRefComponent(b) == component {
			return b, true
		}
	}
	return component, false
}

// sanitizeWipRefComponent mirrors sanitizeRefComponent on the writer side.
func sanitizeWipRefComponent(branch string) string {
	cleaned := wipRefUnsafe.ReplaceAllString(branch, "-")
	cleaned = strings.Trim(cleaned, "-.")
	if cleaned == "" {
		return "stage"
	}
	return cleaned
}

func localBranchSet(repoRoot string) []string {
	out, err := gitWipOutput(repoRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil
	}
	return nonEmptyLines(out)
}

func nonEmptyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}

func gitWipOutput(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	// Never let a credential/pinentry prompt block a reclamation scan.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
