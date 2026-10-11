package execution

import "testing"

// Partial-message events (--include-partial-messages, #1569): reply text is
// read from text_delta events only, and usage arrives before the assistant
// event, folded into the same per-message bucket so nothing counts twice.
// Shapes as recorded in internal/conversation/testdata.
func TestParseStreamLine_PartialMessages(t *testing.T) {
	acc := &TokenAccumulator{}
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-5-5","per_turn_effort_active":true}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":1489,"cache_read_input_tokens":535,"output_tokens":1}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Rivers "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"run."}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":2,"cache_creation_input_tokens":1489,"cache_read_input_tokens":535,"output_tokens":209}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":1489,"cache_read_input_tokens":535,"output_tokens":3}}}`,
		`{"type":"result","subtype":"success","result":"Rivers run.","usage":{"input_tokens":2,"cache_creation_input_tokens":1489,"cache_read_input_tokens":535,"output_tokens":209}}`,
	}
	var text string
	var effort *bool
	for i, line := range lines {
		ev, _ := acc.ParseStreamLine(line)
		if ev == nil {
			t.Fatalf("line %d did not parse", i)
		}
		if ev.PerTurnEffortActive != nil {
			effort = ev.PerTurnEffortActive
		}
		if block, delta, ok := ev.TextDelta(); ok {
			if block != 1 {
				t.Errorf("text delta in block %d", block)
			}
			text += delta
		}
		if i == 5 && (acc.OutputTokens != 209 || acc.InputTokens != 2) {
			t.Errorf("after message_delta: %d in / %d out, want 2 / 209 before any assistant event", acc.InputTokens, acc.OutputTokens)
		}
	}
	if text != "Rivers run." {
		t.Errorf("text = %q", text)
	}
	if effort == nil || !*effort {
		t.Error("per_turn_effort_active not read")
	}
	if acc.InputTokens != 2 || acc.OutputTokens != 209 || acc.CacheCreated != 1489 || acc.CacheRead != 535 {
		t.Errorf("totals %d/%d/%d/%d, want 2/209/1489/535", acc.InputTokens, acc.OutputTokens, acc.CacheCreated, acc.CacheRead)
	}
}
