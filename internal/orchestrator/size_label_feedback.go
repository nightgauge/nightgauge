package orchestrator

import (
	"context"
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// SizeLabeler is the one forge write this feedback path performs. An interface
// rather than *github.IssueService so the behaviour is drivable in a test
// without a live client — the label write is the half of #1515 that touches the
// outside world, and an untestable forge write is an untested one.
type SizeLabeler interface {
	ApplySizeLabel(ctx context.Context, owner, repo string, number int, size string) error
}

// PlannerSizeLabelResult reports what the backfill did, for the caller's log
// and for a test to assert on. Verdict is one of the constants below.
type PlannerSizeLabelResult struct {
	Verdict string
	// Size is the bucket that was applied, or that the planner assessed when
	// nothing was applied.
	Size string
	// LabelSize is the size the issue already carried, when it carried one.
	LabelSize string
	Err       error
}

const (
	// SizeLabelApplied: the issue had no size:* label, the planner assessed
	// one, and it is now on the issue.
	SizeLabelApplied = "applied"
	// SizeLabelAlreadyPresent: the issue already carries a size:* label. It is
	// never overwritten, whether or not it agrees with the planner.
	SizeLabelAlreadyPresent = "already-present"
	// SizeLabelNoAssessment: the plan named no size this can act on.
	SizeLabelNoAssessment = "no-assessment"
	// SizeLabelFailed: the write was attempted and the forge refused it.
	SizeLabelFailed = "failed"
)

// BackfillPlannerSizeLabel applies the size the run's feature-planning stage
// assessed to an issue that carries no `size:*` label (#1515).
//
// This is the cheap half of calibration. Every run that plans assesses a size;
// until now that assessment was read once at record time and never reached the
// issue, so the NEXT run of the same backlog was routed from the same missing
// input, and `nightgauge`'s pre-flight cost estimate had no size to join run
// history on (#112). One additive REST call per planned run — usually a no-op
// once a repo's backlog has been through the pipeline — closes that loop with
// no model spend at all.
//
// IT NEVER OVERWRITES AN EXISTING LABEL, including one that disagrees with the
// planner. The label is a human's input to the router; an agent quietly
// replacing it is a worse failure than the disagreement, and the disagreement
// is not lost — the run record carries both halves (`size`/`size_source` and
// `planner_size`) precisely so it can be measured.
//
// Best-effort throughout: every arm returns a result and logs, and none of them
// fails the stage. A run whose size label could not be written is exactly as
// well off as every run before this existed.
func BackfillPlannerSizeLabel(
	ctx context.Context,
	labeler SizeLabeler,
	repo string,
	issueNumber int,
	labels []string,
	runRoot, worktreeDir string,
) PlannerSizeLabelResult {
	if labeler == nil || issueNumber <= 0 {
		return PlannerSizeLabelResult{Verdict: SizeLabelNoAssessment}
	}
	assessment := execution.LoadPlannerAssessment(runRoot, worktreeDir, repo, issueNumber)
	size := PlannerSizeFromAssessment(assessment.SizeLabel, assessment.Score)
	if size == "" {
		log.Printf("#%d: feature-planning wrote no usable complexity_assessment size — nothing to backfill (#1515)",
			issueNumber)
		return PlannerSizeLabelResult{Verdict: SizeLabelNoAssessment}
	}
	if existing := recognizedSize(OutcomeSizeInput("", labels)); existing != "" {
		if existing != size {
			log.Printf("#%d: keeping the issue's own size:%s — the planner assessed %s, and a disagreement is "+
				"recorded on the run record (planner_size), never resolved by relabelling (#1515)",
				issueNumber, existing, size)
		}
		return PlannerSizeLabelResult{Verdict: SizeLabelAlreadyPresent, Size: size, LabelSize: existing}
	}
	if gh.HasSizeLabel(labels) {
		// A `size:*` label the router does not recognize. Adding a second one
		// would leave the issue carrying two, which is worse than carrying an
		// odd one — the router reads the first it recognizes and a human has to
		// untangle the rest.
		log.Printf("#%d: issue carries an unrecognized size:* label — not adding a second one (#1515)", issueNumber)
		return PlannerSizeLabelResult{Verdict: SizeLabelAlreadyPresent, Size: size}
	}

	owner, name, ok := splitRepoSlug(strings.TrimSpace(repo))
	if !ok {
		log.Printf("#%d: cannot backfill size:%s — repo %q is not owner/name (#1515)", issueNumber, size, repo)
		return PlannerSizeLabelResult{Verdict: SizeLabelNoAssessment, Size: size}
	}
	if err := labeler.ApplySizeLabel(ctx, owner, name, issueNumber, size); err != nil {
		log.Printf("#%d: failed to apply size:%s from the planner's assessment (non-fatal): %v (#1515)",
			issueNumber, size, err)
		return PlannerSizeLabelResult{Verdict: SizeLabelFailed, Size: size, Err: err}
	}
	log.Printf("#%d: applied size:%s from the planner's own assessment — the issue had none, so neither the router "+
		"nor the cost estimate had a size to work from (#112, #1515)", issueNumber, size)
	return PlannerSizeLabelResult{Verdict: SizeLabelApplied, Size: size}
}
