package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

func initEventFromFixture(t *testing.T, name string) *StreamEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		var event StreamEvent
		if json.Unmarshal([]byte(line), &event) == nil && event.Subtype == "init" {
			return &event
		}
	}
	t.Fatalf("%s has no system/init event", name)
	return nil
}

func TestEvaluateClaudePluginInit(t *testing.T) {
	present := initEventFromFixture(t, "claude_init_plugin_present.jsonl")
	absent := initEventFromFixture(t, "claude_init_plugin_absent.jsonl")
	duplicate := &StreamEvent{Plugins: []StreamPlugin{
		{Name: "nightgauge", Version: "1.0.0"}, {Name: "nightgauge", Version: "2.0.0"},
	}}
	lookalike := &StreamEvent{Plugins: []StreamPlugin{{Name: "nightgauge-extras", Version: "1.0.0"}}}

	cases := []struct {
		name        string
		event       *StreamEvent
		bundled     ClaudePluginVerdict
		noBundle    ClaudePluginVerdict
		wantVersion string
	}{
		{"real capture, one entry", present, ClaudePluginPass, ClaudePluginPass, "1.25.0"},
		{"real capture, plugin absent", absent, ClaudePluginStop, ClaudePluginWarn, ""},
		{"duplicate entries", duplicate, ClaudePluginStop, ClaudePluginWarn, ""},
		{"prefix is not the plugin", lookalike, ClaudePluginStop, ClaudePluginWarn, ""},
		{"init has no plugins key", &StreamEvent{}, ClaudePluginStop, ClaudePluginWarn, ""},
		{"work before any init", nil, ClaudePluginStop, ClaudePluginWarn, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateClaudePluginInit(true, tc.event)
			if got.Verdict != tc.bundled || got.Version != tc.wantVersion {
				t.Errorf("bundled: got %+v, want verdict %d version %q", got, tc.bundled, tc.wantVersion)
			}
			if got := EvaluateClaudePluginInit(false, tc.event); got.Verdict != tc.noBundle {
				t.Errorf("no bundle: got %+v, want verdict %d", got, tc.noBundle)
			}
		})
	}
}

// A real capture taken with --plugin-dir while a user-level nightgauge install
// was enabled (#2723): the CLI reports the bundled copy once, so each gate fires
// once and the stage runs.
func TestUserLevelInstallLeavesOneNightgaugeEntry(t *testing.T) {
	event := initEventFromFixture(t, "claude_init_plugin_user_install.jsonl")
	loaded := 0
	for _, p := range event.Plugins {
		if p.Name == claudePluginName {
			loaded++
			if p.Source != "nightgauge@inline" {
				t.Errorf("source = %q, want the --plugin-dir copy", p.Source)
			}
		}
	}
	if loaded != 1 {
		t.Fatalf("capture lists %d nightgauge plugins, want 1", loaded)
	}
	if got := EvaluateClaudePluginInit(true, event); got.Verdict != ClaudePluginPass {
		t.Errorf("reading = %+v, want a pass", got)
	}
}

