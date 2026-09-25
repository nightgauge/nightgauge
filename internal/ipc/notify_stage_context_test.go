package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// completeFeatureDev drives a stage through running → complete on a fresh
// server, with extra JSON fields on the "complete" notify, and returns the
// feature-dev stage of the V2 record the run builds (#1668).
func completeFeatureDev(t *testing.T, extra string) (map[string]any, *state.RuntimeState) {
	t.Helper()
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()
	runID := newTestRunID()
	for _, raw := range []string{
		`{"repo":"","issueNumber":1668,"stage":"feature-dev","status":"running","adapter":"opencode","runId":"` + runID + `"}`,
		`{"repo":"","issueNumber":1668,"stage":"feature-dev","status":"complete","adapter":"opencode","model":"lm-studio/qwen/qwen3.8-27b","inputTokens":29360,"outputTokens":900` + extra + `,"runId":"` + runID + `"}`,
	} {
		if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
			t.Fatalf("notify: %v", err)
		}
	}
	rt := s.activeRuntimes[runID].rs
	rec := state.NewHistoryWriter(t.TempDir()).BuildV2Record(rt.Snapshot(), true, "", state.V2RunInput{Title: "t"}, time.Unix(0, 0))
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Stages map[string]map[string]any `json:"stages"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Stages["feature-dev"], rt
}

var stageContextKeys = []string{"peak_step_input_tokens", "context_window_tokens", "context_window_utilization", "compaction_count"}

// TestNotifyStageTransitionRecordsStageContext pins that an editor-launched
// stage's "complete" notify lands the same V2 context fields #1653's
// scheduler-path fixture produces: peak 12010 of a 131072 window is 0.0916.
func TestNotifyStageTransitionRecordsStageContext(t *testing.T) {
	stage, _ := completeFeatureDev(t, `,"peakStepInputTokens":12010,"contextWindowTokens":131072,"compactionCount":3`)
	if got := stage["context_window_utilization"]; got != 0.0916 {
		t.Errorf("context_window_utilization = %v, want 0.0916", got)
	}
	if got := stage["peak_step_input_tokens"]; got != float64(12010) {
		t.Errorf("peak_step_input_tokens = %v, want 12010", got)
	}
	if got := stage["context_window_tokens"]; got != float64(131072) {
		t.Errorf("context_window_tokens = %v, want 131072", got)
	}
	if got := stage["compaction_count"]; got != float64(3) {
		t.Errorf("compaction_count = %v, want 3", got)
	}
}

// TestNotifyStageTransitionWithoutContextRecordsAsBefore pins that a notify
// without the fields, and one whose fields are dropped as invalid, record no
// context entry at all: the record is the one the handler wrote before #1668.
func TestNotifyStageTransitionWithoutContextRecordsAsBefore(t *testing.T) {
	for name, extra := range map[string]string{
		"absent":           ``,
		"negative peak":    `,"peakStepInputTokens":-1,"contextWindowTokens":131072`,
		"peak over 10x":    `,"peakStepInputTokens":2000000,"contextWindowTokens":131072,"compactionCount":1`,
		"no window":        `,"peakStepInputTokens":12010`,
		"negative compact": `,"peakStepInputTokens":12010,"contextWindowTokens":131072,"compactionCount":-2`,
	} {
		t.Run(name, func(t *testing.T) {
			stage, rt := completeFeatureDev(t, extra)
			if stage == nil {
				t.Fatal("feature-dev stage missing from the record")
			}
			for _, k := range stageContextKeys {
				if v, ok := stage[k]; ok {
					t.Errorf("%s = %v, want absent", k, v)
				}
			}
			if n := len(rt.Snapshot().StageContexts); n != 0 {
				t.Errorf("StageContexts has %d entries, want 0", n)
			}
		})
	}
}

// TestNotifyStageContextBaselineMatchesNoFieldRecord pins byte-identity: the
// stage detail from a notify carrying dropped fields equals the one from a
// notify with none, key for key, apart from its clock-derived values.
func TestNotifyStageContextBaselineMatchesNoFieldRecord(t *testing.T) {
	base, _ := completeFeatureDev(t, ``)
	dropped, _ := completeFeatureDev(t, `,"peakStepInputTokens":-1,"contextWindowTokens":131072`)
	for _, m := range []map[string]any{base, dropped} {
		for k := range m {
			if k == "started_at" || k == "completed_at" || k == "duration_ms" || k == "duration_seconds" {
				delete(m, k)
			}
		}
	}
	a, _ := json.Marshal(base)
	b, _ := json.Marshal(dropped)
	if string(a) != string(b) {
		t.Errorf("record differs:\n no fields: %s\n dropped:   %s", a, b)
	}
}

// writeStageBudgetConfig writes a project config under a temp workspace with
// no machine tier, and returns a server rooted there.
func stageBudgetServer(t *testing.T, yaml string) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newTransitionTestServer(t)
	s.setWorkspaceRoot(root)
	return s
}

func callResolveStageBudgets(t *testing.T, s *Server, stage, adapter, model string) *PipelineResolveStageBudgetsResult {
	t.Helper()
	raw, _ := json.Marshal(PipelineResolveStageBudgetsParams{Stage: stage, Adapter: adapter, Model: model})
	out, err := s.methods["pipeline.resolveStageBudgets"](context.Background(), raw)
	if err != nil {
		t.Fatalf("pipeline.resolveStageBudgets: %v", err)
	}
	return out.(*PipelineResolveStageBudgetsResult)
}

// TestResolveStageBudgetsZeroCostFloor pins that a zero-cost stage whose
// config leaves max_turns at 0 and asks for an unlimited token budget gets
// the non-zero defaults, exactly what #1652's resolver gives it.
func TestResolveStageBudgetsZeroCostFloor(t *testing.T) {
	s := stageBudgetServer(t, `
