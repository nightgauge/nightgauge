package ci

import (
	"fmt"
	"os/exec"
	"strings"

	changeClassifier "github.com/nightgauge/nightgauge/internal/intelligence/changeClassifier"
)

// ClassifyResult is the deterministic CI fast-track decision for a diff (#4127).
// It is consumed by the always-running `changes` gate job in CI, which exposes
// the Jobs map as workflow-job outputs. Those outputs gate the EXPENSIVE STEPS
// of an always-running required job — never the job itself, because a skipped
// required status check deadlocks branch protection.
type ClassifyResult struct {
	// ChangeClass is the coarse classification: empty|docs_only|config_only|source|mixed.
	ChangeClass string `json:"change_class"`
	// RunHeavy is true when the heavy CI work (npm build/test, go test, e2e,
	// vsix audit) should run. It is the OR of the per-job flags.
	RunHeavy bool `json:"run_heavy"`
	// Jobs holds the per-job run flags (run_build, run_go_tests, run_e2e,
	// run_vsix_audit) the workflow reads as `needs.changes.outputs.<key>`.
	Jobs map[string]bool `json:"jobs"`
	// Reason is a human-readable explanation for logs.
	Reason string `json:"reason"`
}

// HeavyJobKeys are the per-job output flags the CI `changes` gate exposes.
var HeavyJobKeys = []string{"run_build", "run_go_tests", "run_e2e", "run_vsix_audit"}

// ClassifyForCI decides whether a diff (changedFiles, typically `git diff
// --name-only base...head`) needs the heavy CI jobs. It is conservative and
// fail-safe: the heavy jobs are skipped ONLY when the change is unambiguously
// safe to fast-track — documentation-only, or no files at all.
//
// Source, Mixed, AND Config changes always run the full suite. Config is
// deliberately NOT fast-tracked for CI even though the pipeline skips
// feature-validate for it (#4125/#4126): a config change can be a package.json /
// tsconfig / CI-workflow edit that genuinely needs build+test, and the CI
// workflow files themselves classify as config — fast-tracking config would let
// an untested CI change merge. The classifier shares change_rules'
// DefaultClassPatterns (#4124/#4125), so this decision flows from the same
// single source of truth as the pipeline routing.
func ClassifyForCI(changedFiles []string) ClassifyResult {
	class := changeClassifier.ClassifyDefault(changedFiles)
	safe := class == changeClassifier.DocsOnly || class == changeClassifier.Empty
	runHeavy := !safe

	res := ClassifyResult{
		ChangeClass: string(class),
		RunHeavy:    runHeavy,
		Jobs:        make(map[string]bool, len(HeavyJobKeys)),
		Reason:      classReason(class, runHeavy),
	}
	for _, k := range HeavyJobKeys {
		res.Jobs[k] = runHeavy
	}
	return res
}

func classReason(class changeClassifier.Classification, runHeavy bool) string {
	if !runHeavy {
		return fmt.Sprintf("change_class=%s is fast-trackable — heavy CI jobs skipped", class)
	}
	return fmt.Sprintf("change_class=%s requires the full CI suite", class)
}

// FailOpenResult returns a decision that runs the FULL CI suite. The CI gate
// uses it whenever the diff cannot be computed/classified, so an
// unclassifiable change is never under-tested (fail-open, not fail-closed).
func FailOpenResult(reason string) ClassifyResult {
	res := ClassifyResult{
		ChangeClass: "unknown",
		RunHeavy:    true,
		Jobs:        make(map[string]bool, len(HeavyJobKeys)),
		Reason:      reason,
	}
	for _, k := range HeavyJobKeys {
		res.Jobs[k] = true
	}
	return res
}

// DefaultDiffBases are the refs tried, in order, when a caller has no explicit
// base: the remote tip first, then the local branch for a repo without a
// fetched remote.
var DefaultDiffBases = []string{"origin/main", "main"}

// BookkeepingDirs are the agent/pipeline state directories that a run always
// writes and that are never part of the deliverable: stage context files land
// in `.nightgauge/pipeline/`, attention cards in `.nightgauge/attention/`, and
// adapters scribble in `.claude/`.
//
// Whether these show up in `git status` is a per-repo accident — this repo
// ignores `.nightgauge/pipeline` (nested `.nightgauge/.gitignore`) but NOT
// `.nightgauge/attention`, and a consumer repo may ignore neither. Any check
// that asks "did this run produce work?" must therefore exclude them
// explicitly rather than rely on the repo's ignore rules, or the pipeline's own
// exhaust answers the question for it. Issue #202.
var BookkeepingDirs = []string{".nightgauge", ".claude"}

// DeliverablePathspec is the `git` pathspec limiting an operation to the
// deliverable: everything except BookkeepingDirs. The leading "." is required —
// a pathspec list containing only exclusions matches nothing, which would
// silently invert every caller.
func DeliverablePathspec() []string {
	spec := make([]string, 0, len(BookkeepingDirs)+1)
	spec = append(spec, ".")
	for _, dir := range BookkeepingDirs {
		spec = append(spec, ":(exclude)"+dir)
	}
	return spec
}

// IsBookkeepingPath reports whether a path is agent/pipeline state rather than
// deliverable content, for callers holding a file list instead of running git.
func IsBookkeepingPath(path string) bool {
	for _, dir := range BookkeepingDirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// ChangedFilesAgainstDefaultBase lists the files changed on the checked-out
// branch relative to the first of DefaultDiffBases that resolves.
//
// Fail-safe: every base failing returns nil rather than an error, because both
// callers treat "no diff" as the conservative answer — the CI relaxation gate
// declines to relax, and the unexercised-deliverable check declines to
// accuse. Neither should ever act on a diff it could not compute.
//
// Shared by the relaxation gate and the deliverable check on purpose. Two
// copies of "which ref is the base" is precisely how one caller ends up
// comparing against something the other does not.
func ChangedFilesAgainstDefaultBase(workdir string) []string {
	files, _ := ChangedFilesAgainstDefaultBaseResolved(workdir)
	return files
}

// ChangedFilesAgainstDefaultBaseResolved is ChangedFilesAgainstDefaultBase with
// the collapsed distinction restored: resolved reports whether a base ref was
// found at all, so "the diff is empty" can be told apart from "the diff could
// not be computed". Both return (nil, ...) — only the flag separates them.
//
// The plain variant is right for callers whose conservative answer is "no
// diff". It is exactly wrong for a caller that ACCUSES on an empty diff: the
// feature-dev ground-truth gate (#202) fails a stage that produced nothing, so
// for it an unresolvable base must mean "cannot verify, pass" rather than
// "verified empty, fail". Collapsing the two there would fail every dev stage
// in any repo whose default branch is not main.
func ChangedFilesAgainstDefaultBaseResolved(workdir string) (files []string, resolved bool) {
	for _, base := range DefaultDiffBases {
		files, err := ChangedFilesFromGit(workdir, base, "HEAD")
		if err != nil {
			continue
		}
		return files, true
	}
	return nil, false
}

// ChangedFilesFromGit returns the files changed between base and head via
// `git diff --name-only base...head`. The three-dot form lists changes on head
// since its merge-base with base — matching how the CI gate compares a PR head
// against its target branch. workdir is the repo root ("" = current dir).
func ChangedFilesFromGit(workdir, base, head string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", base+"..."+head)
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s...%s: %w", base, head, err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			files = append(files, s)
		}
	}
	return files, nil
}
