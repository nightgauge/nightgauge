package state

// PhaseRegistry mirrors PHASE_REGISTRY
// (packages/nightgauge-sdk/src/events/phaseRegistry.ts) for the three agentic
// stages that report phase progress via marker parsing / inference rather than
// through stages.PhaseReporter. It is the one Go-side declaration of each such
// stage's ordered phase names — internal/execution's marker inference reads it
// to build its rule tables, and RuntimeState's stage-boundary settlement reads
// it to back-fill every phase name a stage never reported as `unreported`
// (#1885). A second copy anywhere else is exactly the drift #1885's fifth
// acceptance criterion forbids.
//
// Deterministic stages (issue-pickup, pr-create, pr-merge, ...) already report
// full start/complete/skip/supersede/fail pairs via
// internal/orchestrator/stages.PhaseReporter and have no entry here — the
// stage-boundary back-fill this registry feeds is a no-op for them.
var PhaseRegistry = map[PipelineStage][]string{
	StageFeatureDev: {
		"validate-environment",
		"read-planning-context",
		"batch-plan-detection",
		"feedback-context-check",
		"plan-verification",
		"knowledge-base-read",
		"recall-architectural-constraints",
		"standards-loading",
		"implementation",
		"testing",
		"e2e-testing",
		"quality-review",
		"self-correction",
		"feedback-signal-evaluation",
		"write-dev-context",
		"sync-project-status",
		"output-summary",
		"self-assessment",
	},
	StageFeaturePlanning: {
		"feedback-context-check",
		"load-context",
		"batch-detection",
		"ac-reconcile",
		"assess-complexity",
		"pattern-mining",
		"documentation-analysis",
		"knowledge-base-read",
		"recall-prior-decisions",
		"produce-plan",
		"write-planning-context",
		"knowledge-base-enrichment",
		"complete-stage",
		"self-assessment",
	},
	StageFeatureValidate: {
		"validate-environment",
		"read-dev-context",
		"batch-detection",
		"ac-completion-check",
		"detect-testing-environment",
		"ptc-detection",
		"freshness-check",
		"build-verification",
		"dead-code-detection",
		"baseline-comparison",
		"run-tests",
		"mobile-mcp-tests",
		"verify-ui-gate",
		"ci-parity-check",
		"knowledge-coverage-check",
		"pre-push-merge-validation",
		"generate-checklist",
		"feedback-signal-evaluation",
		"commit-and-push",
		"write-validate-context",
		"sync-project-status",
		"output-summary",
		"self-assessment",
	},
}

// RegistryPhaseNames returns the ordered phase names PhaseRegistry declares
// for stage, or nil for a stage with no entry (every deterministic stage).
func RegistryPhaseNames(stage PipelineStage) []string {
	return PhaseRegistry[stage]
}