schema_version: "2"
owner: nightgauge
pipeline:
  stage_budgets:
    default:
      max_turns: 0
      max_tokens: -1
`)
	got := callResolveStageBudgets(t, s, "feature-dev", "opencode", "lm-studio/qwen/qwen3.8-27b")
	if !got.ZeroCost {
		t.Fatalf("ZeroCost = false for an lm-studio model, want true")
	}
	if got.MaxTurns != config.DefaultZeroCostStageMaxTurns {
		t.Errorf("MaxTurns = %d, want the zero-cost default %d", got.MaxTurns, config.DefaultZeroCostStageMaxTurns)
	}
	if got.MaxTokens != config.DefaultStageMaxTokens {
		t.Errorf("MaxTokens = %d, want the refused -1 to fall through to %d", got.MaxTokens, config.DefaultStageMaxTokens)
	}
	if got.MaxWallClockMs != config.DefaultStageMaxWallClock.Milliseconds() {
		t.Errorf("MaxWallClockMs = %d, want %d", got.MaxWallClockMs, config.DefaultStageMaxWallClock.Milliseconds())
	}
	if len(got.Warnings) == 0 {
		t.Error("a refused -1 must come back as a warning")
	}
}

// TestResolveStageBudgetsMatchesTheGoResolver pins the RPC to the resolver the
// executor uses, for a priced stage whose explicit -1 is honoured.
func TestResolveStageBudgetsMatchesTheGoResolver(t *testing.T) {
	s := stageBudgetServer(t, `
schema_version: "2"
owner: nightgauge
pipeline:
  stage_budgets:
    default:
      max_turns: 300
    feature-dev:
      max_wall_clock: 90m
      max_tokens: -1
`)
	got := callResolveStageBudgets(t, s, "feature-dev", "claude", "claude-sonnet-4-6")
	cfg, err := config.Load(s.workspaceRootPath())
	if err != nil {
		t.Fatal(err)
	}
	want := config.ResolveStageBudget(cfg.Pipeline.StageBudgets, "feature-dev", config.StagePriced)
	if got.ZeroCost {
		t.Error("ZeroCost = true for a priced claude model")
	}
	if got.MaxTurns != want.MaxTurns || got.MaxWallClockMs != want.MaxWallClock.Milliseconds() || got.MaxTokens != want.MaxTokens {
		t.Errorf("RPC = %d/%dms/%d, want the resolver's %d/%s/%d",
			got.MaxTurns, got.MaxWallClockMs, got.MaxTokens, want.MaxTurns, want.MaxWallClock, want.MaxTokens)
	}
	if got.MaxTokens != config.StageBudgetUnlimited {
		t.Errorf("MaxTokens = %s, want -1 for a priced stage", strconv.Itoa(got.MaxTokens))
	}
}

// TestResolveStageBudgetsRequiresStageAndAdapter pins the refusal the
// extension turns into "budgets unresolved".
func TestResolveStageBudgetsRequiresStageAndAdapter(t *testing.T) {
	s, _ := newTransitionTestServer(t)
	if _, err := s.methods["pipeline.resolveStageBudgets"](context.Background(), json.RawMessage(`{"stage":"feature-dev"}`)); err == nil {
		t.Error("a request without an adapter must be refused")
	}
}

// TestExtensionStageBudgetBreachClassifiesAsBudgetExceeded pins the text the
// extension's skillRunner sends on a breach's "failed" transition
// (stageBudgetErrorMessage in packages/nightgauge-vscode/src/utils/stageBudget.ts)
// to the budget_exceeded terminal kind, which is not retried (#1668).
func TestExtensionStageBudgetBreachClassifiesAsBudgetExceeded(t *testing.T) {
	for _, text := range []string{
		"stage_budget_exceeded:turns observed=5 ceiling=5: the stage reached its turn budget and was stopped (pipeline.stage_budgets, docs/GUARDRAILS_AND_BUDGETS.md)",
		"stage_budget_exceeded:wall_clock observed=1003 ceiling=1000: the stage reached its wall-clock budget and was stopped (pipeline.stage_budgets, docs/GUARDRAILS_AND_BUDGETS.md)",
		"stage_budget_exceeded:tokens observed=56000 ceiling=50000: the stage reached its token budget and was stopped (pipeline.stage_budgets, docs/GUARDRAILS_AND_BUDGETS.md)",
	} {
		if got := terminalkind.Classify(text); got != "budget_exceeded" {
			t.Errorf("Classify(%q) = %q, want budget_exceeded", text, got)
		}
	}
}
