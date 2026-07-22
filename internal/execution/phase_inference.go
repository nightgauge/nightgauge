package execution

import (
	"encoding/json"
	"regexp"
)

// Phase inference — deterministic phase progress from observable tool activity.
//
// Some skills do not reliably emit `<!-- phase:start ... -->` markers. The
// feature-dev stage in particular is edit-heavy (Read/Edit/Write dominate its
// tool calls; Bash is rare), so the model routinely skips the standalone printf
// phase-marker commands in its SKILL.md. The result is that the pipeline tree
// shows no phase progress for Feature Development even though planning and
// validation render fine (Issue #3760).
//
// Hardening the marker parser cannot fix this — a marker that is never emitted
// cannot be parsed. Instead this infers phase progress from the tool calls the
// agent actually makes, which the execution manager already scans, and feeds
// the inferred markers through the same PhaseEventFn channel as real markers.
//
// This mirrors packages/nightgauge-sdk/src/events/phaseInference.ts so the
// Go (auto/CLI) and TypeScript (VSCode/IPC) execution paths behave identically.

// featureDevPhases is the ordered phase name table for the feature-dev stage,
// mirroring PHASE_REGISTRY["feature-dev"] in the SDK. Index == position.
var featureDevPhases = []string{
	"validate-environment",             // 0
	"read-planning-context",            // 1
	"batch-plan-detection",             // 2
	"feedback-context-check",           // 3
	"plan-verification",                // 4
	"knowledge-base-read",              // 5
	"recall-architectural-constraints", // 6
	"standards-loading",                // 7
	"implementation",                   // 8
	"testing",                          // 9
	"e2e-testing",                      // 10
	"quality-review",                   // 11
	"self-correction",                  // 12
	"feedback-signal-evaluation",       // 13
	"write-dev-context",                // 14
	"sync-project-status",              // 15
	"output-summary",                   // 16
	"self-assessment",                  // 17
}

// featurePlanningPhases is the ordered phase name table for the feature-planning
// stage, mirroring PHASE_REGISTRY["feature-planning"] in the SDK. Index == position.
var featurePlanningPhases = []string{
	"feedback-context-check",    // 0
	"load-context",              // 1
	"batch-detection",           // 2
	"ac-reconcile",              // 3
	"assess-complexity",         // 4
	"pattern-mining",            // 5
	"documentation-analysis",    // 6
	"knowledge-base-read",       // 7
	"recall-prior-decisions",    // 8
	"produce-plan",              // 9
	"write-planning-context",    // 10
	"knowledge-base-enrichment", // 11
	"complete-stage",            // 12
	"self-assessment",           // 13
}

// stagePhaseTables maps a stage to its ordered phase names. Only stages that do
// NOT reliably self-report phase markers need an entry; others are no-ops.
var stagePhaseTables = map[string][]string{
	"feature-dev":      featureDevPhases,
	"feature-planning": featurePlanningPhases,
}

var (
	editToolRe        = regexp.MustCompile(`^(Edit|Write|MultiEdit|NotebookEdit)$`)
	readToolRe        = regexp.MustCompile(`^(Read|Grep|Glob)$`)
	pipelinePathRe    = regexp.MustCompile(`(^|/)\.nightgauge/`)
	devContextRe      = regexp.MustCompile(`(^|/)dev-\d+\.json$|\.nightgauge/pipeline/dev-`)
	testBuildRe       = regexp.MustCompile(`\b(vitest|jest|go\s+test|go\s+build|npm\s+(run\s+)?(-w\s+\S+\s+)?(test|build)|pytest|cargo\s+test)\b`)
	statusSyncRe      = regexp.MustCompile(`\b(move-status|gh\s+project)\b`)
	planFileRe        = regexp.MustCompile(`(^|/)\.nightgauge/plans/.+\.md$`)
	planningContextRe = regexp.MustCompile(`(^|/)planning-\d+\.json$|\.nightgauge/pipeline/planning-`)
)

// inferenceRule maps an observed tool call to a target phase index.
type inferenceRule struct {
	index int
	match func(toolName string, input map[string]any) bool
}

