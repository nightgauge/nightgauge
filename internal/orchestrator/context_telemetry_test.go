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
