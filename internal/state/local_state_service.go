package state

import (
	"errors"
	"os"
	"strconv"
)

// LocalStateService reads pipeline state from local disk files.
// It implements executor.StateServiceIface (structurally — no import of executor package).
// GetState(key) accepts an issue number as a decimal string and reads
// runtime-{N}.json from the configured state directory.
type LocalStateService struct {
	stateDir string
}

// NewLocalStateService creates a LocalStateService that reads state files from stateDir.
// stateDir is typically {workspaceRoot}/.nightgauge/pipeline.
func NewLocalStateService(stateDir string) *LocalStateService {
	return &LocalStateService{stateDir: stateDir}
}

// GetState returns pipeline status for the given key (decimal issue number string).
// Returns nil when no state file exists (caller should treat nil as idle).
// Returns a map[string]interface{} with fields: status, stage, startedAt, issueNumber.
func (s *LocalStateService) GetState(key string) interface{} {
	issueNumber, err := strconv.Atoi(key)
	if err != nil || issueNumber <= 0 {
		return nil
	}

	rs, err := LoadPersistedState(s.stateDir, issueNumber)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return nil
	}

	status := "running"
	if rs.IsComplete() {
		status = "completed"
	}

	result := map[string]interface{}{
		"status":      status,
		"issueNumber": rs.IssueNumber,
	}
	if rs.Stage != "" {
		result["stage"] = string(rs.Stage)
	}
	if !rs.StartedAt.IsZero() {
		result["startedAt"] = rs.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return result
}
