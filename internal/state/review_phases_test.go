package state

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
)

func TestReviewPhases(t *testing.T) {
	phase := func(stage PipelineStage, name, status string) PhaseRecord {
		return PhaseRecord{Stage: stage, Name: name, Status: status}
	}
	cases := []struct {
		name  string
		rs    *RuntimeState
		stage PipelineStage
		want  string
	}{
		{"nil state says nothing", nil, StageFeatureDev, ""},
		{"another stage says nothing", &RuntimeState{}, StageFeatureValidate, ""},
		{"no record is missing", &RuntimeState{}, StageFeatureDev, diagnostics.ReviewPhasesMissing},
		{"a back-filled record is missing", &RuntimeState{PhaseHistory: []PhaseRecord{
			phase(StageFeatureDev, reviewPhaseName, "unreported")}}, StageFeatureDev, diagnostics.ReviewPhasesMissing},
		{"a skipped record is missing", &RuntimeState{PhaseHistory: []PhaseRecord{
			phase(StageFeatureDev, reviewPhaseName, "skipped")}}, StageFeatureDev, diagnostics.ReviewPhasesMissing},
		{"another stage's phase does not count", &RuntimeState{PhaseHistory: []PhaseRecord{
			phase(StageFeatureValidate, reviewPhaseName, "complete")}}, StageFeatureDev, diagnostics.ReviewPhasesMissing},
		{"a running review ran", &RuntimeState{PhaseHistory: []PhaseRecord{
			phase(StageFeatureDev, reviewPhaseName, "running")}}, StageFeatureDev, diagnostics.ReviewPhasesRan},
		{"a complete review ran", &RuntimeState{PhaseHistory: []PhaseRecord{
			phase(StageFeatureDev, reviewPhaseName, "complete")}}, StageFeatureDev, diagnostics.ReviewPhasesRan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rs.ReviewPhases(tc.stage); got != tc.want {
				t.Errorf("ReviewPhases = %q, want %q", got, tc.want)
			}
		})
	}
}
