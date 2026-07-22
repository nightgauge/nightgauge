package gates

import (
	"fmt"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/state"
)

// Default returns the canonical stage → gate map. Stages without an entry
// have no post-condition gate registered (the orchestrator skips Verify).
//
// To add a new gate: implement StageGate, append a key here, and add a row
// to the table in `docs/STAGE_GATES.md`.
func Default() map[state.PipelineStage]StageGate {
	return map[state.PipelineStage]StageGate{
		state.StageIssuePickup:     IssuePickupGate{},
		state.StageFeaturePlanning: FeaturePlanningGate{},
		state.StageFeatureDev:      FeatureDevGate{},
		state.StageFeatureValidate: FeatureValidateGate{},
		state.StagePRCreate:        PrCreateGate{},
		state.StagePRMerge:         PrMergeGate{},
	}
}

// LookupByStageName resolves a gate by stage name string. Used by the CLI
// `gate verify` subcommand and any caller that needs runtime lookup.
// Returns ok=false when the stage has no registered gate.
func LookupByStageName(name string) (StageGate, bool) {
	gate, ok := Default()[state.PipelineStage(name)]
	return gate, ok
}

// contextFilePath builds the workspace-relative path to a flat skill-output
// context file. Per ContextAssembler convention the layout is:
//
//	{workspace}/.nightgauge/pipeline/{type}-{issue}.json
//
// Type strings match the ContextFileType union in
// `packages/nightgauge-vscode/src/services/RepositoryContextLoader.ts`
// (`issue`, `planning`, `dev`, `validate`, `pr`).
func contextFilePath(workspace, contextType string, issueNumber int) string {
	return filepath.Join(workspace, ".nightgauge", "pipeline",
		fmt.Sprintf("%s-%d.json", contextType, issueNumber))
}
