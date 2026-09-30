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
	editToolRe = regexp.MustCompile(`^(Edit|Write|MultiEdit|NotebookEdit)$`)
	readToolRe = regexp.MustCompile(`^(Read|Grep|Glob)$`)
	// bookkeepingPathRe is a write that is not a source edit: the in-tree
	// .nightgauge/ files (config.yaml, knowledge/, ...) and the per-clone data
	// directory <git-common-dir>/nightgauge/ (ADR-024 § 7).
	bookkeepingPathRe = regexp.MustCompile(`(^|/)(\.nightgauge|\.git/nightgauge)/`)
	devContextRe      = regexp.MustCompile(`(^|/)dev-\d+\.json$|\.git/nightgauge/pipeline/dev-`)
	testBuildRe       = regexp.MustCompile(`\b(vitest|jest|go\s+test|go\s+build|npm\s+(run\s+)?(-w\s+\S+\s+)?(test|build)|pytest|cargo\s+test)\b`)
	statusSyncRe      = regexp.MustCompile(`\b(move-status|gh\s+project)\b`)
	planFileRe        = regexp.MustCompile(`(^|/)\.git/nightgauge/plans/.+\.md$`)
	planningContextRe = regexp.MustCompile(`(^|/)planning-\d+\.json$|\.git/nightgauge/pipeline/planning-`)
	validateContextRe = regexp.MustCompile(`(^|/)validate-\d+\.json$|\.git/nightgauge/pipeline/validate-`)
	gitPushRe         = regexp.MustCompile(`\bgit\s+push\b`)
	// layoutWriteRe finds `nightgauge layout write|append <class> <name>` in a
	// Bash command, the way agents store per-clone files (ADR-024 § 7). Each
	// flag (--from, --workdir) takes a value, before or after the verb.
	layoutWriteRe = regexp.MustCompile(`\bnightgauge\s+layout\s+` + layoutFlags +
		`(?:write|append)\s+` + layoutFlags +
		`(pipeline|plans|retros|logs)\s+["']?([^\s"'|;&<>()]+)`)
)

// layoutFlags matches zero or more `--flag value` / `--flag=value` pairs.
const layoutFlags = `(?:--?[\w-]+(?:=\S+|\s+\S+)\s+)*`

// writtenPaths returns the paths a tool call writes, for the rules that
// recognise a handoff or plan file: an edit tool's file_path, or, for Bash,
// `.git/nightgauge/<class>/<name>` for every `nightgauge layout write|append`
// in the command, so both forms meet the same path patterns.
func writtenPaths(toolName string, input map[string]any) []string {
	if editToolRe.MatchString(toolName) {
		if p := inputStr(input, "file_path"); p != "" {
			return []string{p}
		}
		return nil
	}
	if toolName != "Bash" {
		return nil
	}
	var out []string
	for _, m := range layoutWriteRe.FindAllStringSubmatch(inputStr(input, "command"), -1) {
		out = append(out, ".git/nightgauge/"+m[1]+"/"+m[2])
	}
	return out
}

