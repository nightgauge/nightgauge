package execution

import (
	"encoding/json"
	"regexp"

	"github.com/nightgauge/nightgauge/internal/state"
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

// stagePhaseTables maps a stage to its ordered phase names, read from
// internal/state's PhaseRegistry — the one Go-side declaration of these
// tables (#1885). Only stages that do NOT reliably self-report phase markers
// need an entry; others are no-ops.
var stagePhaseTables = map[string][]string{
	"feature-dev":      state.RegistryPhaseNames(state.StageFeatureDev),
	"feature-planning": state.RegistryPhaseNames(state.StageFeaturePlanning),
	"feature-validate": state.RegistryPhaseNames(state.StageFeatureValidate),
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
	validateContextRe = regexp.MustCompile(`(^|/)validate-\d+\.json$|\.nightgauge/pipeline/validate-`)
	gitPushRe         = regexp.MustCompile(`\bgit\s+push\b`)
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
	case "feature-validate":
		return []inferenceRule{
			// Any read → read-dev-context. The stage opens by reading the dev
			// handoff and the files it names, so this is the earliest honest
			// waypoint and covers phases 0-1.
			{index: 1, match: func(name string, _ map[string]any) bool { return readToolRe.MatchString(name) }},
			// A build/test command → run-tests, where the stage spends nearly
			// all of its wall-clock. Deliberately the same testBuildRe
			// feature-dev uses: the two stages run the same commands, and a
			// second pattern for one vocabulary is a drift source.
			{index: 10, match: func(name string, input map[string]any) bool {
				return name == "Bash" && testBuildRe.MatchString(inputStr(input, "command"))
			}},
			// Pushing the branch → commit-and-push. Anchored on `git push`
			// rather than any git call, because the stage runs `git status` and
			// `git diff` throughout and neither is this waypoint.
			{index: 18, match: func(name string, input map[string]any) bool {
				return name == "Bash" && gitPushRe.MatchString(inputStr(input, "command"))
			}},
			// Writing the validate-context handoff → write-validate-context.
			{index: 19, match: func(name string, input map[string]any) bool {
				return editToolRe.MatchString(name) && validateContextRe.MatchString(inputStr(input, "file_path"))
			}},
			// Board status sync → sync-project-status, the stage's last
			// observable act before it narrates.
			{index: 20, match: func(name string, input map[string]any) bool {
				return name == "Bash" && statusSyncRe.MatchString(inputStr(input, "command"))
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
		stage:  stage,
		phases: phases,
		rules:  rules,
		// A phase table alone is enough (#1924). Gap-fill derives progress from
		// ORDERING, so it works for a stage that self-reports markers and has no
		// inference rules at all; rules only add extra landmarks to fill between.
		// Requiring both is what kept gap-fill off any stage the rule table had
		// not been taught yet.
		enabled: len(phases) > 0,
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

// advanceTo moves the cursor to index and returns the marker for it, plus the
// markers for every phase the cursor jumped OVER (#1924).
//
// The jumped-over phases are the whole point: a run that reports 1 and then 9
// was in 2..8 and said nothing, and before this they vanished until the
// end-of-stage back-fill. They are returned separately from the advance because
// they are a weaker claim — see RuntimeState.PassPhase.
func (p *PhaseInferer) advanceTo(index int) (*PhaseMarker, []PhaseMarker, bool) {
	if index <= p.cursor {
		return nil, nil, false
	}
	m, ok := p.markerFor(index)
	if !ok {
		return nil, nil, false
	}
	passed := p.gapBelow(index)
	p.cursor = index
	return m, passed, true
}

// gapBelow returns markers for the unreported phases strictly between the
// cursor and index. Callers hold no lock; PhaseInferer is single-goroutine,
// driven by the one output-scanning loop in manager.go.
func (p *PhaseInferer) gapBelow(index int) []PhaseMarker {
	var passed []PhaseMarker
	for i := p.cursor + 1; i < index; i++ {
		if m, ok := p.markerFor(i); ok {
			passed = append(passed, *m)
		}
	}
	return passed
}

// Start emits the stage's first phase. Call once when output begins.
func (p *PhaseInferer) Start() (*PhaseMarker, bool) {
	if !p.enabled {
		return nil, false
	}
	m, _, ok := p.advanceTo(0)
	return m, ok
}

// ObserveToolUse returns a marker when the tool call advances the phase.
func (p *PhaseInferer) ObserveToolUse(toolName string, input map[string]any) (*PhaseMarker, []PhaseMarker, bool) {
	if !p.enabled {
		return nil, nil, false
	}
	best := -1
	for _, r := range p.rules {
		if r.index > best && r.index > p.cursor && r.match(toolName, input) {
			best = r.index
		}
	}
	if best == -1 {
		return nil, nil, false
	}
	return p.advanceTo(best)
}

// ObserveRealMarker syncs the cursor forward when a genuine marker was emitted,
// so inferred markers never regress or duplicate a real one.
// A real marker also closes a gap: a skill that printf's 0 then 6 was in 1..5
// and never said so, so the jumped-over phases are returned here too (#1924).
func (p *PhaseInferer) ObserveRealMarker(index int) []PhaseMarker {
	if !p.enabled || index <= p.cursor {
		return nil
	}
	passed := p.gapBelow(index)
	p.cursor = index
	return passed
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
