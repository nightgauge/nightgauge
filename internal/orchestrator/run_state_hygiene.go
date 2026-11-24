package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/state"

	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// adoptableRunStateID returns the run ID a run for issue may inherit from
// run-state.json, or "" with the reason it may not (#3081). run-state.json is
// per clone, not per issue: the record a previous run left behind names that
// run's issue, and adopting its ID would file this run's snapshots, exit
// record and trace under another issue's identity.
func adoptableRunStateID(rs *runstate.RunState, issue int) (string, string) {
	if rs == nil || rs.RunID == "" {
		return "", ""
	}
	if rs.IssueNumber != issue {
		return "", fmt.Sprintf("run-state.json names issue #%d, not #%d", rs.IssueNumber, issue)
	}
	if !runstate.IsIdentity(rs.RunID) {
		return "", fmt.Sprintf("non-identity run id %q", rs.RunID)
	}
	return rs.RunID, ""
}

// archivePriorHandoff renames an existing handoff such as dev-<N>.json at path
// to dev-<N>.<unix>.prior.json beside it, so a stage never reads or inherits a
// prior attempt's handoff (#3081, #3565). It returns the archive path, or ""
// when there was nothing to archive.
func archivePriorHandoff(path string, now time.Time) (string, error) {
	if path == "" {
		return "", nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	archived := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.%d.prior.json", base, now.Unix()))
	if err := os.Rename(path, archived); err != nil {
		return "", err
	}
	return archived, nil
}

// startsPastFeatureDev reports whether a run entering at stages[startIdx]
// has already passed feature-dev, so the dev-<N>.json on disk belongs to this
// run and must not be archived on a later feature-dev lap (#3081).
func startsPastFeatureDev(stages []state.PipelineStage, startIdx int) bool {
	for i, st := range stages {
		if st == state.StageFeatureDev {
			return startIdx > i
		}
	}
	return false
}

// startsAtOrBeforeFeaturePlanning reports whether a run entering at
// stages[startIdx] has not yet passed feature-planning, so every handoff on
// disk from dev onward belongs to an earlier attempt (#3565).
func startsAtOrBeforeFeaturePlanning(stages []state.PipelineStage, startIdx int) bool {
	for i, st := range stages {
		if st == state.StageFeaturePlanning {
			return startIdx <= i
		}
	}
	return false
}

// priorAttemptHandoffs are the handoffs a run started at or before
// feature-planning must not inherit from an earlier attempt (#3565).
var priorAttemptHandoffs = []string{"dev", "validate", "quality-review", feedbackHandoffName}

const feedbackHandoffName = "feedback"

// archivePriorAttemptHandoffs archives the dev, validate, quality-review and
// feedback handoffs an earlier attempt of issue left in ws (#3565). The
// feedback file is rewritten holding only its OPERATOR_STEER signals, because
// the Action Center writes the retry's steer there before the run starts. It
// returns every archive path written. An error on one handoff does not stop
// the rest; all errors are joined. An unreadable or malformed feedback file is
// still archived, with nothing kept, and reported as an error.
func archivePriorAttemptHandoffs(ws string, issue int, now time.Time) ([]string, error) {
	var archived []string
	var errs []error
	for _, name := range priorAttemptHandoffs {
		path, err := stagecontext.ContextPath(ws, issue, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve %s: %w", name, err))
			continue
		}
		var steers []FeedbackSignal
		var fc FeedbackContext
		if name == feedbackHandoffName {
			data, rerr := os.ReadFile(path)
			if rerr == nil {
				rerr = json.Unmarshal(data, &fc)
			}
			if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("read operator steer from %s: %w", path, rerr))
			}
			if rerr == nil {
				for _, sig := range fc.Signals {
					if sig.SignalType == OperatorSteerSignalType {
						steers = append(steers, sig)
					}
				}
			}
		}
		dst, err := archivePriorHandoff(path, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("archive %s: %w", path, err))
			continue
		}
		if dst == "" {
			continue
		}
		archived = append(archived, dst)
		if len(steers) > 0 {
			fc.Signals = steers
			data, err := json.MarshalIndent(fc, "", "  ")
			if err == nil {
				err = atomicWriteFile(path, append(data, '\n'))
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("keep operator steer in %s: %w", path, err))
			}
		}
	}
	return archived, errors.Join(errs...)
}
