package orchestrator

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestOpenCodeCompactionCountLogsARefusal: an events path that is a symlink
// to /etc/hosts counts 0, and the reader's refusal is logged rather than
// silently swallowed or failing the stage (#1653).
func TestOpenCodeCompactionCountLogsARefusal(t *testing.T) {
	if _, err := os.Stat("/etc/hosts"); err != nil {
		t.Skip("no /etc/hosts on this machine")
	}
	path := filepath.Join(t.TempDir(), opencodeplugin.EventsFileName("run-1653"))
	if err := os.Symlink("/etc/hosts", path); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	if n := openCodeCompactionCount(path); n != 0 {
		t.Errorf("openCodeCompactionCount = %d for a symlink to /etc/hosts, want 0", n)
	}
	if !strings.Contains(buf.String(), "refusing to read run events") {
		t.Errorf("no refusal logged; log was %q", buf.String())
	}
}

// TestStageResultCarriesThePeakStepPrompt: the executor's per-step peak
// reaches the scheduler's StageRunResult, and a feature-dev stage run as
// sub-sessions keeps its largest session's peak, never their sum (#1653).
func TestStageResultCarriesThePeakStepPrompt(t *testing.T) {
	if got := cliRunResultToStageResult(&adapters.RunResult{PeakStepInputTokens: 12010}).PeakStepInputTokens; got != 12010 {
		t.Errorf("StageRunResult.PeakStepInputTokens = %d, want 12010", got)
	}

	agg := &StageRunResult{}
	foldSubSession(agg, &StageRunResult{PeakStepInputTokens: 9000})
	foldSubSession(agg, &StageRunResult{PeakStepInputTokens: 12010})
	foldSubSession(agg, &StageRunResult{PeakStepInputTokens: 7550})
	if agg.PeakStepInputTokens != 12010 {
		t.Errorf("folded peak = %d, want 12010 (the largest session's; a sum would be 28560)", agg.PeakStepInputTokens)
	}
}

// TestStageContextProbeRecordsEachAttempt table-tests the scheduler's
// per-attempt context wiring (#1653): the compaction count is the events
// file's growth across this attempt's dispatch, the window passes through,
// each attempt replaces the stage's entry, and an attempt that ran no
// session (a deterministic, refused or rate-limited arm) clears it.
func TestStageContextProbeRecordsEachAttempt(t *testing.T) {
	const compaction = `{"v":1,"ts":"t","kind":"compaction","session_id":"s","child":false,"detail":{}}` + "\n"
	intp := func(n int) *int { return &n }
	earlier := state.StageContext{PeakStepInputTokens: 9000, ContextWindowTokens: 131072, Compactions: intp(3)}

	cases := []struct {
		name        string
		adapter     string
		relOutput   bool // outputFile not absolute: the events file is disabled
		prior       *state.StageContext
		preexisting int // compaction lines an earlier attempt left
		during      int // compaction lines this attempt appends
		session     bool
		peak        int
		window      int
		want        *state.StageContext
	}{
		{name: "opencode counts only this attempt's compactions", adapter: "opencode", preexisting: 1, during: 2, session: true,
			peak: 12010, window: 131072, want: &state.StageContext{PeakStepInputTokens: 12010, ContextWindowTokens: 131072, Compactions: intp(2)}},
		{name: "opencode with no events file counts 0", adapter: "opencode", session: true,
			peak: 12010, window: 131072, want: &state.StageContext{PeakStepInputTokens: 12010, ContextWindowTokens: 131072, Compactions: intp(0)}},
		{name: "opencode with a disabled events path records no count", adapter: "opencode", relOutput: true, session: true,
			peak: 12010, window: 131072, want: &state.StageContext{PeakStepInputTokens: 12010, ContextWindowTokens: 131072}},
		{name: "claude passes the window through with no peak and no count", adapter: "claude", session: true,
			window: 200000, want: &state.StageContext{ContextWindowTokens: 200000}},
		{name: "a session attempt replaces the earlier attempt's entry", adapter: "opencode", prior: &earlier, session: true,
			peak: 5000, window: 131072, want: &state.StageContext{PeakStepInputTokens: 5000, ContextWindowTokens: 131072, Compactions: intp(0)}},
		{name: "an attempt with no session clears the earlier attempt's entry", adapter: "opencode", prior: &earlier, preexisting: 3,
			session: false, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runID, err := runstate.NewRunID()
			if err != nil {
				t.Fatal(err)
			}
			rt := state.NewRuntimeState("nightgauge/nightgauge", 1653, "item", runID)
			if tc.prior != nil {
				rt.RecordStageContext(state.StageFeatureDev, tc.prior.PeakStepInputTokens, tc.prior.ContextWindowTokens, tc.prior.Compactions)
			}
			outputFile := filepath.Join(t.TempDir(), "dev.json")
			if tc.relOutput {
				outputFile = "dev.json"
			}
			eventsPath, _ := opencodeplugin.EventsPath(filepath.Join(filepath.Dir(outputFile), "dev.json"), runID)
			appendLines := func(n int) {
				if n == 0 {
					return
				}
				f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.WriteString(strings.Repeat(compaction, n)); err != nil {
					t.Fatal(err)
				}
			}
			appendLines(tc.preexisting)

			var probe stageContextProbe
			if tc.session {
				probe = beginStageContext(tc.adapter, outputFile, runID)
			}
			appendLines(tc.during)
			probe.record(rt, state.StageFeatureDev, &StageRunResult{PeakStepInputTokens: tc.peak}, tc.window)

			got, ok := rt.Snapshot().StageContexts[string(state.StageFeatureDev)]
			if tc.want == nil {
				if ok {
					t.Fatalf("stage context = %+v, want the entry cleared", got)
				}
				return
			}
			if !ok {
				t.Fatalf("no stage context recorded, want %+v", *tc.want)
			}
			if got.PeakStepInputTokens != tc.want.PeakStepInputTokens || got.ContextWindowTokens != tc.want.ContextWindowTokens {
				t.Errorf("peak/window = %d/%d, want %d/%d", got.PeakStepInputTokens, got.ContextWindowTokens,
					tc.want.PeakStepInputTokens, tc.want.ContextWindowTokens)
			}
			switch {
			case tc.want.Compactions == nil && got.Compactions != nil:
				t.Errorf("compactions = %d, want none recorded", *got.Compactions)
			case tc.want.Compactions != nil && got.Compactions == nil:
				t.Errorf("compactions not recorded, want %d", *tc.want.Compactions)
			case tc.want.Compactions != nil && *got.Compactions != *tc.want.Compactions:
				t.Errorf("compactions = %d, want %d", *got.Compactions, *tc.want.Compactions)
			}
		})
	}
}
