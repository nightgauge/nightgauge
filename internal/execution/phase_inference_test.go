package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseInferer_FeatureDevStartsAtValidateEnvironment(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	if !inf.enabled {
		t.Fatal("expected feature-dev inference to be enabled")
	}
	m, ok := inf.Start()
	if !ok {
		t.Fatal("expected a start marker")
	}
	if m.Name != "validate-environment" || m.Index != 0 || m.Total != 17 || m.Stage != "feature-dev" {
		t.Fatalf("unexpected start marker: %+v", m)
	}
}

func TestPhaseInferer_AdvancesThroughWaypoints(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()

	cases := []struct {
		tool  string
		input map[string]any
		want  string
		index int
	}{
		{"Read", map[string]any{"file_path": "PLAN.md"}, "read-planning-context", 1},
		{"Write", map[string]any{"file_path": "src/feature.ts"}, "implementation", 7},
		{"Bash", map[string]any{"command": "go test ./..."}, "testing", 8},
		{"Write", map[string]any{"file_path": "/repo/.git/nightgauge/pipeline/dev-42.json"}, "write-dev-context", 13},
		{"Bash", map[string]any{"command": "nightgauge project move-status 42"}, "sync-project-status", 14},
	}
	for _, c := range cases {
		m, _, ok := inf.ObserveToolUse(c.tool, c.input)
		if !ok {
			t.Fatalf("expected advancement for %s %v", c.tool, c.input)
		}
		if m.Name != c.want || m.Index != c.index {
			t.Fatalf("got %s/%d, want %s/%d", m.Name, m.Index, c.want, c.index)
		}
	}
}

func TestPhaseInferer_Monotonic(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); !ok {
		t.Fatal("expected implementation advancement")
	}
	// A later context read must not regress the phase.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "src/b.ts"}); ok {
		t.Fatal("expected no regression for a late read")
	}
	// Re-editing source must not re-emit implementation.
	if _, _, ok := inf.ObserveToolUse("Edit", map[string]any{"file_path": "src/c.ts"}); ok {
		t.Fatal("expected no duplicate implementation emission")
	}
}

func TestPhaseInferer_DevContextWriteIsNotImplementation(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	// Before any implementation edit it is not implementation, and not yet
	// write-dev-context either (#2181).
	if m, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "/repo/.git/nightgauge/pipeline/dev-42.json"}); ok {
		t.Fatalf("dev-context write before implementation advanced to %s/%d", m.Name, m.Index)
	}
	if _, _, ok := inf.ObserveToolUse("Edit", map[string]any{"file_path": "src/feature.ts"}); !ok {
		t.Fatal("an implementation edit did not advance")
	}
	m, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "/repo/.git/nightgauge/pipeline/dev-42.json"})
	if !ok {
		t.Fatal("expected advancement")
	}
	if m.Name != "write-dev-context" || m.Index != 13 {
		t.Fatalf("dev-context write should map to write-dev-context, got %s/%d", m.Name, m.Index)
	}
}

func TestPhaseInferer_RealMarkerWins(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	inf.ObserveRealMarker(11) // quality-review reached via a genuine marker
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("inferred implementation must not regress past a real marker")
	}
}

// TestPhaseInferer_DisabledForSelfReportingStages uses pr-merge, which really
// does self-report: its phases come from the deterministic Go reporter as
// direct function calls, so there is nothing for inference to add.
//
// It used to use feature-validate, on the premise that feature-validate
// self-reports. It does not (#1850). It is an LLM stage whose 23 markers are
// standalone printfs the model skips for exactly feature-dev's reason, and the
// observed run reported 0 of 23 across four minutes and $0.68 while exiting 0.
func TestPhaseInferer_DisabledForSelfReportingStages(t *testing.T) {
	inf := NewPhaseInferer("pr-merge")
	if inf.enabled {
		t.Fatal("pr-merge reports phases from the deterministic runner and must not infer them")
	}
	if _, ok := inf.Start(); ok {
		t.Fatal("disabled inferer must not emit a start marker")
	}
	if _, _, ok := inf.ObserveToolUse("Write", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("disabled inferer must not emit from tool use")
	}
}

