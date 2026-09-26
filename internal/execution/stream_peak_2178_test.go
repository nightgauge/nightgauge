package execution

import "testing"

// TestClaudeStreamRecordsPeakStepInput2178 proves a claude-headless stage's
// peak single-turn prompt (input + cache read + cache write) is recorded,
// with a turn's repeated per-block snapshots folded by message id (#2178).
func TestClaudeStreamRecordsPeakStepInput2178(t *testing.T) {
	acc := &TokenAccumulator{}
	for _, line := range []string{
		`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":5000}}}`,
		`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":5000,"output_tokens":300}}}`,
		`{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":4,"cache_read_input_tokens":61000,"cache_creation_input_tokens":900}}}`,
		`{"type":"assistant","message":{"id":"m3","usage":{"input_tokens":2,"cache_read_input_tokens":1000}}}`,
		`{"type":"result","usage":{"input_tokens":16,"cache_read_input_tokens":82000,"cache_creation_input_tokens":5900}}`,
	} {
		acc.ParseStreamLine(line)
	}
	if want := 4 + 61000 + 900; acc.PeakStepInputTokens != want {
		t.Errorf("PeakStepInputTokens = %d, want %d (the largest single turn, not the summed pools)", acc.PeakStepInputTokens, want)
	}
}
