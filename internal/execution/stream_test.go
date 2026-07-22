package execution

import "testing"

func TestParseStreamLineResult(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"result","result":{"usage":{"input_tokens":1500,"output_tokens":800,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}}`
	event, updated := acc.ParseStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if event.Type != "result" {
		t.Errorf("type = %q", event.Type)
	}
	if !updated {
		t.Error("expected token update")
	}
	if acc.InputTokens != 1500 {
		t.Errorf("input = %d", acc.InputTokens)
	}
	if acc.OutputTokens != 800 {
		t.Errorf("output = %d", acc.OutputTokens)
	}
	if acc.CacheCreated != 100 {
		t.Errorf("cache created = %d", acc.CacheCreated)
	}
	if acc.CacheRead != 50 {
		t.Errorf("cache read = %d", acc.CacheRead)
	}
	if acc.Total() != 2300 {
		t.Errorf("total = %d", acc.Total())
	}
}

func TestParseStreamLineMessage(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"message","message":{"usage":{"input_tokens":500,"output_tokens":200}}}`
	_, updated := acc.ParseStreamLine(line)

	if !updated {
		t.Error("expected token update")
	}
	if acc.InputTokens != 500 {
		t.Errorf("input = %d", acc.InputTokens)
	}
}

func TestParseStreamLineCumulative(t *testing.T) {
	acc := &TokenAccumulator{}

	// First update
	acc.ParseStreamLine(`{"type":"result","result":{"usage":{"input_tokens":100,"output_tokens":50}}}`)
	// Second update with higher values (cumulative)
	acc.ParseStreamLine(`{"type":"result","result":{"usage":{"input_tokens":200,"output_tokens":150}}}`)

	if acc.InputTokens != 200 {
		t.Errorf("should take max: input = %d", acc.InputTokens)
	}
	if acc.OutputTokens != 150 {
		t.Errorf("should take max: output = %d", acc.OutputTokens)
	}
}

func TestParseStreamLineIgnoresNonJSON(t *testing.T) {
	acc := &TokenAccumulator{}

	tests := []string{
		"",
		"not json",
		"  ",
		"# comment",
	}

	for _, line := range tests {
		event, updated := acc.ParseStreamLine(line)
		if event != nil || updated {
			t.Errorf("should ignore non-JSON line %q", line)
		}
	}
}

func TestParseStreamLineLegacyFormat(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"result","result":{"input_tokens":1000,"output_tokens":500}}`
	_, updated := acc.ParseStreamLine(line)

	if !updated {
		t.Error("expected token update")
	}
	if acc.InputTokens != 1000 {
		t.Errorf("input = %d", acc.InputTokens)
	}
}

// --- Codex stream parser tests ---

func TestParseCodexStreamLineAgentMessage(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"item.completed","item":{"type":"agent_message","text":"Implementation complete."}}`
	event, updated := acc.ParseCodexStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if event.Type != "message" {
		t.Errorf("type = %q, want message", event.Type)
	}
	if updated {
		t.Error("agent_message carries no usage payload — tokens must not update")
	}
}

func TestParseCodexStreamLineTurnCompletedUsage(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"turn.completed","usage":{"input_tokens":13246,"cached_input_tokens":7296,"output_tokens":150}}`
	event, updated := acc.ParseCodexStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if !updated {
		t.Fatal("turn.completed with usage must update tokens (#4027)")
	}
	// Codex input_tokens (13246) is cache-inclusive; the cached subset (7296) is
	// stored as CacheRead and subtracted out of InputTokens (13246-7296=5950) so
	// the two pools are disjoint.
	if acc.InputTokens != 5950 {
		t.Errorf("InputTokens = %d, want 5950", acc.InputTokens)
	}
	if acc.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", acc.OutputTokens)
	}
	if acc.CacheRead != 7296 {
		t.Errorf("CacheRead = %d, want 7296", acc.CacheRead)
	}
}

func TestParseCodexStreamLineTurnCompletedSumsAndClamps(t *testing.T) {
	acc := &TokenAccumulator{}

	// Two per-turn events sum; a malformed cached > input is clamped to input.
	acc.ParseCodexStreamLine(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20}}`)
	acc.ParseCodexStreamLine(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":250,"output_tokens":30}}`)

	// Turn 1: non-cached 90, cached 10, out 20. Turn 2: cached clamped to 100 →
	// non-cached 0, cached 100, out 30. Totals: input 90, cacheRead 110, out 50.
	if acc.InputTokens != 90 {
		t.Errorf("InputTokens = %d, want 90", acc.InputTokens)
	}
	if acc.CacheRead != 110 {
		t.Errorf("CacheRead = %d, want 110", acc.CacheRead)
	}
	if acc.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", acc.OutputTokens)
	}
}

