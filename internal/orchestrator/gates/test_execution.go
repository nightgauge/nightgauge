package gates

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/testexec"
)

// testExecutionVerdict is the feature-validate gate's evidence-of-execution
// check (#1261): of the test files this change adds, did the repo's own test
// command ever execute the ones it structurally excludes?
//
// Unlike markUnexercisedDeliverable — which downgrades a verdict and never
// blocks — this one blocks. The two are answering different questions and the
// difference decides the severity. A tier the stage chose not to run is a
// scoping decision an operator can review after the fact; a file the configured
// command *cannot* reach has never been executed by anything, and merging it
// puts code in the tree that no run has ever touched. A downstream Flutter app
// merged that state three times running, and it stayed undetected for five
// weeks precisely because nothing downstream was allowed to say no.
//
// Everything about it is quiet in the common case. A repo whose test command
// excludes nothing produces no evidence lines, no warnings, and the identical
// gate result it produced before this check existed.
type testExecutionVerdict struct {
	Blocked  bool
	Reason   string
	Evidence []string
	// Quiet is true when the check had nothing to say at all. The gate uses it
	// to decide whether to add a passing evidence line, so an ordinary repo
	// gains no new output.
	Quiet bool
}

// checkTestExecution runs the evidence-of-execution check for one issue.
//
// Every failure path returns a non-blocking, quiet verdict. An unresolvable
// diff, an unreadable config, a broken record file — none of them are evidence
// that a suite went unexecuted, and a gate that blocks on "I could not tell" is
// a gate that gets disabled by the second person who hits it.
func checkTestExecution(issueNumber int, workspace string) testExecutionVerdict {
	changed, resolved := ci.ChangedFilesAgainstDefaultBaseResolved(workspace)
	if !resolved || len(changed) == 0 {
		return testExecutionVerdict{Quiet: true}
	}

	var configured string
	if cfg, err := config.LoadMerged(workspace); err == nil && cfg != nil {
		configured = cfg.Pipeline.ResolveTestExecutionCommand()
	}

	res, err := testexec.Check(testexec.Options{
		Workspace:         workspace,
		IssueNumber:       issueNumber,
		ChangedFiles:      changed,
		ConfiguredCommand: configured,
	})
	if err != nil {
		return testExecutionVerdict{Quiet: true}
	}
	// Record what was found in the validate artifact either way — a satisfied
	// exclusion is evidence too, and a consumer reading the artifact later
	// cannot re-derive it without the diff.
	_ = testexec.ApplyToValidateContext(workspace, issueNumber, res)

	if res.Quiet() {
		return testExecutionVerdict{Quiet: true}
	}
	if !res.Blocked() {
		return testExecutionVerdict{
			Evidence: []string{fmt.Sprintf(
				"test-execution: %d excluded test file(s) each carry a passing execution record",
				len(res.Satisfied))},
		}
	}
	return testExecutionVerdict{
		Blocked:  true,
		Reason:   res.Reason(),
		Evidence: res.Evidence(),
	}
}
