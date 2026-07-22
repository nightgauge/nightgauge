package platform

import (
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// canonicalStageOrder defines the pipeline execution order for deriving
// deterministic stage array ordering (e.g. orderedStageNames /
// buildExecutionHistoryStages in execution_history_mapper.go). Matches the
// PipelineStage constants in internal/state/board_state.go.
var canonicalStageOrder = []string{
	string(state.StageIssuePickup),
	string(state.StageFeaturePlanning),
	string(state.StageFeatureDev),
	string(state.StageFeatureValidate),
	string(state.StagePRCreate),
	string(state.StagePRMerge),
}

// parseOptionalTime parses an RFC3339 string to a *time.Time pointer, normalised
// to UTC so it marshals with a trailing 'Z' (the platform's z.string().datetime()
// rejects numeric timezone offsets). Returns nil (not an error) when s is empty.
func parseOptionalTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	u := t.UTC()
	return &u, nil
}