func TestParseCodexStreamLineTurnCompletedNoUsage(t *testing.T) {
	acc := &TokenAccumulator{}

	// A bare turn.completed (early exit) carries no usage — tokens stay zero.
	line := `{"type":"turn.completed"}`
	_, updated := acc.ParseCodexStreamLine(line)

	if updated {
		t.Error("turn.completed without usage must not update tokens")
	}
	if acc.InputTokens != 0 || acc.OutputTokens != 0 || acc.CacheRead != 0 {
		t.Error("token counts must remain zero when no usage payload is present")
	}
}

func TestParseCodexStreamLineCommandExecution(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"item.completed","item":{"type":"command_execution","command":"npm test","status":"success"}}`
	event, updated := acc.ParseCodexStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if updated {
		t.Error("Codex should not update tokens")
	}
}

func TestParseCodexStreamLineIgnoresNonJSON(t *testing.T) {
	acc := &TokenAccumulator{}

	event, updated := acc.ParseCodexStreamLine("not json")
	if event != nil || updated {
		t.Error("should ignore non-JSON")
	}
}

// --- Gemini stream parser tests ---

func TestParseGeminiStreamLineResultWithStats(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"result","status":"success","stats":{"input_tokens":3000,"output_tokens":1500,"cached":200}}`
	event, updated := acc.ParseGeminiStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if event.Type != "result" {
		t.Errorf("type = %q", event.Type)
	}
	if !updated {
		t.Error("expected token update")
	}
	// input_tokens (3000) is cache-inclusive; the cached subset (200) is
	// subtracted so input/cacheRead are disjoint and TotalTokens does not
	// double-count the cached tokens (#4036).
	if acc.InputTokens != 2800 {
		t.Errorf("input = %d, want 2800 (3000 prompt - 200 cached)", acc.InputTokens)
	}
	if acc.OutputTokens != 1500 {
		t.Errorf("output = %d", acc.OutputTokens)
	}
	if acc.CacheRead != 200 {
		t.Errorf("cache read = %d", acc.CacheRead)
	}
}

func TestParseGeminiStreamLineResultWithAlternateInputKey(t *testing.T) {
	acc := &TokenAccumulator{}

	// Some Gemini versions use "input" instead of "input_tokens"
	line := `{"type":"result","status":"success","stats":{"input":2500,"output_tokens":1000}}`
	_, updated := acc.ParseGeminiStreamLine(line)

	if !updated {
		t.Error("expected token update")
	}
	if acc.InputTokens != 2500 {
		t.Errorf("input = %d, want 2500", acc.InputTokens)
	}
}

func TestParseGeminiStreamLineResultUsageFallback(t *testing.T) {
	acc := &TokenAccumulator{}

	// Fallback: token usage in result.usage field
	line := `{"type":"result","status":"success","result":{"usage":{"input_tokens":4000,"output_tokens":2000,"cached":100}}}`
	_, updated := acc.ParseGeminiStreamLine(line)

	if !updated {
		t.Error("expected token update")
	}
	// 4000 prompt is cache-inclusive; 100 cached subtracted → 3900 non-cached (#4036).
	if acc.InputTokens != 3900 {
		t.Errorf("input = %d, want 3900 (4000 - 100 cached)", acc.InputTokens)
	}
	if acc.OutputTokens != 2000 {
		t.Errorf("output = %d", acc.OutputTokens)
	}
	if acc.CacheRead != 100 {
		t.Errorf("cache read = %d, want 100", acc.CacheRead)
	}
}

