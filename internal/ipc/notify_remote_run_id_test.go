package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A run that serves a platform trigger records the trigger's run id on its
// runtime, once (#2339): the paused snapshot a window finds after a reload
// then names the platform run it holds. A run no trigger started records none.
func TestNotifyStageTransition_RecordsThePlatformRunIdOnce(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()

	runID := newTestRunID()
	for _, params := range []string{
		`{"repo":"","issueNumber":2339,"stage":"init","status":"initialized","runId":"` + runID + `","remoteRunId":"platform-run-1"}`,
		`{"repo":"","issueNumber":2339,"stage":"issue-pickup","status":"running","runId":"` + runID + `","remoteRunId":"platform-run-other"}`,
		`{"repo":"","issueNumber":2339,"stage":"issue-pickup","status":"complete","runId":"` + runID + `"}`,
	} {
		if _, err := handler(ctx, json.RawMessage(params)); err != nil {
			t.Fatalf("notifyStageTransition(%s): %v", params, err)
		}
	}

	entry := s.activeRuntimes[runID]
	if entry == nil {
		t.Fatal("the transition should have adopted a runtime under its run identity")
	}
	snap := entry.rs.Snapshot()
	if snap.RemoteRunID != "platform-run-1" {
		t.Fatalf("RemoteRunID = %q, want the first platform run id, never rewritten", snap.RemoteRunID)
	}
	raw, _ := json.Marshal(snap)
	if !strings.Contains(string(raw), `"remoteRunId":"platform-run-1"`) {
		t.Fatalf("the snapshot JSON does not name the platform run: %s", raw)
	}

	local := newTestRunID()
	params := `{"repo":"","issueNumber":2340,"stage":"init","status":"initialized","runId":"` + local + `"}`
	if _, err := handler(ctx, json.RawMessage(params)); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(s.activeRuntimes[local].rs.Snapshot())
	if strings.Contains(string(raw), "remoteRunId") {
		t.Fatalf("a run no trigger started names a platform run: %s", raw)
	}
}
