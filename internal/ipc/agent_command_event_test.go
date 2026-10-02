package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The agent.command event the daemon relays a command on (#2335) is the line
// testdata/agent-command-event.json holds. The extension's
// AgentCommandDispatcher test feeds the same file to its relay subscription,
// so the event name and the `{agentId, frame}` field names are pinned on
// both sides of the IPC boundary; IPC codegen covers methods, not events.
func TestAgentCommandEvent_MatchesTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/agent-command-event.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Data struct {
			Frame json.RawMessage `json:"frame"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}

	s := NewServer(nil)
	var out bytes.Buffer
	s.writer = &out
	s.Emit(EventAgentCommand, AgentCommandEvent{AgentID: "agent-daemon", Frame: fixture.Data.Frame})

	var emitted, want any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &emitted); err != nil {
		t.Fatalf("emitted line is not JSON: %v (%s)", err, out.String())
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(emitted, want) {
		t.Errorf("emitted %s\nwant the fixture %s", out.String(), raw)
	}
}