func TestParseGeminiStreamLineClampsCachedToInput(t *testing.T) {
	acc := &TokenAccumulator{}

	// Malformed payload: cached subset exceeds the prompt total. Clamp cached to
	// the prompt so input never goes negative and the pools stay disjoint (#4036).
	line := `{"type":"result","status":"success","stats":{"input_tokens":500,"output_tokens":50,"cached":900}}`
	_, updated := acc.ParseGeminiStreamLine(line)

	if !updated {
		t.Error("expected token update")
	}
	if acc.InputTokens != 0 {
		t.Errorf("input = %d, want 0 (cached clamped to prompt total)", acc.InputTokens)
	}
	if acc.CacheRead != 500 {
		t.Errorf("cache read = %d, want 500 (clamped to prompt)", acc.CacheRead)
	}
}

func TestParseGeminiStreamLineMessage(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"message","role":"assistant","content":"I'll implement the feature now."}`
	event, updated := acc.ParseGeminiStreamLine(line)

	if event == nil {
		t.Fatal("expected event")
	}
	if event.Type != "message" {
		t.Errorf("type = %q", event.Type)
	}
	if event.Subtype != "text" {
		t.Errorf("subtype = %q, want text", event.Subtype)
	}
	if updated {
		t.Error("message events should not update tokens")
	}
}

func TestParseGeminiStreamLineIgnoresNonJSON(t *testing.T) {
	acc := &TokenAccumulator{}

	event, updated := acc.ParseGeminiStreamLine("not json")
	if event != nil || updated {
		t.Error("should ignore non-JSON")
	}
}

// --- ParseLine dispatch tests ---

func TestParseLineDispatch(t *testing.T) {
	acc := &TokenAccumulator{}

	// Claude format via ParseLine
	line := `{"type":"result","result":{"usage":{"input_tokens":100,"output_tokens":50}}}`
	_, updated := acc.ParseLine(StreamFormatClaude, line)
	if !updated {
		t.Error("Claude ParseLine should update tokens")
	}

	// Codex format via ParseLine
	acc2 := &TokenAccumulator{}
	codexLine := `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`
	_, updated = acc2.ParseLine(StreamFormatCodex, codexLine)
	if updated {
		t.Error("Codex ParseLine should not update tokens")
	}

	// Gemini format via ParseLine
	acc3 := &TokenAccumulator{}
	geminiLine := `{"type":"result","status":"success","stats":{"input_tokens":500,"output_tokens":200}}`
	_, updated = acc3.ParseLine(StreamFormatGemini, geminiLine)
	if !updated {
		t.Error("Gemini ParseLine should update tokens")
	}

	// Copilot format via ParseLine (plain-text footer)
	acc4 := &TokenAccumulator{}
	copilotLine := "Usage: Total usage est: 2 Premium requests"
	_, updated = acc4.ParseLine(StreamFormatCopilot, copilotLine)
	if !updated {
		t.Error("Copilot ParseLine should update premium requests")
	}
	if acc4.PremiumRequests != 2 {
		t.Errorf("PremiumRequests = %d, want 2", acc4.PremiumRequests)
	}
	if acc4.InputTokens != 0 || acc4.OutputTokens != 0 {
		t.Errorf("copilot must not fabricate token counts: in=%d out=%d", acc4.InputTokens, acc4.OutputTokens)
	}
}

// --- Copilot plain-text stats-footer parsing (#52) ---

func TestParseCopilotStreamLinePremiumRequests(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"Usage: Total usage est: 3 Premium requests", 3},
		{"Total usage est: 0 Premium requests", 0},
		{"1 premium request", 1},         // singular, bare
		{"Total usage est: 2.4 Premium requests", 2}, // fractional rounds
	}
	for _, tc := range cases {
		acc := &TokenAccumulator{}
		_, updated := acc.ParseCopilotStreamLine(tc.line)
		if !updated {
			t.Errorf("ParseCopilotStreamLine(%q) should report an update", tc.line)
		}
		if acc.PremiumRequests != tc.want {
			t.Errorf("ParseCopilotStreamLine(%q) PremiumRequests = %d, want %d", tc.line, acc.PremiumRequests, tc.want)
		}
	}
}

func TestParseCopilotStreamLineSessionID(t *testing.T) {
	acc := &TokenAccumulator{}
	event, updated := acc.ParseCopilotStreamLine("Session ID: 221b5571-3998-47e1-b57a-552cf9078947")
	if updated {
		t.Error("session id line should not update token/premium accounting")
	}
	if event == nil || event.SessionID != "221b5571-3998-47e1-b57a-552cf9078947" {
		t.Errorf("expected session id extracted, got %+v", event)
	}
}

func TestParseCopilotStreamLinePlainText(t *testing.T) {
	acc := &TokenAccumulator{}
	event, updated := acc.ParseCopilotStreamLine("Implemented the feature and ran the tests.")
	if updated {
		t.Error("plain agent text should not update premium accounting")
	}
	if event == nil || event.Type != "message" {
		t.Errorf("expected a text message event, got %+v", event)
	}
	if acc.PremiumRequests != 0 {
		t.Errorf("PremiumRequests should stay 0 for plain text, got %d", acc.PremiumRequests)
	}
}

func TestParseCopilotStreamLineEmpty(t *testing.T) {
	acc := &TokenAccumulator{}
	event, updated := acc.ParseCopilotStreamLine("   ")
	if updated || event != nil {
		t.Errorf("blank line should be ignored, got event=%+v updated=%v", event, updated)
	}
}

func TestStreamFormatForAdapter(t *testing.T) {
	tests := []struct {
		adapter  string
		expected AdapterStreamFormat
	}{
		{"claude", StreamFormatClaude},
		{"claude-sdk", StreamFormatClaude},
		{"claude-headless", StreamFormatClaude},
		{"codex", StreamFormatCodex},
		{"gemini", StreamFormatGemini},
		{"gemini-sdk", StreamFormatGemini},
		{"copilot", StreamFormatCopilot},
		{"unknown", StreamFormatClaude}, // default
	}

	for _, tt := range tests {
		got := StreamFormatForAdapter(tt.adapter)
		if got != tt.expected {
			t.Errorf("StreamFormatForAdapter(%q) = %q, want %q", tt.adapter, got, tt.expected)
		}
	}
}

// ── #91 served-model attribution ─────────────────────────────────────────

// TestServedModelTrackerRefusalFallback is the #91 regression test: a stream
// containing the CLI's model_refusal_fallback event must attribute the
// FALLBACK model as the served model, even though the session init still
// reported the requested model and the run exits 0.
// Event shape captured live in docs/spikes/fable-5-behavior-porting.md §8.3.
func TestServedModelTrackerRefusalFallback(t *testing.T) {
	acc := &TokenAccumulator{}
	tracker := &ServedModelTracker{}

	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-fable-5","session_id":"abc"}`,
		`{"type":"assistant","message":{"model":"claude-fable-5","usage":{"input_tokens":100,"output_tokens":10}}}`,
		`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","original_model":"claude-fable-5","fallback_model":"claude-opus-4-8","api_refusal_category":"reasoning_extraction","content":"…"}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":200,"output_tokens":40}}}`,
		`{"type":"result","result":{"usage":{"input_tokens":200,"output_tokens":40}}}`,
	}

	var fired int
	for _, line := range lines {
		event, _ := acc.ParseStreamLine(line)
		if fb := tracker.Observe(event); fb != nil {
			fired++
			if fb.OriginalModel != "claude-fable-5" {
				t.Errorf("fallback original = %q", fb.OriginalModel)
			}
			if fb.FallbackModel != "claude-opus-4-8" {
				t.Errorf("fallback model = %q", fb.FallbackModel)
			}
			if fb.RefusalCategory != "reasoning_extraction" {
				t.Errorf("refusal category = %q", fb.RefusalCategory)
			}
		}
	}

	if fired != 1 {
		t.Errorf("Observe returned a fallback %d times, want exactly 1", fired)
	}
	if tracker.ServedModel != "claude-opus-4-8" {
		t.Errorf("ServedModel = %q, want the fallback model claude-opus-4-8", tracker.ServedModel)
	}
	if tracker.Fallback == nil {
		t.Fatal("Fallback not recorded")
	}
}