// TestPhaseInferer_FeatureValidateInfersFromItsRealWork is the #1850 half: the
// worst-reported stage in the observed run now has the same fallback
// feature-dev already had.
func TestPhaseInferer_FeatureValidateInfersFromItsRealWork(t *testing.T) {
	inf := NewPhaseInferer("feature-validate")
	if !inf.enabled {
		t.Fatal("feature-validate must infer phases — it does not reliably emit markers")
	}

	m, ok := inf.Start()
	if !ok || m.Index != 0 || m.Total != 22 {
		t.Fatalf("Start should open validate-environment of 22, got %+v ok=%v", m, ok)
	}

	if m, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "dev-42.json"}); !ok ||
		m.Name != "read-dev-context" {
		t.Fatalf("a read should map to read-dev-context, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Bash", map[string]any{"command": "go test ./..."}); !ok ||
		m.Name != "run-tests" {
		t.Fatalf("a test command should map to run-tests, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Bash", map[string]any{"command": "git push -u origin HEAD"}); !ok ||
		m.Name != "commit-and-push" {
		t.Fatalf("a branch push should map to commit-and-push, got %+v ok=%v", m, ok)
	}
	if m, _, ok := inf.ObserveToolUse("Write", map[string]any{
		"file_path": "/repo/.git/nightgauge/pipeline/validate-42.json",
	}); !ok || m.Name != "write-validate-context" {
		t.Fatalf("the validate-context write should map to write-validate-context, got %+v ok=%v", m, ok)
	}

	// Monotonic, exactly as for feature-dev: a later read must not drag the
	// cursor back to an earlier phase.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "src/a.ts"}); ok {
		t.Fatal("a read after write-validate-context must not regress the cursor")
	}
}

func TestPhaseInferer_FeaturePlanningWaypoints(t *testing.T) {
	inf := NewPhaseInferer("feature-planning")
	if !inf.enabled {
		t.Fatal("expected feature-planning inference to be enabled")
	}
	m, ok := inf.Start()
	if !ok || m.Name != "feedback-context-check" || m.Index != 0 || m.Total != 13 {
		t.Fatalf("unexpected start marker: %+v ok=%v", m, ok)
	}

	cases := []struct {
		tool  string
		input map[string]any
		want  string
		index int
	}{
		{"Grep", map[string]any{"pattern": "x", "path": "docs/"}, "documentation-analysis", 5},
		{"Write", map[string]any{"file_path": "/repo/.git/nightgauge/plans/6-flutter-ia-nav.md"}, "produce-plan", 8},
		{"Write", map[string]any{"file_path": "/repo/.git/nightgauge/pipeline/planning-6.json"}, "write-planning-context", 9},
	}
	for _, c := range cases {
		m, _, ok := inf.ObserveToolUse(c.tool, c.input)
		if !ok {
			t.Fatalf("expected advancement for %s %v", c.tool, c.input)
		}
		if m.Name != c.want || m.Index != c.index {
			t.Fatalf("got %s/%d, want %s/%d", m.Name, m.Index, c.want, c.index)
		}
	}

	// A late read must not regress past produce-plan/write-planning-context.
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "docs/ARCHITECTURE.md"}); ok {
		t.Fatal("expected no regression for a late planning read")
	}
}

func TestPhaseInferer_FeaturePlanningRealMarkerWins(t *testing.T) {
	inf := NewPhaseInferer("feature-planning")
	inf.Start()
	inf.ObserveRealMarker(10) // write-planning-context reached via a genuine marker
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "docs/x.md"}); ok {
		t.Fatal("inferred documentation-analysis must not regress past a real marker")
	}
}

func TestExtractToolUses(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Write","input":{"file_path":"src/x.ts"}}]}}`
	uses := extractToolUses(line)
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(uses))
	}
	if uses[0].Name != "Write" || inputStr(uses[0].Input, "file_path") != "src/x.ts" {
		t.Fatalf("unexpected tool use: %+v", uses[0])
	}

	// Non-assistant lines yield nothing.
	if got := extractToolUses(`{"type":"user","message":{}}`); got != nil {
		t.Fatalf("expected nil for non-assistant line, got %+v", got)
	}
	// Malformed JSON yields nothing.
	if got := extractToolUses("not json"); got != nil {
		t.Fatalf("expected nil for malformed line, got %+v", got)
	}
}

