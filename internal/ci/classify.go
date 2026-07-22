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
