package doctor

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// minMergedRunsForCoverageFinding is how many recorded runs must exist before
// an empty survival store is worth reporting.
//
// Below this, "zero survival records" is indistinguishable from a workspace
// that has not merged anything yet. At or above it, the pipeline has merged
// enough times that a total absence of capture is a defect rather than a
// start-up condition.
const minMergedRunsForCoverageFinding = 10

// checkSurvivalCoverage reports a workspace whose pipeline has merged work but
// whose survival journal has never been written to (#1019).
//
// This is the INVERSE of checkSurvivalBacklog, and the distinction is the whole
// point. The backlog arm reads pending records and reports the ones whose
// window has lapsed — so it answers "are verdicts going stale?" and returns a
// clean bill for a store with nothing in it. An empty store is exactly what a
// dead writer produces, and it is also exactly what a healthy young workspace
// produces. The two render identically to every consumer.
//
// That ambiguity is not hypothetical: the extension merge path — the one the
// dogfood runbook mandates — invoked the post-merge hook without --pr, so
// SurvivalEligible was never true and the seeding block never ran. Across a
// 113-run history the store stayed empty, the backlog arm stayed green, and
// "we have no evidence the pipeline's work survives" was indistinguishable
// from "nothing has been merged yet".
//
// The discriminator is the outcome corpus: one row per completed run, written
// by a different mechanism on the same merges. Rows without records means the
// capture is broken, not that the workspace is new.
func checkSurvivalCoverage(workspaceRoot string) (CheckItem, string) {
	if workspaceRoot == "" {
		return CheckItem{OK: true, Detail: "survival coverage not checked (no workspace root)"}, ""
	}

	outcomes, err := learning.NewRecorder(workspaceRoot).LoadAll()
	if err != nil {
		// Unreadable is not empty — the same rule the backlog arm follows.
		msg := fmt.Sprintf("survival coverage unverifiable: %v", err)
		return CheckItem{OK: false, Detail: "could not read the outcome corpus", Error: msg}, msg
	}
	if len(outcomes) < minMergedRunsForCoverageFinding {
		return CheckItem{
			OK: true,
			Detail: fmt.Sprintf("%d recorded run(s); below the %d-run floor for a coverage finding",
				len(outcomes), minMergedRunsForCoverageFinding),
		}, ""
	}

	records, err := survival.NewStore(workspaceRoot).Load()
	if err != nil {
		msg := fmt.Sprintf("survival coverage unverifiable: %v", err)
		return CheckItem{OK: false, Detail: "could not read the survival store", Error: msg}, msg
	}
	if len(records) > 0 {
		return CheckItem{
			OK: true,
			Detail: fmt.Sprintf("survival capture is live: %d record(s) against %d recorded run(s)",
				len(records), len(outcomes)),
		}, ""
	}

	msg := fmt.Sprintf("survival-capture-never-fired: %d recorded run(s) and ZERO survival records — "+
		"post-merge capture has never written to this journal, so there is no ground truth about "+
		"whether merged work survives. Check that the post-merge hook is invoked with --pr (it needs "+
		"the merge SHA) and with --workdir set to the launch root when the run executes in a worktree",
		len(outcomes))
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d recorded run(s), 0 survival records", len(outcomes)),
		Error:  msg,
	}, msg
}