// TestExtractToolUses_OpenCode covers ADR-022's `tool_use` shape (#1646):
// opencode 1.18.30 emits it only once a call completes or errors, never on
// start, with the tool's own lowercase id at part.tool and its input at
// part.state.input.
func TestExtractToolUses_OpenCode(t *testing.T) {
	line := `{"type":"tool_use","sessionID":"s1","part":{"type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"calc.py"}}}}`
	uses := extractToolUses(line)
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(uses))
	}
	if uses[0].Name != "Edit" {
		t.Fatalf("tool %q did not map to Edit: %+v", "edit", uses[0])
	}
	// filePath is copied to file_path so the SAME rule that reads Claude's
	// Write/Edit input matches, without a second rule table.
	if got := inputStr(uses[0].Input, "file_path"); got != "calc.py" {
		t.Fatalf("file_path = %q, want calc.py", got)
	}

	bash := extractToolUses(`{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{"command":"go test ./..."}}}}`)
	if len(bash) != 1 || bash[0].Name != "Bash" || inputStr(bash[0].Input, "command") != "go test ./..." {
		t.Fatalf("unexpected bash tool use: %+v", bash)
	}

	// A tool with no Claude-name mapping (task, webfetch, ...) yields nothing
	// — it advances no phase rule, exactly like an unrecognized Claude tool.
	if got := extractToolUses(`{"type":"tool_use","part":{"type":"tool","tool":"webfetch","state":{"input":{}}}}`); got != nil {
		t.Fatalf("expected nil for an unmapped OpenCode tool, got %+v", got)
	}
	// A non-tool_use OpenCode event (step_start) yields nothing.
	if got := extractToolUses(`{"type":"step_start","part":{"type":"step-start"}}`); got != nil {
		t.Fatalf("expected nil for step_start, got %+v", got)
	}
}

// TestPhaseInferer_OpenCodeFixtureAdvancesOnEdit feeds #1629's real-model
// OpenCode capture (testdata/opencode_stream_research_sample.jsonl, ADR-022)
// through the exact loop internal/execution/manager.go drives: extractToolUses
// then ObserveToolUse, one line at a time. The fixture's one `tool_use` is an
// `edit` of calc.py, which a feature-dev inferer must reach the
// "implementation" waypoint (index 8) on — an edit-heavy OpenCode stage is
// this issue's whole premise (#1646 "slow local models are not stalls").
//
// Deleting the OpenCode branch from extractToolUses (openCodeToolNameToClaudeName
// gone, or extractOpenCodeToolUse's call removed) leaves every line
// unrecognized: the cursor never leaves phase 0 ("validate-environment") and
// this test goes red.
func TestPhaseInferer_OpenCodeFixtureAdvancesOnEdit(t *testing.T) {
	data, err := os.ReadFile("testdata/opencode_stream_research_sample.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	if inf.cursor != 0 {
		t.Fatalf("cursor after Start = %d, want 0", inf.cursor)
	}

	reached := -1
	for _, line := range lines {
		for _, tu := range extractToolUses(line) {
			if m, _, ok := inf.ObserveToolUse(tu.Name, tu.Input); ok {
				reached = m.Index
			}
		}
	}
	if reached != 7 {
		t.Fatalf("phase index after the fixture = %d, want 7 (implementation)", reached)
	}
	if inf.cursor != 7 {
		t.Fatalf("cursor after the fixture = %d, want 7", inf.cursor)
	}
}

// TestPhaseInferer_LatePhasesWaitForImplementation: a status move and a build
// made while feature-dev validates its environment do not advance it past
// implementation (#2181). In #1659 leg 1 run 11 a phase-1 `move-status`
// jumped the stage to sync-project-status and reported thirteen phases passed
// with no file changed.
func TestPhaseInferer_LatePhasesWaitForImplementation(t *testing.T) {
	inf := NewPhaseInferer("feature-dev")
	inf.Start()
	if _, _, ok := inf.ObserveToolUse("Read", map[string]any{"file_path": "PLAN.md"}); !ok {
		t.Fatal("a read did not reach read-planning-context")
	}
	for _, c := range []struct {
		tool  string
		input map[string]any
	}{
		{"Bash", map[string]any{"command": "nightgauge project move-status 42 'In Progress'"}},
		{"Bash", map[string]any{"command": "go build ./..."}},
		{"Write", map[string]any{"file_path": "/repo/.git/nightgauge/pipeline/dev-42.json"}},
	} {
		if m, passed, ok := inf.ObserveToolUse(c.tool, c.input); ok {
			t.Fatalf("%s %v before any implementation edit advanced to %s/%d (passing %d phases)", c.tool, c.input, m.Name, m.Index, len(passed))
		}
	}
	m, _, ok := inf.ObserveToolUse("Edit", map[string]any{"file_path": "internal/github/client.go"})
	if !ok || m.Index != 7 {
		t.Fatalf("an implementation edit did not reach implementation: %+v %v", m, ok)
	}
	m, _, ok = inf.ObserveToolUse("Bash", map[string]any{"command": "nightgauge project move-status 42 Done"})
	if !ok || m.Index != 14 {
		t.Fatalf("a status move after implementation did not reach sync-project-status: %+v %v", m, ok)
	}
}

