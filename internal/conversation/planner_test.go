package conversation

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/platform"
)

func testPlanner() planner {
	p := defaultPlanner(func(string) (string, error) { return "/usr/bin/true", nil })
	p.getenv = func(string) string { return "" }
	return p
}

// The permission floor: whatever the workspace's auto-accept settings, mode,
// effort or model, no conversation argv carries a bypass flag, a widening
// permission mode or a session handle, and every one carries the posture the
// spike requires. Adding a bypass flag to any branch of the builder turns
// this red.
func TestPlan_NoConfigurationReachesABypassFlag(t *testing.T) {
	hermetic(t)
	forbiddenFlags, forbiddenModes := adapters.ConversationForbiddenFlags()
	required := []string{"-p", "--no-session-persistence", "--restricted", "--strict-mcp-config",
		"--include-partial-messages", "--tools"}
	root := workspace(t, "")
	modeFile := layouttest.CheckoutPath(t, root, "performance-mode.yaml")
	for _, autoAccept := range []string{"true", "false"} {
		for _, mode := range []string{"", "efficiency", "elevated", "maximum", "frontier"} {
			for _, model := range []string{"", "haiku", "sonnet", "opus", "fable"} {
				for _, effort := range []string{"", "low", "high", "max"} {
					cfg := "ui:\n  core:\n    adapter: claude-headless\n"
					if model != "" {
						cfg += "    default_model: " + model + "\n"
					}
					cfg += "human_in_the_loop:\n  auto_accept_stages: " + autoAccept +
						"\n  auto_accept_permissions: " + autoAccept + "\n"
					if effort != "" {
						cfg += "model_routing:\n  default_effort: " + effort + "\n"
					}
					writeConfig(t, root, cfg)
					_ = os.Remove(modeFile)
					if mode != "" {
						if err := os.WriteFile(modeFile, []byte("mode: "+mode+"\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					p, err := testPlanner().plan(root)
					if err != nil {
						t.Fatalf("auto-accept %s, mode %q, model %q, effort %q: %v", autoAccept, mode, model, effort, err)
					}
					// The variations reach the plan: the enumeration is not
					// one argv two hundred times.
					if mode == "maximum" && p.model != "claude-opus-5-5" ||
						mode == "" && model == "haiku" && p.model != "claude-haiku-4-5-20251001" {
						t.Fatalf("mode %q model %q planned %s", mode, model, p.model)
					}
					args := p.args
					for _, f := range forbiddenFlags {
						if slices.Contains(args, f) {
							t.Errorf("argv carries %s: %v", f, args)
						}
					}
					for i, a := range args {
						if a == "--permission-mode" && (i+1 >= len(args) || args[i+1] != "dontAsk") {
							t.Errorf("permission mode is not dontAsk: %v", args)
						}
						if slices.Contains(forbiddenModes, a) {
							t.Errorf("argv carries permission mode %s: %v", a, args)
						}
					}
					for _, r := range required {
						if !slices.Contains(args, r) {
							t.Errorf("argv lacks %s: %v", r, args)
						}
					}
					if !containsPair(args, "--permission-mode", "dontAsk") || !containsPair(args, "--tools", "") {
						t.Errorf("argv lacks dontAsk or the empty tool list: %v", args)
					}
				}
			}
		}
	}
}

// Spike #1568 § 11's effort rule: no flag, reported as not applied, for a
// model with no effort axis; a declared rung passed through. The model is
// the workspace's default band clamped into the mode's envelope.
func TestPlan_ResolvesModelAndEffortOnTheWorkspace(t *testing.T) {
	hermetic(t)
	cases := []struct {
		name, config, mode string
		model, effort      string
	}{
		{"default band", "model_routing:\n  default_effort: medium\n", "", "claude-sonnet-5-5", "medium"},
		{"haiku has no effort axis", "ui:\n  core:\n    default_model: haiku\nmodel_routing:\n  default_effort: high\n", "", "claude-haiku-4-5-20251001", ""},
		{"maximum floors the band and the effort", "ui:\n  core:\n    default_model: haiku\n", "maximum", "claude-opus-5-5", "high"},
		{"efficiency caps the band and the effort", "ui:\n  core:\n    default_model: opus\nmodel_routing:\n  default_effort: max\n", "efficiency", "claude-sonnet-5-5", "medium"},
		{"no effort named", "", "", "claude-sonnet-5-5", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := workspace(t, "ui:\n  core:\n    adapter: claude-headless\n"+strings.TrimPrefix(tc.config, "ui:\n  core:\n"))
			if tc.mode != "" {
				_ = os.WriteFile(layouttest.CheckoutPath(t, root, "performance-mode.yaml"), []byte("mode: "+tc.mode+"\n"), 0o644)
			}
			p, err := testPlanner().plan(root)
			if err != nil {
				t.Fatal(err)
			}
			if p.model != tc.model || p.effort != tc.effort {
				t.Errorf("model %s effort %q, want %s %q", p.model, p.effort, tc.model, tc.effort)
			}
			if containsPair(p.args, "--model", tc.model) == false {
				t.Errorf("argv %v lacks --model %s", p.args, tc.model)
			}
			if hasEffort := slices.Contains(p.args, "--effort"); hasEffort != (tc.effort != "") {
				t.Errorf("argv %v: --effort present %v, want %v", p.args, hasEffort, tc.effort != "")
			}
			if tc.effort == "" && p.effortNote == "" {
				t.Error("no effort applied, and no note says why")
			}
		})
	}
}

// A workspace the turn host cannot serve is refused before anything is
// claimed: an adapter with no conversational builder, a missing binary, a
// rung the model does not declare.
func TestPlan_Refusals(t *testing.T) {
	hermetic(t)
	var r *refusal
	_, err := testPlanner().plan(workspace(t, "ui:\n  core:\n    adapter: grok\n"))
	if !errors.As(err, &r) || !strings.HasPrefix(r.detail, "adapter-not-conversational:") {
		t.Errorf("grok: %v", err)
	}
	missing := testPlanner()
	missing.lookPath = exec.LookPath
	t.Setenv("PATH", t.TempDir())
	if _, err := missing.plan(workspace(t, "ui:\n  core:\n    adapter: claude\n")); !errors.As(err, &r) ||
		!strings.HasPrefix(r.detail, "adapter-unavailable:") {
		t.Errorf("no claude on PATH: %v", err)
	}
	undeclared := testPlanner()
	undeclared.builderFor = func(string) (adapters.ConversationBuilder, bool) { return effortBuilder{}, true }
	undeclared.resolveProfile = func(string) (platform.ExecutionProfile, error) {
		return platform.ExecutionProfile{Adapter: "claude-headless", PerformanceMode: "elevated", Effort: "ultra"}, nil
	}
	if _, err := undeclared.plan(workspace(t, "")); !errors.As(err, &r) || !strings.HasPrefix(r.detail, "effort-unsupported:") {
		t.Errorf("an undeclared rung: %v", err)
	}
}

type effortBuilder struct{ stubBuilder }

func (effortBuilder) HasEffortControl() bool { return true }

// The viability bar's recorded evidence (spike #1568 § 13), pinned against
// the captures in testdata, which a real claude CLI produced with this
// builder's argv:
//   - streaming within a message: a reply of at least 50 words arrives as
//     at least two text deltas of one content block, the first at least
//     500 ms before that block's content_block_stop;
//   - accounting: the terminal event yields the turn's own usage, which the
//     pipeline's accumulator reads, partial-message events included;
//   - effort: the CLI reports it applied the rung it was given.
func TestViability_RecordedStreamingCapture(t *testing.T) {
	f, err := os.Open(fixture("claude_turn_stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var timing struct {
		Offsets []int `json:"offsets_ms"`
	}
	raw, err := os.ReadFile(fixture("claude_turn_stream.timing.json"))
	if err != nil || json.Unmarshal(raw, &timing) != nil {
		t.Fatalf("timing: %v", err)
	}
	acc := &execution.TokenAccumulator{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	line := 0
	blockDeltas := map[int]int{}
	firstDelta := map[int]int{}
	stopAt := map[int]int{}
	var text strings.Builder
	var effortActive *bool
	var resultUsage *execution.TokenUsage
	for sc.Scan() {
		ev, _ := acc.ParseStreamLine(sc.Text())
		at := timing.Offsets[line]
		line++
		if ev == nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "init" {
			effortActive = ev.PerTurnEffortActive
		}
		if block, delta, ok := ev.TextDelta(); ok && delta != "" {
			if blockDeltas[block] == 0 {
				firstDelta[block] = at
			}
			blockDeltas[block]++
			text.WriteString(delta)
		}
		if ev.Type == "stream_event" && ev.Event.Type == "content_block_stop" {
			stopAt[ev.Event.Index] = at
		}
		if ev.Type == "result" {
			resultUsage = ev.Usage
		}
	}
	if line != len(timing.Offsets) {
		t.Fatalf("%d lines, %d offsets", line, len(timing.Offsets))
	}
	if words := len(strings.Fields(text.String())); words < 50 {
		t.Errorf("the reply has %d words, want at least 50", words)
	}
	streamed := false
	for block, n := range blockDeltas {
		if n >= 2 && stopAt[block]-firstDelta[block] >= 500 {
			streamed = true
		}
	}
	if !streamed {
		t.Errorf("no block streamed: deltas %v, first %v, stop %v", blockDeltas, firstDelta, stopAt)
	}
	if effortActive == nil || !*effortActive {
		t.Error("the capture does not show the effort applied")
	}
	if resultUsage == nil || acc.InputTokens != resultUsage.InputTokens || acc.OutputTokens != resultUsage.OutputTokens ||
		acc.CacheCreated != resultUsage.CacheCreationInput || acc.CacheRead != resultUsage.CacheReadInput {
		t.Errorf("accumulated %+v, result usage %+v", acc, resultUsage)
	}
}

// Multi-turn by replay, recorded: turn 2 ran in a new process whose only
// knowledge of turn 1 was the replayed context in its stdin, and named the
// fact only turn 1 held (TestRenderMessage_MatchesTheRecordedReplay pins
// that stdin to this host's renderer).
func TestViability_RecordedReplay(t *testing.T) {
	if got := fixtureText(t, "claude_turn_replay_1.jsonl"); strings.TrimSpace(got) != "noted" {
		t.Errorf("turn 1 answered %q", got)
	}
	if got := strings.ToLower(fixtureText(t, "claude_turn_replay_2.jsonl")); !strings.Contains(got, "plum") {
		t.Errorf("turn 2 answered %q", got)
	}
	for _, name := range []string{"claude_turn_replay_1.jsonl", "claude_turn_replay_2.jsonl", "claude_turn_stream.jsonl"} {
		raw, err := os.ReadFile(fixture(name))
		if err != nil {
			t.Fatal(err)
		}
		var init struct {
			PermissionMode string   `json:"permissionMode"`
			Tools          []string `json:"tools"`
		}
		_ = json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &init)
		if init.PermissionMode != "dontAsk" || len(init.Tools) != 0 {
			t.Errorf("%s ran with permission mode %q and tools %v, want dontAsk and none", name, init.PermissionMode, init.Tools)
		}
	}
}
