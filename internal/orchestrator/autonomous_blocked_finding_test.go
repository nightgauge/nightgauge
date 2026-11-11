package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Tests for #2914: the autonomous candidate filter skips an issue with an
// unresolved blocked finding, logs that once per finding, and re-admits the
// issue on the next cycle once the finding is cleared.

func writeBlockedFindingFile(t *testing.T, root string, issue int, body string) string {
	t.Helper()
	path := BlockedFindingPath(root, issue)
	if path == "" {
		t.Fatalf("BlockedFindingPath returned empty for %s", root)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func blockedFindingGraph() *depgraph.Graph {
	return &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1255": {Repo: "O/app", Number: 1255, State: "OPEN", BoardStatus: "Ready"},
	}}
}

const validFinding1255 = `{"schema_version":"1.0","issue_number":1255,"stage":"feature-planning",` +
	`"signal_type":"OUT_OF_SCOPE_BLOCKER","reason":"needs upstream","rationale":"r",` +
	`"evidence":["blocked-on: x"],"run_id":"run-1","recorded_at":"2026-11-01T00:00:00Z"}`

func TestPrioritize_BlockedFinding_SkipsLogsOnceAndReadmitsOnClear(t *testing.T) {
	root := layouttest.Repo(t)
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}, workspaceRoot: root}
	g := blockedFindingGraph()

	if !isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
		t.Fatal("precondition: issue with no finding must be a candidate")
	}

	path := writeBlockedFindingFile(t, root, 1255, validFinding1255)
	buf := withCapturedLog(t)
	for i := 0; i < 2; i++ {
		if isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
			t.Fatalf("pass %d: issue with an unresolved blocked finding was a candidate", i+1)
		}
	}
	if n := strings.Count(buf.String(), "unresolved blocked finding"); n != 1 {
		t.Fatalf("skip logged %d times across two passes; want exactly 1\n%s", n, buf.String())
	}

	// blocked.clearFinding deletes the file; the next pass must re-admit.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
		t.Fatal("issue not re-admitted on the cycle after its finding was cleared")
	}

	// A finding recorded again later logs again.
	writeBlockedFindingFile(t, root, 1255, validFinding1255)
	as.prioritize(context.Background(), g)
	if n := strings.Count(buf.String(), "unresolved blocked finding"); n != 2 {
		t.Fatalf("re-recorded finding logged %d times total; want 2", n)
	}
}

func TestPrioritize_BlockedFinding_InvalidFilesDoNotBlock(t *testing.T) {
	cases := map[string]string{
		"malformed":      `{not json`,
		"wrong-schema":   strings.Replace(validFinding1255, `"1.0"`, `"2.0"`, 1),
		"mismatched":     strings.Replace(validFinding1255, `"issue_number":1255`, `"issue_number":7`, 1),
		"no-signal-type": strings.Replace(validFinding1255, `"OUT_OF_SCOPE_BLOCKER"`, `""`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := layouttest.Repo(t)
			writeBlockedFindingFile(t, root, 1255, body)
			as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}, workspaceRoot: root}
			if !isCandidate(as.prioritize(context.Background(), blockedFindingGraph()), "O/app", 1255) {
				t.Fatalf("%s finding file blocked dispatch; an unreadable file must not wedge an issue", name)
			}
		})
	}
}

// TestPrioritize_HeldBlockedIssue_EligibleAfterClear reproduces #3170: a run
// that stopped on an out-of-scope finding can also leave a Failed entry held
// for HoldOperatorResume. Deleting the finding alone left the issue skipped
// forever; releasing the hold (what blocked.clearFinding now also does in the
// daemon) re-admits it.
func TestPrioritize_HeldBlockedIssue_EligibleAfterClear(t *testing.T) {
	root := layouttest.Repo(t)
	as := &AutonomousScheduler{config: AutonomousConfig{}, workspaceRoot: root,
		state: &AutonomousState{Failed: []FailedItem{{
			Repo: "O/app", Number: 1255, Kind: TerminalKindNotPipelineActionable, AttemptCount: 1,
		}}}}
	g := blockedFindingGraph()

	path := writeBlockedFindingFile(t, root, 1255, validFinding1255)
	if isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
		t.Fatal("held issue with a finding was a candidate")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
		t.Fatal("removing only the finding re-admitted the issue; the operator hold must still skip it")
	}
	if n, _ := as.ClearIssueFailures("O/app#1255"); n != 1 {
		t.Fatalf("ClearIssueFailures released %d issues, want 1", n)
	}
	if !isCandidate(as.prioritize(context.Background(), g), "O/app", 1255) {
		t.Fatal("issue not re-admitted after its finding was cleared and its hold released")
	}
}