// A fallback event with no subsequent assistant message still attributes the
// fallback model — the event itself is authoritative.
func TestServedModelTrackerFallbackEventIsAuthoritative(t *testing.T) {
	acc := &TokenAccumulator{}
	tracker := &ServedModelTracker{}
	acc0, _ := acc.ParseStreamLine(`{"type":"system","subtype":"init","model":"claude-fable-5"}`)
	tracker.Observe(acc0)
	ev, _ := acc.ParseStreamLine(`{"type":"system","subtype":"model_refusal_fallback","original_model":"claude-fable-5","fallback_model":"claude-opus-4-8"}`)
	if fb := tracker.Observe(ev); fb == nil {
		t.Fatal("expected fallback record")
	}
	if tracker.ServedModel != "claude-opus-4-8" {
		t.Errorf("ServedModel = %q", tracker.ServedModel)
	}
}

// Streams with no model information (non-claude adapters, usage-only lines)
// leave the tracker empty so callers fall back to the requested model.
func TestServedModelTrackerNoModelInfo(t *testing.T) {
	acc := &TokenAccumulator{}
	tracker := &ServedModelTracker{}
	for _, line := range []string{
		`{"type":"result","result":{"usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"message","message":{"usage":{"input_tokens":100,"output_tokens":50}}}`,
		`not json`,
		`{"type":"system","subtype":"init"}`,
	} {
		event, _ := acc.ParseStreamLine(line)
		if fb := tracker.Observe(event); fb != nil {
			t.Errorf("unexpected fallback for line %q", line)
		}
	}
	if tracker.ServedModel != "" {
		t.Errorf("ServedModel = %q, want empty", tracker.ServedModel)
	}
	if tracker.Fallback != nil {
		t.Error("unexpected fallback record")
	}
}

