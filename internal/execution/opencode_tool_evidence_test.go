package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestRunStage_FailedOpenCodeStagePreservesToolEvidence is #3305 end to end:
// a failed stage that streamed the captured 2.2.7 tool run leaves one
// redacted record per tool call in its preserved evidence, tool-calls.jsonl,
// after its per-run root is gone.
func TestRunStage_FailedOpenCodeStagePreservesToolEvidence(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("adapters", "testdata", "opencode-cli", "stream-tool-state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := runCostStage(t, costStage{
		model:  "anthropic/claude-sonnet-5",
		budget: 5,
		run:    fmt.Sprintf("cat %q; exit 1", fixture),
	})
	if out.result.ExitCode == 0 {
		t.Fatalf("the stage exited 0, want a failure:\n%s", out.logged)
	}
	matches, err := filepath.Glob(filepath.Join(adapters.OpenCodeEvidenceDir(os.Getenv("NIGHTGAUGE_STATE_HOME")), "*", adapters.OpenCodeToolEvidenceFile))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preserved tool evidence = %v, %v; want one file\n%s", matches, err, out.logged)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		var rec adapters.OpenCodeToolEvidence
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		got = append(got, rec.Tool+"/"+rec.CallID+"/"+rec.Status)
	}
	want := []string{"read/call_1/completed", "shell/call_2/error", "read/call_3/error"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("preserved calls = %q, want %q", got, want)
	}
	if bytes.Contains(body, []byte("hello.txt")) || bytes.Contains(body, []byte("non-interactive")) {
		t.Errorf("preserved tool evidence holds transcript text:\n%s", body)
	}
}