func inputStr(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// stageRules returns the ordered inference rules for a stage, or nil.
func stageRules(stage string) []inferenceRule {
	if _, ok := stagePhaseTables[stage]; !ok {
		return nil
	}
	switch stage {
	case "feature-dev":
		return []inferenceRule{
			{index: 1, match: func(name string, _ map[string]any) bool { return readToolRe.MatchString(name) }},
			{index: 8, match: func(name string, input map[string]any) bool {
				if !editToolRe.MatchString(name) {
					return false
				}
				path := inputStr(input, "file_path")
				if path == "" {
					path = inputStr(input, "notebook_path")
				}
				return path != "" && !pipelinePathRe.MatchString(path) && !devContextRe.MatchString(path)
			}},
			{index: 9, match: func(name string, input map[string]any) bool {
				return name == "Bash" && testBuildRe.MatchString(inputStr(input, "command"))
			}},
			{index: 14, match: func(name string, input map[string]any) bool {
				return editToolRe.MatchString(name) && devContextRe.MatchString(inputStr(input, "file_path"))
			}},
			{index: 15, match: func(name string, input map[string]any) bool {
				return name == "Bash" && statusSyncRe.MatchString(inputStr(input, "command"))
			}},
		}
	case "feature-planning":
		return []inferenceRule{
			// Reading docs/standards/source → documentation-analysis, where
			// planning spends the bulk of its time. Covers early phases 0-6.
			{index: 6, match: func(name string, _ map[string]any) bool { return readToolRe.MatchString(name) }},
			// Writing the plan file (.nightgauge/plans/{N}-*.md) → produce-plan.
			{index: 9, match: func(name string, input map[string]any) bool {
				return editToolRe.MatchString(name) && planFileRe.MatchString(inputStr(input, "file_path"))
			}},
			// Writing the planning-context handoff → write-planning-context.
			{index: 10, match: func(name string, input map[string]any) bool {
				return editToolRe.MatchString(name) && planningContextRe.MatchString(inputStr(input, "file_path"))
			}},
		}
	default:
		return nil
	}
}

// PhaseInferer infers phase progress for a single stage run. Monotonic: the
// cursor only ever advances. Real markers take precedence via ObserveRealMarker.
type PhaseInferer struct {
	stage   string
	phases  []string
	rules   []inferenceRule
	enabled bool
	cursor  int // highest phase index emitted/observed; -1 = none yet
}

// NewPhaseInferer builds an inferer for a stage. For stages without rules it is
// disabled and all methods are no-ops, leaving self-reporting stages untouched.
func NewPhaseInferer(stage string) *PhaseInferer {
	phases := stagePhaseTables[stage]
	rules := stageRules(stage)
	return &PhaseInferer{
		stage:   stage,
		phases:  phases,
		rules:   rules,
		enabled: len(phases) > 0 && len(rules) > 0,
		cursor:  -1,
	}
}

func (p *PhaseInferer) markerFor(index int) (*PhaseMarker, bool) {
	if index < 0 || index >= len(p.phases) {
		return nil, false
	}
	return &PhaseMarker{
		Name:  p.phases[index],
		Index: index,
		Total: len(p.phases),
		Stage: p.stage,
	}, true
}

func (p *PhaseInferer) advanceTo(index int) (*PhaseMarker, bool) {
	if index <= p.cursor {
		return nil, false
	}
	m, ok := p.markerFor(index)
	if !ok {
		return nil, false
	}
	p.cursor = index
	return m, true
}

// Start emits the stage's first phase. Call once when output begins.
func (p *PhaseInferer) Start() (*PhaseMarker, bool) {
	if !p.enabled {
		return nil, false
	}
	return p.advanceTo(0)
}

// ObserveToolUse returns a marker when the tool call advances the phase.
func (p *PhaseInferer) ObserveToolUse(toolName string, input map[string]any) (*PhaseMarker, bool) {
	if !p.enabled {
		return nil, false
	}
	best := -1
	for _, r := range p.rules {
		if r.index > best && r.index > p.cursor && r.match(toolName, input) {
			best = r.index
		}
	}
	if best == -1 {
		return nil, false
	}
	return p.advanceTo(best)
}

// ObserveRealMarker syncs the cursor forward when a genuine marker was emitted,
// so inferred markers never regress or duplicate a real one.
func (p *PhaseInferer) ObserveRealMarker(index int) {
	if p.enabled && index > p.cursor {
		p.cursor = index
	}
}

// toolUse is a single tool call extracted from an assistant message.
type toolUse struct {
	Name  string
	Input map[string]any
}

// extractToolUses parses an assistant stream-json line and returns its tool_use
// blocks. The CLI delivers tool calls inside complete `assistant` messages, so
// this is the primary signal for inference. Returns nil for non-assistant lines.
func extractToolUses(line string) []toolUse {
	var env struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string         `json:"type"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return nil
	}
	if env.Type != "assistant" {
		return nil
	}
	var out []toolUse
	for _, b := range env.Message.Content {
		if b.Type == "tool_use" && b.Name != "" {
			out = append(out, toolUse{Name: b.Name, Input: b.Input})
		}
	}
	return out
}
