package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// MergeQueueCodeFailure sends a merge-queue removal caused by the PR's own
// check back to feature-dev, once per check (#3198).
//
// pr-merge refuses such a removal with pmstages.MergeQueueCodeFailureMarker
// in its merge-queue-failed reason and writes the failing job, step and log
// excerpt to merge-queue-failure-{N}.json. This action appends a
// feature-dev feedback signal carrying that evidence, the same intake a
// feature-validate revision uses, and rewinds the run to feature-dev through
// BacktrackTargetStage. It does no LLM work.
//
// The bound lives in its own ledger, merge-queue-code-rewinds-{N}.json, not
// in the feedback file, which other writers may replace. It is keyed by the
// pipeline run and by the failed check (job and failed step): a second
// removal for a check this run already sent back fails the run, while a
// different failing step of the same job, or a later run of the issue, gets
// its own rewind. The scheduler deletes the ledger when the run ends
// (ClearMergeQueueFailureState).
type MergeQueueCodeFailure struct {
	now func() time.Time
}

// NewMergeQueueCodeFailure builds the action.
func NewMergeQueueCodeFailure() *MergeQueueCodeFailure {
	return &MergeQueueCodeFailure{now: time.Now}
}

// mergeQueueCodeFailureSignal is the feedback signal type this action writes.
// It is a warning, not blocking: the rewind is this action's own, so the
// generic post-stage backtrack evaluator must not act on it again.
const mergeQueueCodeFailureSignal = "MERGE_QUEUE_CODE_FAILURE"

// Name implements RecoveryAction.
func (a *MergeQueueCodeFailure) Name() string { return "merge-queue-code-failure" }

// CapExempt implements recovery.CapExempt: the action keeps its own bound,
// one rewind per check, so the global per-run pool must not pre-empt it.
func (a *MergeQueueCodeFailure) CapExempt() bool { return true }

// Description implements RecoveryAction.
func (a *MergeQueueCodeFailure) Description() string {
	return "the merge queue removed the PR because a merge-group job failed in the repository's own check — rewind to feature-dev once with the job's log excerpt; a second removal for the same check fails the run."
}

// Matches implements RecoveryAction. Pure: a pr-merge KindNoOp failure whose
// text carries the merge-queue-failed reason and the code-failure marker.
func (a *MergeQueueCodeFailure) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge || failure.GateKind != gates.KindNoOp {
		return false
	}
	if failure.Workspace == "" || failure.IssueNumber == 0 {
		return false
	}
	return isQueueCodeFailure(failure)
}

func isQueueCodeFailure(failure StageFailure) bool {
	combined := strings.ToLower(failure.Reason + " " + failure.StageError + " " + strings.Join(failure.Evidence, " "))
	return strings.Contains(combined, pmstages.ReasonMergeQueueFailed) &&
		strings.Contains(combined, strings.ToLower(pmstages.MergeQueueCodeFailureMarker))
}

// mergeQueueCodeRewinds is the on-disk bound: rewinds spent per failed
// check in one run. A ledger from another run, or with no run, counts as
// empty.
type mergeQueueCodeRewinds struct {
	IssueNumber int            `json:"issue_number"`
	RunID       string         `json:"run_id"`
	Checks      map[string]int `json:"checks"`
}

func mergeQueueCodeRewindsFile(issue int) string {
	return fmt.Sprintf("merge-queue-code-rewinds-%d.json", issue)
}

// mergeQueueCheckKey names one failed check: its job and its failed step.
func mergeQueueCheckKey(rec pmstages.MergeQueueFailureRecord) string {
	return rec.Check + "\x00" + rec.Step
}