// TestPhaseInferer_PerCloneWrites replays the shared fixture the SDK's
// phaseInference.test.ts replays too, so both inferers reach the same phase
// for every way an agent writes a per-clone file (ADR-024 § 7): `nightgauge
// layout write|append` in a Bash call, or an edit under
// <git-common-dir>/nightgauge/.
func TestPhaseInferer_PerCloneWrites(t *testing.T) {
	type call struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
	}
	var fixture struct {
		Cases []struct {
			Name  string `json:"name"`
			Stage string `json:"stage"`
			Setup []call `json:"setup"`
			call
			Want int `json:"want"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "phase_inference_writes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			inf := NewPhaseInferer(c.Stage)
			inf.Start()
			for _, s := range c.Setup {
				inf.ObserveToolUse(s.Tool, s.Input)
			}
			got := -1
			if m, _, ok := inf.ObserveToolUse(c.Tool, c.Input); ok {
				got = m.Index
			}
			if got != c.Want {
				t.Fatalf("%s %v: phase %d, want %d", c.Tool, c.Input, got, c.Want)
			}
		})
	}
}

// TestExtractToolUses_GrokToolCall reads #533's real Grok capture: its one
// `tool_call` (run_terminal_command, "echo one") maps to Bash with rawInput as
// the input, and its four `tool_call_update` lines, which carry no toolName,
// yield nothing (#559).
func TestExtractToolUses_GrokToolCall(t *testing.T) {
	data, err := os.ReadFile("testdata/grok_stream_real_capture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var got []toolUse
	updates := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		uses := extractToolUses(line)
		if strings.Contains(line, `"type":"tool_call_update"`) {
			updates++
			if uses != nil {
				t.Fatalf("tool_call_update yielded %+v, want nil", uses)
			}
		}
		got = append(got, uses...)
	}
	if updates != 4 {
		t.Fatalf("fixture has %d tool_call_update lines, want 4", updates)
	}
	if len(got) != 1 || got[0].Name != "Bash" || inputStr(got[0].Input, "command") != "echo one" {
		t.Fatalf("tool uses = %+v, want one Bash {command: echo one}", got)
	}

	// An unmapped Grok tool matches no rule, like an unknown Claude tool.
	if uses := extractToolUses(`{"type":"tool_call","toolName":"web_search","rawInput":{}}`); uses != nil {
		t.Fatalf("unmapped Grok tool yielded %+v, want nil", uses)
	}
}

// TestPhaseInferer_GrokToolCallsAdvanceFeatureValidate drives Grok-shaped
// tool_call lines (rawInput keys as grok 1.0.70 records them) through the loop
// manager.go runs. Removing extractGrokToolUse from extractToolUses leaves the
// cursor at phase 0 and turns this red.
func TestPhaseInferer_GrokToolCallsAdvanceFeatureValidate(t *testing.T) {
	inf := NewPhaseInferer("feature-validate")
	inf.Start()
	steps := []struct {
		line string
		want int
	}{
		{`{"type":"tool_call","toolCallId":"c0","status":"pending","toolName":"read_file","rawInput":{"target_file":"dev.json","limit":50}}`, 1},
		{`{"type":"tool_call","toolCallId":"c1","status":"pending","toolName":"grep","rawInput":{"pattern":"func","glob":"*.go"}}`, 1},
		{`{"type":"tool_call","toolCallId":"c2","status":"pending","toolName":"run_terminal_command","rawInput":{"command":"go test ./..."}}`, 9},
		{`{"type":"tool_call_update","toolCallId":"c2","status":"completed","rawInput":{"command":"git push origin x"}}`, 9},
		{`{"type":"tool_call","toolCallId":"c3","status":"pending","toolName":"run_terminal_command","rawInput":{"command":"git push origin x"}}`, 17},
	}
	for i, s := range steps {
		for _, tu := range extractToolUses(s.line) {
			inf.ObserveToolUse(tu.Name, tu.Input)
		}
		if inf.cursor != s.want {
			t.Fatalf("step %d: cursor = %d, want %d", i, inf.cursor, s.want)
		}
	}
}