// The floor is only supported if a capture from it passes the gate (#2807), so
// min_version cannot move without a capture from the new floor.
func TestFloorCaptureBacksTheManifestFloor(t *testing.T) {
	const name = "claude_init_plugin_floor.jsonl"
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var init struct {
		ClaudeCodeVersion string `json:"claude_code_version"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &init); err != nil {
		t.Fatal(err)
	}
	manifest, ok := adaptercompat.Get("claude-headless")
	if !ok {
		t.Fatal("no claude-headless manifest")
	}
	if init.ClaudeCodeVersion != manifest.MinVersion {
		t.Errorf("capture is from %q, min_version is %q", init.ClaudeCodeVersion, manifest.MinVersion)
	}
	if got := EvaluateClaudePluginInit(true, initEventFromFixture(t, name)); got.Verdict != ClaudePluginPass {
		t.Errorf("a bundled build on the floor reads %+v, want a pass", got)
	}
}

// A real stream puts hook_started ahead of init, and that must not read as work.
func TestClaudePluginGateIgnoresEventsBeforeInit(t *testing.T) {
	g := newClaudePluginGate(true)
	for _, e := range []*StreamEvent{{Type: "system", Subtype: "hook_started"}, {Type: "rate_limit_event"}, nil} {
		if g.observe(e) || g.decided {
			t.Fatalf("event %+v decided the gate", e)
		}
	}
	if !g.observe(&StreamEvent{Type: "assistant"}) {
		t.Fatal("work with no init did not stop a bundled stage")
	}
}

// A subagent's or a later init cannot change the stage's first reading.
func TestClaudePluginGateDecidesOnce(t *testing.T) {
	g := newClaudePluginGate(true)
	first := initEventFromFixture(t, "claude_init_plugin_present.jsonl")
	if g.observe(first) {
		t.Fatal("present fixture stopped the stage")
	}
	if g.observe(initEventFromFixture(t, "claude_init_plugin_absent.jsonl")) || g.stopped() {
		t.Fatal("a second init re-decided the gate")
	}
	if g.version() != "1.25.0" {
		t.Errorf("version = %q", g.version())
	}
}

func TestNilClaudePluginGateObservesNothing(t *testing.T) {
	var g *claudePluginGate
	if g.observe(&StreamEvent{Type: "assistant"}) || g.observeLine(StreamFormatClaude, workLine) ||
		g.stopped() || g.version() != "" || g.notice() != "" {
		t.Fatal("a nil gate, every other adapter's, must do nothing")
	}
}

// stubClaudeAdapter runs a script as the CLI under a chosen adapter name, and
// records the options the manager built.
type stubClaudeAdapter struct {
	name   string
	script string
	got    adapters.RunOptions
}

func (a *stubClaudeAdapter) Name() string { return a.name }
func (a *stubClaudeAdapter) BuildCommand(opts adapters.RunOptions) (string, []string, map[string]string) {
	a.got = opts
	return "sh", []string{"-c", a.script}, nil
}
func (a *stubClaudeAdapter) UsesStdin() bool { return false }
func (a *stubClaudeAdapter) Agentic() bool   { return true }

// A line printed only if the stage survives the gate, after a pause long enough
// to fail the run's elapsed-time assertion.
const lateWork = `sleep 20; echo '{"type":"result","usage":{"input_tokens":999,"output_tokens":999}}'`

// workLine is an assistant event carrying usage and a model, so anything that
// reads it books tokens and attributes the model.
const workLine = `{"type":"assistant","message":{"id":"msg_1","model":"claude-haiku-4-5-20251001",` +
	`"role":"assistant","content":[],"usage":{"input_tokens":7,"output_tokens":7}}}`

const resultLine = `{"type":"result","subtype":"success","total_cost_usd":1,` +
	`"usage":{"input_tokens":999,"output_tokens":999}}`

func assertBookedNothing(t *testing.T, result *adapters.RunResult) {
	t.Helper()
	if result.InputTokens != 0 || result.OutputTokens != 0 {
		t.Errorf("a stopped stage booked %d input and %d output tokens, want none",
			result.InputTokens, result.OutputTokens)
	}
	if result.ServedModel != "" {
		t.Errorf("ServedModel = %q, want none", result.ServedModel)
	}
}

func runGateStage(t *testing.T, adapter *stubClaudeAdapter, pluginDir, stage string) (*adapters.RunResult, time.Duration) {
	t.Helper()
	m := NewManager(realGitRepo(t), adapter)
	m.claudePluginDir = func() string { return pluginDir }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	result, err := m.RunStage(ctx, StageOptions{
		Repo: "acme/repo", IssueNumber: 2723, Stage: stage, Timeout: 60 * time.Second,
		Runtime: state.NewRuntimeState("acme/repo", 2723, "", "01890a5d-ac96-774b-bcce-b302099a8058"),
	})
	if err != nil && result == nil {
		t.Fatalf("RunStage: %v", err)
	}
	return result, time.Since(start)
}

func fixtureScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return "cat '" + path + "'"
}

func TestRunStageStopsABundledClaudeStageWithoutThePlugin(t *testing.T) {
	// Every Claude stage path through RunStage: both adapters, and a feature-dev
	// sub-session, which dispatches through RunStage under its own stage name.
	for _, tc := range []struct{ adapter, stage string }{
		{"claude", "feature-dev"},
		{"claude-sdk", "feature-dev"},
		{"claude", "feature-validate"},
		{"claude", "feature-dev-session-2"},
	} {
		t.Run(tc.adapter+"/"+tc.stage, func(t *testing.T) {
			adapter := &stubClaudeAdapter{name: tc.adapter,
				script: fixtureScript(t, "claude_init_plugin_absent.jsonl") + "; " + lateWork}
			result, elapsed := runGateStage(t, adapter, "/bundle/claude-plugins/nightgauge", tc.stage)

			if adapter.got.ClaudePluginDir != "/bundle/claude-plugins/nightgauge" {
				t.Errorf("ClaudePluginDir = %q", adapter.got.ClaudePluginDir)
			}
			if result.ExitCode == 0 {
				t.Error("a stopped stage exited 0")
			}
			if elapsed > 15*time.Second {
				t.Errorf("stage ran %s, so the gate did not kill it", elapsed)
			}
			assertBookedNothing(t, result)
			if got := terminalkind.Classify(result.Stderr); got != "gate_plugin_missing" {
				t.Errorf("kind = %q, stderr = %q", got, result.Stderr)
			}
			if result.PluginVersion != "" {
				t.Errorf("PluginVersion = %q, want none", result.PluginVersion)
			}
		})
	}
}

// No sleep: every line is already in the pipe when the kill lands, so only the
// gate deciding first keeps them out of the booking (#2807).
func TestRunStageStoppedByTheGateBooksNothing(t *testing.T) {
	emit := "echo '" + workLine + "'; echo '" + resultLine + "'"
	for name, script := range map[string]string{
		"lines buffered after a failed init": fixtureScript(t, "claude_init_plugin_absent.jsonl") + "; " + emit,
		"work with no init decides the stop": emit,
	} {
		t.Run(name, func(t *testing.T) {
			adapter := &stubClaudeAdapter{name: "claude", script: script}
			result, _ := runGateStage(t, adapter, "/bundle/claude-plugins/nightgauge", "feature-dev")
			assertBookedNothing(t, result)
			if got := terminalkind.Classify(result.Stderr); got != "gate_plugin_missing" {
				t.Errorf("kind = %q, stderr = %q", got, result.Stderr)
			}
		})
	}
}

func TestRunStagePassingTheGateStillBooks(t *testing.T) {
	adapter := &stubClaudeAdapter{name: "claude",
		script: fixtureScript(t, "claude_init_plugin_present.jsonl") + "; echo '" + workLine + "'"}
	result, _ := runGateStage(t, adapter, "/bundle/claude-plugins/nightgauge", "feature-dev")
	if result.InputTokens == 0 || result.ServedModel == "" {
		t.Errorf("booked %d input tokens, model %q", result.InputTokens, result.ServedModel)
	}
}

func TestRunStageRecordsTheLoadedPluginVersion(t *testing.T) {
	adapter := &stubClaudeAdapter{name: "claude", script: fixtureScript(t, "claude_init_plugin_present.jsonl")}
	result, _ := runGateStage(t, adapter, "/bundle/claude-plugins/nightgauge", "feature-dev")
	if result.ExitCode != 0 || result.PluginVersion != "1.25.0" {
		t.Errorf("exit %d, version %q", result.ExitCode, result.PluginVersion)
	}
	if strings.Contains(result.Stderr, "gate-plugin") {
		t.Errorf("a pass printed a notice: %q", result.Stderr)
	}
}

func TestRunStageWarnsWithNoBundle(t *testing.T) {
	adapter := &stubClaudeAdapter{name: "claude", script: fixtureScript(t, "claude_init_plugin_absent.jsonl")}
	result, _ := runGateStage(t, adapter, "", "feature-dev")
	if result.ExitCode != 0 {
		t.Errorf("exit %d: a contributor's stage must complete", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, gatePluginWarningMarker) {
		t.Errorf("no warning in stderr: %q", result.Stderr)
	}
	if strings.Contains(result.Stderr, gatePluginMissingMarker) {
		t.Errorf("warning carries the stop marker: %q", result.Stderr)
	}
}

func TestRunStageLeavesOtherAdaptersUngated(t *testing.T) {
	adapter := &stubClaudeAdapter{name: "codex", script: fixtureScript(t, "claude_init_plugin_absent.jsonl")}
	result, _ := runGateStage(t, adapter, "/bundle/claude-plugins/nightgauge", "feature-dev")
	if result.ExitCode != 0 || adapter.got.ClaudePluginDir != "" {
		t.Errorf("exit %d, dir %q", result.ExitCode, adapter.got.ClaudePluginDir)
	}
}