// writes reports whether the tool call writes a path re matches.
func writes(toolName string, input map[string]any, re *regexp.Regexp) bool {
	for _, p := range writtenPaths(toolName, input) {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// inferenceRule maps an observed tool call to a target phase index.
type inferenceRule struct {
	index int
	match func(toolName string, input map[string]any) bool
	// after, when set, is the phase the run must already have reached for
	// this rule to fire. A late phase's tool call also happens early: the
	// skill moves the issue's status (`gh project`) and builds (`go build`)
	// while it validates its environment, so without this a status move
	// made in phase 1 advanced the stage to sync-project-status and marked
	// every phase between as passed with no file changed (#2181).
	after int
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
				return path != "" && !bookkeepingPathRe.MatchString(path) && !devContextRe.MatchString(path)
			}},
			// Tests, the dev handoff and the status sync each count only once
			// an implementation edit has been seen (after: 8).
			{index: 9, after: 8, match: func(name string, input map[string]any) bool {
				return name == "Bash" && testBuildRe.MatchString(inputStr(input, "command"))
			}},
			{index: 14, after: 8, match: func(name string, input map[string]any) bool {
				return writes(name, input, devContextRe)
			}},
			{index: 15, after: 8, match: func(name string, input map[string]any) bool {
				return name == "Bash" && statusSyncRe.MatchString(inputStr(input, "command"))
			}},
		}
	case "feature-planning":
		return []inferenceRule{
			// Reading docs/standards/source → documentation-analysis, where
			// planning spends the bulk of its time. Covers early phases 0-6.
			{index: 6, match: func(name string, _ map[string]any) bool { return readToolRe.MatchString(name) }},
			// Writing the plan file (plans/{N}-*.md in the per-clone data
			// directory) → produce-plan.
			{index: 9, match: func(name string, input map[string]any) bool {
				return writes(name, input, planFileRe)
			}},
			// Writing the planning-context handoff → write-planning-context.
			{index: 10, match: func(name string, input map[string]any) bool {
				return writes(name, input, planningContextRe)
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
				return writes(name, input, validateContextRe)
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
		if r.index > best && r.index > p.cursor && (r.after == 0 || p.cursor >= r.after) && r.match(toolName, input) {
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

// openCodeToolNameToClaudeName maps an OpenCode tool_use event's part.tool
// (opencode's own lowercase tool id, ADR-022 § 1 / opencode_usage.go) to the
// Claude tool name stageRules matches on, so the one rule table drives phase
// inference on both the Claude assistant-message shape and OpenCode's. write
// and apply_patch join edit: opencode 1.18.30 has no permission or usage
// distinction between them (openCodeToolRejectionPermission), and neither
// does an edit-heavy stage's phase progress. A tool this table has no entry
// for (task, webfetch, websearch, todowrite, skill, lsp, ...) matches no
// phase rule, same as an unrecognized Claude tool name would.
var openCodeToolNameToClaudeName = map[string]string{
	"bash":        "Bash",
	"edit":        "Edit",
	"write":       "Edit",
	"apply_patch": "Edit",
	"read":        "Read",
	"glob":        "Glob",
	"grep":        "Grep",
}

// extractOpenCodeToolUse reads one OpenCode `tool_use` event (opencode
// 1.18.30's `run --format json`, ADR-022): `{"type":"tool_use","part":
// {"type":"tool","tool":"edit","state":{"input":{"filePath":...}}}}`. OpenCode
// emits this only once a call has completed or errored, never on start
// (#1635's plugin-handshake comment records the same observation), so a
// phase this advances to was actually reached, not merely attempted.
//
// filePath, OpenCode's input key for edit/write/apply_patch, is copied to
// file_path so the SAME rules that read Claude's Edit/Write input
// (inputStr(input, "file_path")) match here without a second rule table.
func extractOpenCodeToolUse(line string) (toolUse, bool) {
	var env struct {
		Type string `json:"type"`
		Part struct {
			Type  string `json:"type"`
			Tool  string `json:"tool"`
			State struct {
				Input map[string]any `json:"input"`
			} `json:"state"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return toolUse{}, false
	}
	if env.Type != "tool_use" || env.Part.Type != "tool" {
		return toolUse{}, false
	}
	name, ok := openCodeToolNameToClaudeName[env.Part.Tool]
	if !ok {
		return toolUse{}, false
	}
	input := env.Part.State.Input
	if fp, ok := input["filePath"].(string); ok {
		input["file_path"] = fp
	}
	return toolUse{Name: name, Input: input}, true
}

// extractToolUses parses one line of stage stdout and returns its tool_use
// blocks: an assistant stream-json line (the CLI's shape, tool calls inside
// complete `assistant` messages) or an OpenCode `tool_use` event
// (extractOpenCodeToolUse). Returns nil for a line that is neither.
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
	if err := json.Unmarshal([]byte(line), &env); err == nil && env.Type == "assistant" {
		var out []toolUse
		for _, b := range env.Message.Content {
			if b.Type == "tool_use" && b.Name != "" {
				out = append(out, toolUse{Name: b.Name, Input: b.Input})
			}
		}
		return out
	}
	if tu, ok := extractOpenCodeToolUse(line); ok {
		return []toolUse{tu}
	}
	return nil
}
