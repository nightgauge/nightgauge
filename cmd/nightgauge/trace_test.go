package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/trace"
)

func runTraceList(t *testing.T, args ...string) string {
	t.Helper()
	cmd := traceListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("trace list %v: %v", args, err)
	}
	return out.String()
}

func TestTraceListOldestFirstAndEmptyState(t *testing.T) {
	root := layouttest.Repo(t)
	first := "01890a5d-ac96-774b-bcce-b30209900031"
	second := "01890a5d-ac96-774b-bcce-b30209900032"
	trace.NewWriter(root, first, "o/r", 1283).Emit(trace.KindStageStart, "issue-pickup", nil)
	w := trace.NewWriter(root, second, "o/r", 1283)
	w.Emit(trace.KindStageStart, "issue-pickup", nil)
	w.Emit(trace.KindOutcome, "", trace.OutcomePayload{Success: true})

	var res traceListResult
	if err := json.Unmarshal([]byte(runTraceList(t, "1283", "--json", "--workdir", root)), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != trace.JoinJoined || len(res.Runs) != 2 || res.Runs[0].RunID != first || res.Runs[1].RunID != second {
		t.Fatalf("runs = %+v, want [%s %s] joined", res.Runs, first, second)
	}
	if res.Runs[0].OutcomeSuccess != nil || res.Runs[1].OutcomeSuccess == nil || !*res.Runs[1].OutcomeSuccess {
		t.Errorf("outcome_success = %v / %v, want null then true", res.Runs[0].OutcomeSuccess, res.Runs[1].OutcomeSuccess)
	}

	human := runTraceList(t, "7", "--workdir", root)
	if !strings.Contains(human, "no traced runs for issue #7") {
		t.Errorf("empty state = %q, want explicit no-runs line", human)
	}
	var empty traceListResult
	_ = json.Unmarshal([]byte(runTraceList(t, "7", "--json", "--workdir", root)), &empty)
	if empty.Status != trace.JoinUnavailable || empty.Reason == "" || empty.Runs == nil {
		t.Errorf("empty json = %+v, want unavailable with reason and runs []", empty)
	}
}
