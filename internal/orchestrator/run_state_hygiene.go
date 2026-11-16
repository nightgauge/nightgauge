package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/state"

	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// archivePriorDevContext renames an existing dev-<N>.json at path to
// dev-<N>.<unix>.prior.json beside it, so a feature-dev dispatch never reads
// or inherits a prior attempt's handoff (#3081). It returns the archive path,
// or "" when there was nothing to archive.
func archivePriorDevContext(path string, now time.Time) (string, error) {
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