// ClearMergeQueueFailureState removes the merge-queue code-failure record and
// rewind ledger of issue from workspace's pipeline state. The scheduler calls
// it when a run ends, so neither outlives the run that wrote it (#3198).
// Absent files are not an error.
func ClearMergeQueueFailureState(workspace string, issue int) error {
	if workspace == "" || issue <= 0 {
		return nil
	}
	dir, err := layout.PipelineStateDir(workspace)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range []string{mergeQueueCodeRewindsFile(issue), pmstages.MergeQueueFailureFile(issue)} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Execute implements RecoveryAction.
func (a *MergeQueueCodeFailure) Execute(_ context.Context, failure StageFailure) RecoveryResult {
	fail := func(reason string, evidence ...string) RecoveryResult {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   reason,
			Evidence: append([]string{fmt.Sprintf("pr=%d", failure.PRNumber)}, evidence...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	dir, err := layout.PipelineStateDir(failure.Workspace)
	if err != nil {
		return fail(fmt.Sprintf("merge-group code failure: pipeline state not readable: %v", err))
	}

	var rec pmstages.MergeQueueFailureRecord
	recPath := filepath.Join(dir, pmstages.MergeQueueFailureFile(failure.IssueNumber))
	data, err := os.ReadFile(recPath)
	if err != nil {
		return fail(fmt.Sprintf("merge-group code failure: %s not readable, so there is no failing job to send back to feature-dev: %v",
			pmstages.MergeQueueFailureFile(failure.IssueNumber), err))
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.Check == "" {
		return fail(fmt.Sprintf("merge-group code failure: %s names no failing check", pmstages.MergeQueueFailureFile(failure.IssueNumber)))
	}
	evidence := []string{
		fmt.Sprintf("check=%s", rec.Check),
		fmt.Sprintf("step=%s", rec.Step),
	}
	if rec.RunID > 0 {
		evidence = append(evidence, fmt.Sprintf("run_id=%d", rec.RunID))
	}
	if rec.JobID > 0 {
		evidence = append(evidence, fmt.Sprintf("job_id=%d", rec.JobID))
	}

	ledgerName := mergeQueueCodeRewindsFile(failure.IssueNumber)
	ledger := mergeQueueCodeRewinds{IssueNumber: failure.IssueNumber, RunID: failure.RunID, Checks: map[string]int{}}
	if b, err := os.ReadFile(filepath.Join(dir, ledgerName)); err == nil {
		var prior mergeQueueCodeRewinds
		if json.Unmarshal(b, &prior) == nil && prior.Checks != nil && prior.RunID != "" && prior.RunID == failure.RunID {
			ledger.Checks = prior.Checks
		}
	}
	key := mergeQueueCheckKey(rec)
	if ledger.Checks[key] > 0 {
		return fail(fmt.Sprintf("merge-group code failure in the PR's own check %q (step %q); already sent back to feature-dev once",
			rec.Check, rec.Step), evidence...)
	}

	if err := a.appendFeedback(filepath.Join(dir, fmt.Sprintf("feedback-%d.json", failure.IssueNumber)), rec); err != nil {
		return fail(fmt.Sprintf("merge-group code failure: writing the feature-dev feedback signal failed: %v", err), evidence...)
	}
	ledger.Checks[key]++
	out, err := json.MarshalIndent(ledger, "", "  ")
	if err == nil {
		_, err = layout.WriteClassFile(failure.Workspace, layout.ClassPipeline, ledgerName, bytes.NewReader(out))
	}
	if err != nil {
		// Without the ledger the bound cannot hold, so do not rewind.
		return fail(fmt.Sprintf("merge-group code failure: recording the rewind for check %q failed: %v", rec.Check, err), evidence...)
	}

	return RecoveryResult{
		Action: a.Name(),
		Reason: fmt.Sprintf("merge-group code failure in the PR's own check %q (step %q): sending the run back to feature-dev once with the job's log excerpt",
			rec.Check, rec.Step),
		Evidence:             evidence,
		FollowUp:             FollowUpStageCanResume,
		BacktrackTargetStage: string(state.StageFeatureDev),
	}
}

// appendFeedback adds the signal feature-dev's retry intake reads, keeping
// every signal already in the file.
func (a *MergeQueueCodeFailure) appendFeedback(path string, rec pmstages.MergeQueueFailureRecord) error {
	var fb feedbackOnDisk
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &fb) != nil {
			fb = feedbackOnDisk{}
		}
	}
	if fb.SchemaVersion == "" {
		fb.SchemaVersion = "1.1"
	}
	if fb.IssueNumber == 0 {
		fb.IssueNumber = rec.IssueNumber
	}
	if fb.CreatedAt == "" {
		fb.CreatedAt = a.now().UTC().Format(time.RFC3339)
	}
	evidence := []string{
		fmt.Sprintf("Merge-group job %q failed in step %q, one of the repository's own checks, after the PR merged with its base in the merge queue.", rec.Check, rec.Step),
	}
	if rec.LogExcerpt != "" {
		evidence = append(evidence, "Job log excerpt:\n"+rec.LogExcerpt)
	} else {
		evidence = append(evidence, "The job log could not be read; reproduce the check locally on the branch merged with the base.")
	}
	fb.Signals = append(fb.Signals, feedbackSignalOnDisk{
		SignalType:           mergeQueueCodeFailureSignal,
		EmittedByStage:       string(state.StagePRMerge),
		BacktrackTargetStage: string(state.StageFeatureDev),
		Rationale:            fmt.Sprintf("The merge queue removed the PR: check %q failed in the repository's own step %q on the merge-group commit. Fix the code so the check passes on the branch merged with its base.", rec.Check, rec.Step),
		Evidence:             evidence,
		Severity:             "warning",
	})
	out, err := json.MarshalIndent(fb, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// isMergeQueueInfraExhausted reports whether failure is pr-merge's refusal
// after an infrastructure removal outlasted every re-enqueue (#3198).
func isMergeQueueInfraExhausted(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.TerminalKind == "merge_queue_infra_failed" {
		return true
	}
	combined := strings.ToLower(failure.Reason + " " + failure.StageError + " " + strings.Join(failure.Evidence, " "))
	return strings.Contains(combined, pmstages.ReasonMergeQueueInfraExhausted)
}