// Without a fallback event, init seeds the served model (the canonicalized
// requested model) and later assistant messages override it.
func TestServedModelTrackerInitSeedsAssistantOverrides(t *testing.T) {
	acc := &TokenAccumulator{}
	tracker := &ServedModelTracker{}
	ev1, _ := acc.ParseStreamLine(`{"type":"system","subtype":"init","model":"opus"}`)
	tracker.Observe(ev1)
	if tracker.ServedModel != "opus" {
		t.Errorf("after init: ServedModel = %q", tracker.ServedModel)
	}
	ev2, _ := acc.ParseStreamLine(`{"type":"assistant","message":{"model":"claude-opus-4-8"}}`)
	tracker.Observe(ev2)
	if tracker.ServedModel != "claude-opus-4-8" {
		t.Errorf("after assistant: ServedModel = %q", tracker.ServedModel)
	}
	if tracker.Fallback != nil {
		t.Error("no fallback should be recorded for plain model canonicalization")
	}
}

// Nil-safety: Observe tolerates nil trackers and nil events (parser returns
// nil for non-JSON lines).
func TestServedModelTrackerNilSafety(t *testing.T) {
	var nilTracker *ServedModelTracker
	if fb := nilTracker.Observe(&StreamEvent{Type: "system", Subtype: "model_refusal_fallback", FallbackModel: "x"}); fb != nil {
		t.Error("nil tracker must not record")
	}
	tracker := &ServedModelTracker{}
	if fb := tracker.Observe(nil); fb != nil {
		t.Error("nil event must not record")
	}
}
