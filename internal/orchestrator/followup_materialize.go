package orchestrator

import (
	"errors"
	"log"
	"os"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// appendPostMergeStages appends the post-merge filers. Spike issues get
// spike-materialize, which creates follow-ups from the artifact's YAML
// recommendations block (docs/SPIKE_CONTRACT.md). Every issue then
// gets followup-materialize (#480): it is unconditional because every issue
// type may record discovered work, and the zero-record case is skipped before
// dispatch (followupCaptureHasRecords).
func appendPostMergeStages(item types.BoardItem, stages []state.PipelineStage) []state.PipelineStage {
	if gh.IsSpikeIssue(item.Labels) {
		log.Printf("#%d: spike issue detected — appending spike-materialize stage after pr-merge",
			item.Number)
		stages = append(stages, state.StageSpikeMaterialize)
	}
	return append(stages, state.StageFollowupMaterialize)
}

// followupCaptureHasRecords reports whether the run's discovered-work capture
// holds at least one record. An absent file is an empty capture. A capture
// that cannot be read or validated counts as having records, so the stage
// dispatches and the materializer reports the fault instead of the scheduler
// silently skipping work it could not see.
func followupCaptureHasRecords(workspace string, issue int) bool {
	dir, err := layout.PipelineStateDir(workspace)
	if err != nil {
		log.Printf("#%d: followup-materialize: pipeline state dir: %v — dispatching", issue, err)
		return true
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return false // no pipeline state dir: nothing was recorded
	}
	c, err := discoveredwork.Load(dir, issue)
	if err != nil {
		log.Printf("#%d: followup-materialize: capture unreadable: %v — dispatching", issue, err)
		return true
	}
	return c != nil && len(c.Records) > 0
}
