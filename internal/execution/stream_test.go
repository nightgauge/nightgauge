package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The result event's shape is the one from testdata/claude_stream_real_capture.jsonl:
// `usage` at the top level, and `result` holding the assistant's final TEXT.
// Typing `result` as a struct failed the whole line's unmarshal and dropped the
// terminal event, so every claude run on the Go path booked zero tokens (#300).
func TestParseStreamLineResult(t *testing.T) {
	acc := &TokenAccumulator{}

	line := `{"type":"result","subtype":"success","result":"Done.","usage":{"input_tokens":1500,"output_tokens":800,"cache_creation_input_tokens":100,"cache_read_input_tokens":50},"total_cost_usd":0.0107762}`
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
	cache5m, cache1h := acc.CacheCreationByTTL()
	if cache5m != 100 || cache1h != 0 {
		t.Errorf("cache creation split = (%d, %d), want unsplit fallback (100, 0)", cache5m, cache1h)
	}
	if acc.CacheRead != 50 {
		t.Errorf("cache read = %d", acc.CacheRead)
	}
	if acc.Total() != 2300 {
		t.Errorf("total = %d", acc.Total())
	}
	if string(event.Result) != `"Done."` {
		t.Errorf("result text = %s, want the raw final-text string", event.Result)
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

// A run can emit several result envelopes, and their `usage` payloads are
// per-envelope deltas — so envelopes sum. Maxing them would book only the
// largest. (Only `total_cost_usd` is session-cumulative; that asymmetry is what
// #256 was booked against.) Delta semantics are proven by the multi-envelope
// fixture, where the second envelope is smaller in every field.
func TestParseStreamLineResultEnvelopesSum(t *testing.T) {
	acc := &TokenAccumulator{}

	acc.ParseStreamLine(`{"type":"result","result":"a","usage":{"input_tokens":200,"output_tokens":150}}`)
	acc.ParseStreamLine(`{"type":"result","result":"b","usage":{"input_tokens":100,"output_tokens":50}}`)

	if acc.InputTokens != 300 {
		t.Errorf("input = %d, want 300 (200+100 — envelopes are deltas)", acc.InputTokens)
	}
	if acc.OutputTokens != 200 {
		t.Errorf("output = %d, want 200 (150+50)", acc.OutputTokens)
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

// ── #300 per-turn usage, against a REAL captured transcript ──────────────
//
// The fixture is a live `claude --output-format stream-json` capture, not a
// hand-authored one — see testdata/README.md. #300 is a #166 silent-no-op:
// the parser was green against a shape the CLI does not emit, so the Go
// auto/CLI path booked zero tokens and a killed stage looked free.

// Ground truth read off testdata/claude_stream_real_capture.jsonl.
const (
	captureTurn1ID = "msg_011CdkixpD3Jjqq1eV8pN4zj"
	captureTurn2ID = "msg_011Cdkiy8tCJsBAWYxS2STVS"

	// Deduped per-turn sums: 10+8, 3+1, 3048+260, 13287+16335.
	captureTurnSumInput  = 18
	captureTurnSumOutput = 4
	captureTurnSumCacheC = 3308
	captureTurnSumCacheR = 29622

	// The terminal result event's invocation totals.
	captureResultInput  = 18
	captureResultOutput = 236
	captureResultCacheC = 3308
	captureResultCacheR = 29622
)

// realCaptureLines returns the fixture's lines, and the index of its terminal
// result event.
func realCaptureLines(t *testing.T) (lines []string, resultIdx int) {
	t.Helper()
	data, err := os.ReadFile("testdata/claude_stream_real_capture.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	resultIdx = -1
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"type":"result"`) {
			resultIdx = len(lines)
		}
		lines = append(lines, line)
	}
	if resultIdx < 0 {
		t.Fatal("fixture has no result event — recapture it")
	}
	return lines, resultIdx
}

// The #300 regression: a stage killed before the terminal result event must
// still book the tokens its assistant turns already reported. Booking zero is
// worse than booking nothing — zero is indistinguishable from a free stage, so
// the budget enforcer never accumulates against repeatedly-killed runs.
func TestParseStreamRealCaptureKilledBeforeResult(t *testing.T) {
	lines, resultIdx := realCaptureLines(t)

	// Exactly what a killed stage leaves on stdout: every line up to, but not
	// including, the result event.
	acc := &TokenAccumulator{}
	for _, line := range lines[:resultIdx] {
		acc.ParseStreamLine(line)
	}

	if acc.Total() == 0 {
		t.Fatal("killed stage booked zero tokens — a free stage and a lost one are indistinguishable (#300)")
	}
	if acc.InputTokens != captureTurnSumInput {
		t.Errorf("input = %d, want %d (sum of the two turns)", acc.InputTokens, captureTurnSumInput)
	}
	if acc.OutputTokens != captureTurnSumOutput {
		t.Errorf("output = %d, want %d", acc.OutputTokens, captureTurnSumOutput)
	}
	if acc.CacheCreated != captureTurnSumCacheC {
		t.Errorf("cache created = %d, want %d", acc.CacheCreated, captureTurnSumCacheC)
	}
	if acc.CacheRead != captureTurnSumCacheR {
		t.Errorf("cache read = %d, want %d", acc.CacheRead, captureTurnSumCacheR)
	}
}

// The whole capture: the terminal result event must parse (its `result` field
// is a STRING, which is what broke the unmarshal) and its invocation totals
// must win over the partial per-turn sum.
func TestParseStreamRealCaptureComplete(t *testing.T) {
	lines, resultIdx := realCaptureLines(t)

	acc := &TokenAccumulator{}
	tracker := &ServedModelTracker{}
	resultParsed := false
	for i, line := range lines {
		event, updated := acc.ParseStreamLine(line)
		tracker.Observe(event)
		if i == resultIdx {
			if event == nil {
				t.Fatal("result event did not parse — `result` is the final text string, not an object (#300)")
			}
			if !updated {
				t.Error("result event carried usage but booked nothing")
			}
			resultParsed = true
		}
	}
	if !resultParsed {
		t.Fatal("never reached the result event")
	}

	if acc.InputTokens != captureResultInput {
		t.Errorf("input = %d, want %d", acc.InputTokens, captureResultInput)
	}
	if acc.OutputTokens != captureResultOutput {
		t.Errorf("output = %d, want %d (the result total, not the streamed partial)", acc.OutputTokens, captureResultOutput)
	}
	if acc.CacheCreated != captureResultCacheC {
		t.Errorf("cache created = %d, want %d", acc.CacheCreated, captureResultCacheC)
	}
	cache5m, cache1h := acc.CacheCreationByTTL()
	if cache5m != 0 || cache1h != captureResultCacheC {
		t.Errorf("cache creation split = (%d, %d), want (0, %d) from real capture", cache5m, cache1h, captureResultCacheC)
	}
	if acc.CacheRead != captureResultCacheR {
		t.Errorf("cache read = %d, want %d", acc.CacheRead, captureResultCacheR)
	}
	if tracker.ServedModel != "claude-haiku-4-5-20251001" {
		t.Errorf("served model = %q", tracker.ServedModel)
	}
}

// Regression for #390: the real capture's nonzero cache writes must survive
// the durable Go history boundary. Before the fix, CompleteStage priced these
// counts but StageResult discarded them, so both per-stage and run totals were
// permanently zero.
func TestRealCaptureCacheCreationReachesHistory(t *testing.T) {
	lines, _ := realCaptureLines(t)
	acc := &TokenAccumulator{}
	for _, line := range lines {
		acc.ParseStreamLine(line)
	}
	result := runResultFromAccumulator("", "", acc, &ServedModelTracker{})
	if result.CacheReadTokens != captureResultCacheR || result.CacheCreationTokens != captureResultCacheC {
		t.Fatalf("native RunResult cache counts = (read %d, create %d), want (%d, %d)",
			result.CacheReadTokens, result.CacheCreationTokens, captureResultCacheR, captureResultCacheC)
	}
	if result.CacheCreation5mTokens != 0 || result.CacheCreation1hTokens != captureResultCacheC {
		t.Fatalf("native RunResult cache creation split = (%d, %d), want (0, %d)",
			result.CacheCreation5mTokens, result.CacheCreation1hTokens, captureResultCacheC)
	}

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 390, "item", "run")
	runtime.BeginStage(state.StageFeatureDev)
	recordRunResultTokenCounts(runtime, string(state.StageFeatureDev), result)
	// Mirrors ExecutionManagerRunner's unchanged projection: only input/output
	// reach this call directly; the manager's one-shot handoff supplies cache.
	runtime.CompleteStageWithCost(0, result.InputTokens, result.OutputTokens, 0, 0.01)

	record := state.NewHistoryWriter(t.TempDir()).BuildV2Record(
		runtime.Snapshot(), true, "", state.V2RunInput{}, time.Now(),
	)
	stage := record.Tokens.PerStage[string(state.StageFeatureDev)]
	if stage.CacheCreation != captureResultCacheC {
		t.Errorf("per-stage cache creation = %d, want %d from real capture", stage.CacheCreation, captureResultCacheC)
	}
	if record.Tokens.TotalCacheCreation != captureResultCacheC {
		t.Errorf("total cache creation = %d, want %d from real capture", record.Tokens.TotalCacheCreation, captureResultCacheC)
	}
}

// Why per-turn snapshots are deduped by message id before being summed: the
// CLI emits one assistant event per content block (thinking, tool_use, text),
// each repeating that turn's usage. The fixture's first turn spans three such
// events. Summing them blind would triple-count it.
func TestParseStreamRealCaptureDedupesTurnBlocks(t *testing.T) {
	lines, _ := realCaptureLines(t)

	acc := &TokenAccumulator{}
	turn1Events := 0
	for _, line := range lines {
		if !strings.Contains(line, `"type":"assistant"`) {
			continue
		}
		if strings.Contains(line, captureTurn1ID) {
			turn1Events++
		}
		acc.ParseStreamLine(line)
	}

	if turn1Events < 2 {
		t.Fatalf("fixture no longer exercises repeated blocks per turn (%d events for turn 1)", turn1Events)
	}
	if got := acc.turns[captureTurn1ID].InputTokens; got != 10 {
		t.Errorf("turn 1 input = %d, want 10 counted once across %d events", got, turn1Events)
	}
	if got := acc.turns[captureTurn2ID].InputTokens; got != 8 {
		t.Errorf("turn 2 input = %d, want 8", got)
	}
	if acc.InputTokens != captureTurnSumInput {
		t.Errorf("input = %d, want %d — turns summed, blocks deduped", acc.InputTokens, captureTurnSumInput)
	}
}

// ── Multi-envelope + subagent capture ────────────────────────────────────
//
// A second real capture, of a stage that spawns a Task subagent. It exposes
// two shapes the single-turn capture cannot: several result envelopes in one
// stream, and assistant turns belonging to a subagent.

// Ground truth read off testdata/claude_stream_subagent_multi_result.jsonl.
const (
	// Two result envelopes. The second is smaller in EVERY field, which is
	// what proves envelope usage is a delta and not a running total.
	subEnvelope1Input, subEnvelope1Output = 28, 755
	subEnvelope2Input, subEnvelope2Output = 10, 141
	subEnvelopeSumInput                   = subEnvelope1Input + subEnvelope2Input   // 38
	subEnvelopeSumOutput                  = subEnvelope1Output + subEnvelope2Output // 896
	subEnvelopeSumCacheC                  = 5460
	subEnvelopeSumCacheR                  = 65467

	// Six distinct assistant turns, two of them the subagent's. The envelopes
	// account for the four main-thread turns only.
	subTurnCount     = 6
	subTurnSumInput  = 56 // 38 main + 18 subagent
	subTurnSumCacheC = 13350
	subTurnSumCacheR = 72701
)

func TestParseStreamSubagentCaptureEnvelopesAreDeltas(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_stream_subagent_multi_result.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	acc := &TokenAccumulator{}
	envelopes := 0
	for _, line := range lines {
		event, _ := acc.ParseStreamLine(line)
		if event != nil && event.Type == "result" {
			envelopes++
		}
	}

	if envelopes != 2 {
		t.Fatalf("fixture no longer carries multiple result envelopes (%d) — the shape this test exists for", envelopes)
	}
	if acc.resultTotal.InputTokens != subEnvelopeSumInput {
		t.Errorf("envelope input sum = %d, want %d", acc.resultTotal.InputTokens, subEnvelopeSumInput)
	}
	if acc.resultTotal.OutputTokens != subEnvelopeSumOutput {
		t.Errorf("envelope output sum = %d, want %d — maxing would book only %d", acc.resultTotal.OutputTokens, subEnvelopeSumOutput, subEnvelope1Output)
	}
	if acc.resultTotal.CacheCreationInput != subEnvelopeSumCacheC {
		t.Errorf("envelope cache-create sum = %d, want %d", acc.resultTotal.CacheCreationInput, subEnvelopeSumCacheC)
	}
	if acc.resultTotal.CacheReadInput != subEnvelopeSumCacheR {
		t.Errorf("envelope cache-read sum = %d, want %d", acc.resultTotal.CacheReadInput, subEnvelopeSumCacheR)
	}

	// Neither source is complete alone: the envelopes omit the subagent's
	// turns, and the turn snapshots report output as a partial. Per field, the
	// better-informed source wins.
	if len(acc.turns) != subTurnCount {
		t.Errorf("distinct turns = %d, want %d", len(acc.turns), subTurnCount)
	}
	if acc.InputTokens != subTurnSumInput {
		t.Errorf("input = %d, want %d — turn sum, which unlike the envelopes counts subagent turns", acc.InputTokens, subTurnSumInput)
	}
	if acc.OutputTokens != subEnvelopeSumOutput {
		t.Errorf("output = %d, want %d — envelope sum, the only accurate output source", acc.OutputTokens, subEnvelopeSumOutput)
	}
	if acc.CacheCreated != subTurnSumCacheC {
		t.Errorf("cache created = %d, want %d", acc.CacheCreated, subTurnSumCacheC)
	}
	if acc.CacheRead != subTurnSumCacheR {
		t.Errorf("cache read = %d, want %d", acc.CacheRead, subTurnSumCacheR)
	}
}

// Killed mid-subagent: the stage still books the subagent's tokens, which no
// result envelope would ever have reported.
func TestParseStreamSubagentCaptureKilledStillCountsSubagentTurns(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_stream_subagent_multi_result.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	acc := &TokenAccumulator{}
	subagentTurns := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"type":"result"`) {
			continue // no envelope ever arrives
		}
		if strings.Contains(line, `"type":"assistant"`) && !strings.Contains(line, `"parent_tool_use_id":null`) {
			subagentTurns++
		}
		acc.ParseStreamLine(line)
	}

	if subagentTurns == 0 {
		t.Fatal("fixture no longer contains subagent assistant turns")
	}
	if acc.Total() == 0 {
		t.Fatal("killed stage booked zero tokens (#300)")
	}
	if acc.InputTokens != subTurnSumInput {
		t.Errorf("input = %d, want %d (main + subagent turns)", acc.InputTokens, subTurnSumInput)
	}
	if acc.CacheRead != subTurnSumCacheR {
		t.Errorf("cache read = %d, want %d", acc.CacheRead, subTurnSumCacheR)
	}
}

// Assistant events with no message id cannot be deduped, so they collapse into
// one bucket by max. That under-counts a multi-turn stage; it never fabricates
// cost, which is the safe direction for a budget input.
func TestParseStreamAssistantWithoutMessageID(t *testing.T) {
	acc := &TokenAccumulator{}
	for i := 0; i < 3; i++ {
		acc.ParseStreamLine(`{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":40,"output_tokens":10}}}`)
	}
	if acc.InputTokens != 40 {
		t.Errorf("input = %d, want 40 — id-less snapshots must not be summed", acc.InputTokens)
	}
	if acc.OutputTokens != 10 {
		t.Errorf("output = %d, want 10", acc.OutputTokens)
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
	line := `{"type":"result","result":"done","usage":{"input_tokens":100,"output_tokens":50}}`
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
		{"1 premium request", 1},                     // singular, bare
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

func TestParseGrokStreamLineUsage(t *testing.T) {
	acc := &TokenAccumulator{}
	line := `{"type":"end","sessionId":"s1","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"reasoning_tokens":3}}`
	ev, updated := acc.ParseGrokStreamLine(line)
	if !updated {
		t.Fatal("expected token update")
	}
	if ev == nil || ev.SessionID != "s1" {
		t.Fatalf("event = %+v", ev)
	}
	if acc.InputTokens != 10 || acc.OutputTokens != 7 || acc.CacheRead != 2 || acc.CacheCreated != 1 {
		t.Fatalf("tokens = in=%d out=%d read=%d write=%d", acc.InputTokens, acc.OutputTokens, acc.CacheRead, acc.CacheCreated)
	}
}

// Ground truth read off testdata/grok_stream_real_capture.jsonl — the terminal
// `end` event's session totals.
const (
	grokCaptureEndInput     = 7133
	grokCaptureEndOutput    = 89
	grokCaptureEndReasoning = 49
	grokCaptureEndCacheR    = 23168
	grokCaptureEndCacheC    = 0
)

// TestParseGrokStreamRealCapture runs the hand-authored TestParseGrokStreamLineUsage
// expectations against a REAL `grok --output-format streaming-json` transcript
// (#533). It asserts nothing new about the parser — it asserts that the shape the
// parser was written for is the shape the CLI actually emits, which is the only
// thing a synthetic fixture can never establish (#166/#300).
func TestParseGrokStreamRealCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/grok_stream_real_capture.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	acc := &TokenAccumulator{}
	var endEvent *StreamEvent
	sawToolCall, sawToolCallUpdate := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		ev, _ := acc.ParseLine(StreamFormatGrok, line)
		if ev == nil {
			continue
		}
		switch ev.Type {
		case "end":
			endEvent = ev
		case "tool_call":
			sawToolCall = true
		case "tool_call_update":
			sawToolCallUpdate = true
		}
	}

	if endEvent == nil {
		t.Fatal("fixture has no `end` event — recapture it")
	}
	if endEvent.Usage == nil {
		t.Fatal("`end` event carried no usage — the real terminal event is where grok reports session totals")
	}
	if endEvent.SessionID == "" {
		t.Error("`end` event carried no sessionId — grok spells it camelCase, not session_id")
	}

	// The accumulator lands on the terminal event's totals: grok's `usage`
	// events are per-turn snapshots and `end` is the session total, so the
	// parser assigns rather than sums.
	wantOutput := grokCaptureEndOutput + grokCaptureEndReasoning
	if acc.InputTokens != grokCaptureEndInput {
		t.Errorf("input = %d, want %d (the `end` event's session total)", acc.InputTokens, grokCaptureEndInput)
	}
	if acc.OutputTokens != wantOutput {
		t.Errorf("output = %d, want %d (output %d + reasoning %d — reasoning_tokens is a sibling of output_tokens, not a component)",
			acc.OutputTokens, wantOutput, grokCaptureEndOutput, grokCaptureEndReasoning)
	}
	if acc.CacheRead != grokCaptureEndCacheR {
		t.Errorf("cache read = %d, want %d", acc.CacheRead, grokCaptureEndCacheR)
	}
	if acc.CacheCreated != grokCaptureEndCacheC {
		t.Errorf("cache created = %d, want %d", acc.CacheCreated, grokCaptureEndCacheC)
	}

	// #533 scope note: live stdout emits `tool_call` / `tool_call_update`. It
	// does NOT emit tool_started/tool_completed/phase_changed — those are the
	// SESSION-FILE schema under ~/.grok/sessions/<enc-cwd>/<id>/events.jsonl.
	// The capture is the evidence that pins which stream is which.
	if !sawToolCall || !sawToolCallUpdate {
		t.Errorf("capture is missing live tool events (tool_call=%v tool_call_update=%v) — recapture with a tool-using prompt",
			sawToolCall, sawToolCallUpdate)
	}
	for _, absent := range []string{"tool_started", "tool_completed", "phase_changed"} {
		if strings.Contains(string(data), `"type":"`+absent+`"`) {
			t.Errorf("capture contains %q on stdout — the session-file schema is documented as distinct from stdout; re-check #533's scope split", absent)
		}
	}
}

// Ground truth read off testdata/codex_stream_real_capture.jsonl — hand-computed
// from its single `turn.completed` event's `usage` payload (see the README's
// "codex_stream_real_capture.jsonl" section for the full table). The capture
// has exactly one turn, so per-turn and total are the same numbers.
const (
	codexCaptureTurn1Input  = 67553
	codexCaptureTurn1Cached = 56704
	codexCaptureTurn1Output = 124

	codexCaptureTotalInput     = codexCaptureTurn1Input - codexCaptureTurn1Cached
	codexCaptureTotalOutput    = codexCaptureTurn1Output
	codexCaptureTotalCacheRead = codexCaptureTurn1Cached
)

// TestParseCodexRealCapture runs a REAL `codex exec --json` transcript (#1620)
// through ParseLine and checks the accumulator lands on the README's
// hand-computed ground truth. It asserts nothing new about the parser — it
// asserts that the shape ParseCodexStreamLine was written for is the shape the
// CLI actually emits, the same purpose TestParseGrokStreamRealCapture serves
// for grok (#166/#300 class: a parser tested only against hand-written lines
// stays green while the runtime emits something else entirely).
func TestParseCodexRealCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/codex_stream_real_capture.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	acc := &TokenAccumulator{}
	sawCommandExecution := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		ev, _ := acc.ParseLine(StreamFormatCodex, line)
		if ev == nil {
			continue
		}
		if strings.Contains(line, `"type":"command_execution"`) {
			sawCommandExecution = true
		}
	}

	if acc.InputTokens != codexCaptureTotalInput {
		t.Errorf("input = %d, want %d (input_tokens %d minus cached_input_tokens %d)",
			acc.InputTokens, codexCaptureTotalInput, codexCaptureTurn1Input, codexCaptureTurn1Cached)
	}
	if acc.OutputTokens != codexCaptureTotalOutput {
		t.Errorf("output = %d, want %d", acc.OutputTokens, codexCaptureTotalOutput)
	}
	if acc.CacheRead != codexCaptureTotalCacheRead {
		t.Errorf("cache read = %d, want %d (cached_input_tokens)", acc.CacheRead, codexCaptureTotalCacheRead)
	}
	if acc.CacheCreated != 0 {
		t.Errorf("cache created = %d, want 0 — codex has no cache-write usage field", acc.CacheCreated)
	}

	// The capture's prompt asked for two sequential shell commands specifically
	// so the fixture pins codex's real item type for a tool call
	// (`command_execution`) rather than a hand-guessed one.
	if !sawCommandExecution {
		t.Error("capture is missing a command_execution item — recapture with a tool-using prompt")
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
		{"grok", StreamFormatGrok},
		{"opencode", StreamFormatOpenCode},
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
// Event shape captured live in docs/FAILURE_TAXONOMY.md § Model Refusal Fallback.
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

// ── #1624 OpenCode `run --format json` ────────────────────────────────────

// openCodeFixtureLines reads a real opencode 1.18.30 capture from testdata
// (testdata/README.md § OpenCode), one event per element.
func openCodeFixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// parseOpenCode feeds lines through the dispatch the manager uses for the
// opencode adapter.
func parseOpenCode(lines []string) *TokenAccumulator {
	acc := &TokenAccumulator{}
	format := StreamFormatForAdapter("opencode")
	for _, line := range lines {
		acc.ParseLine(format, line)
	}
	return acc
}

// withOpenCodeTokens returns a step_finish line with its part.tokens
// replaced, keeping every other field of the captured event.
func withOpenCodeTokens(t *testing.T, line string, input, output, reasoning, read, write int) string {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	part, ok := ev["part"].(map[string]any)
	if !ok || ev["type"] != "step_finish" {
		t.Fatalf("not a step_finish event: %s", line)
	}
	part["tokens"] = map[string]any{
		"total": input + output + reasoning + read + write, "input": input, "output": output,
		"reasoning": reasoning, "cache": map[string]any{"read": read, "write": write},
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestParseOpenCodeStream: opencode has no final usage event, so a run's
// usage is the sum of its step_finish events. The research sample is a real
// two-step capture (1539 then 1550 input): a parser that kept only the last
// step would book 1550. Reasoning folds into output as it does for grok, and
// the peak is the largest single step's prompt, which #1653 compares with the
// context window.
func TestParseOpenCodeStream(t *testing.T) {
	lines := openCodeFixtureLines(t, "opencode_stream_research_sample.jsonl")
	acc := parseOpenCode(lines)
	s := acc.OpenCode()
	if s.StepFinishes != 2 {
		t.Fatalf("step_finish events = %d, want the capture's 2", s.StepFinishes)
	}
	if acc.InputTokens != 1539+1550 || acc.OutputTokens != 8+6 {
		t.Errorf("input/output = %d/%d, want the summed steps %d/%d (the last step alone is 1550/6)",
			acc.InputTokens, acc.OutputTokens, 1539+1550, 8+6)
	}
	if acc.PeakStepInputTokens != 1550 {
		t.Errorf("peak step input = %d, want 1550", acc.PeakStepInputTokens)
	}
	if acc.CacheRead != 0 || acc.CacheCreated != 0 {
		t.Errorf("cache read/write = %d/%d, want the capture's 0/0", acc.CacheRead, acc.CacheCreated)
	}
	if s.SessionID != "ses_fixture0000000000000000001" {
		t.Errorf("session = %q, want the capture's (redacted) session", s.SessionID)
	}
	s.Finish(0)
	if got := s.DriftMarkers(); len(got) != 0 {
		t.Errorf("the real capture produced drift markers: %q", got)
	}

	// The same two captured events, carrying the issue's research numbers:
	// 7550 and 7750 input, 101 output and 54 reasoning between them.
	var steps []int
	for i, line := range lines {
		if strings.Contains(line, `"type":"step_finish"`) {
			steps = append(steps, i)
		}
	}
	research := append([]string{}, lines...)
	research[steps[0]] = withOpenCodeTokens(t, lines[steps[0]], 7550, 40, 30, 0, 0)
	research[steps[1]] = withOpenCodeTokens(t, lines[steps[1]], 7750, 61, 24, 0, 0)
	acc = parseOpenCode(research)
	if acc.InputTokens != 15300 || acc.OutputTokens != 155 || acc.PeakStepInputTokens != 7750 {
		t.Errorf("input/output/peak = %d/%d/%d, want 15300/155/7750",
			acc.InputTokens, acc.OutputTokens, acc.PeakStepInputTokens)
	}

	// Cache pools: OpenCode reports input with both cache pools already
	// subtracted, so each lands in its own pool, and a step's prompt, the
	// peak, counts all three.
	cached := append([]string{}, lines...)
	cached[steps[0]] = withOpenCodeTokens(t, lines[steps[0]], 100, 10, 0, 6000, 400)
	cached[steps[1]] = withOpenCodeTokens(t, lines[steps[1]], 200, 20, 0, 6400, 0)
	acc = parseOpenCode(cached)
	if acc.InputTokens != 300 || acc.CacheRead != 12400 || acc.CacheCreated != 400 {
		t.Errorf("input/cache read/cache write = %d/%d/%d, want 300/12400/400",
			acc.InputTokens, acc.CacheRead, acc.CacheCreated)
	}
	if acc.PeakStepInputTokens != 6600 {
		t.Errorf("peak = %d, want the second step's 200+6400", acc.PeakStepInputTokens)
	}
}

// TestParseOpenCodeStreamDriftMarkers: a shape opencode 1.18.30 did not emit
// is a drift marker, one per finding, and never a crash or a silent zero.
func TestParseOpenCodeStreamDriftMarkers(t *testing.T) {
	sample := openCodeFixtureLines(t, "opencode_stream_research_sample.jsonl")
	last := len(sample) - 1
	if !strings.Contains(sample[last], `"type":"step_finish"`) {
		t.Fatalf("the sample no longer ends with a step_finish: %s", sample[last])
	}
	without := func(field string) []string {
		var ev map[string]any
		if err := json.Unmarshal([]byte(sample[last]), &ev); err != nil {
			t.Fatal(err)
		}
		delete(ev["part"].(map[string]any), field)
		b, _ := json.Marshal(ev)
		lines := append([]string{}, sample[:last]...)
		return append(lines, string(b))
	}
	var noSteps []string
	for _, line := range sample {
		if !strings.Contains(line, `"type":"step_finish"`) {
			noSteps = append(noSteps, line)
		}
	}
	unknown := append(append([]string{}, sample...), `{"type":"step_summary","timestamp":1,"sessionID":"ses_fixture0000000000000000001","part":{}}`)

	for _, tc := range []struct {
		name  string
		lines []string
		exit  int
		want  string // "" = no marker
	}{
		{"unknown event type", unknown, 0, `unknown event type "step_summary"`},
		{"step_finish without part.tokens", without("tokens"), 0, "a step_finish event has no part.tokens"},
		{"step_finish without part.reason", without("reason"), 0, "a step_finish event has no part.reason"},
		{"zero step_finish on exit 0", noSteps, 0, "the run exited 0 without a step_finish event"},
		{"zero step_finish on a failed exit", noSteps, 1, ""},
		{"a line that is not a JSON event", append(append([]string{}, sample...), "plain text"), 0, "a stdout line is not a JSON event"},
		{"the real capture", sample, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := parseOpenCode(tc.lines).OpenCode()
			s.Finish(tc.exit)
			got := s.DriftMarkers()
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("markers = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || !strings.HasPrefix(got[0], OpenCodeDriftMarker+" ") || !strings.Contains(got[0], tc.want) {
				t.Errorf("markers = %q, want exactly one %s marker saying %q", got, OpenCodeDriftMarker, tc.want)
			}
		})
	}

	// A marker repeated is one marker that says how often, so a stream that
	// drifted on every line cannot bury the log.
	s := parseOpenCode([]string{"x", "y", "z"}).OpenCode()
	if got := s.DriftMarkers(); len(got) != 1 || !strings.HasSuffix(got[0], "(3 times)") {
		t.Errorf("markers = %q, want one marker counted 3 times", got)
	}
}

// ── #1629 real-model OpenCode captures ────────────────────────────────────

// openCodeCaptureParent is the redacted id of every capture's own session.
const openCodeCaptureParent = "ses_fixture0000000000000000001"

// openCodeStepTruth is one step_finish of a real capture, as its ground-truth
// table in testdata/README.md (§ OpenCode: real-model captures) records it.
type openCodeStepTruth struct {
	reason                                string
	input, output, reasoning, read, write int
	cost                                  float64
}

// openCodeCaptureSession is one session of a captured run, with the
// info.tokens and info.cost its sanitized export held at capture time
// (testdata/README.md). The export itself was never kept.
type openCodeCaptureSession struct {
	id, parent      string
	tokens          OpenCodeTokens
	cost            float64
	provider, model string
}

func openCodeTokens(input, output, reasoning, read, write int) OpenCodeTokens {
	t := OpenCodeTokens{Input: input, Output: output, Reasoning: reasoning}
	t.Cache.Read, t.Cache.Write = read, write
	return t
}

// sumOpenCodeSteps is a ground-truth table's sum row.
func sumOpenCodeSteps(steps []openCodeStepTruth) (sum openCodeStepTruth, peak int) {
	for _, s := range steps {
		sum.input += s.input
		sum.output += s.output
		sum.reasoning += s.reasoning
		sum.read += s.read
		sum.write += s.write
		sum.cost += s.cost
		peak = max(peak, s.input+s.read+s.write)
	}
	return sum, peak
}

func closeUSD(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// checkOpenCodeCapture parses a real capture and checks it against its
// ground-truth table: each step_finish, then the parser's totals against the
// table's sum, the peak step prompt and OpenCode's own reported cost. It also
// checks the guardrail every capture shares: only the six event types
// opencode 1.18.30 writes, every event on the run's own session, and no drift
// marker.
func checkOpenCodeCapture(t *testing.T, name string, steps []openCodeStepTruth) *TokenAccumulator {
	t.Helper()
	lines := openCodeFixtureLines(t, name)
	var got []openCodeStepTruth
	for i, line := range lines {
		var ev openCodeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%s line %d is not a JSON event: %v", name, i+1, err)
		}
		if !openCodeKnownEvents[ev.Type] {
			t.Errorf("%s line %d: event type %q is not one opencode 1.18.30 writes", name, i+1, ev.Type)
		}
		if ev.SessionID != openCodeCaptureParent {
			t.Errorf("%s line %d: event on session %q; the stream carries the run's own session %q only",
				name, i+1, ev.SessionID, openCodeCaptureParent)
		}
		if ev.Type == "step_finish" && ev.Part != nil && ev.Part.Tokens != nil && ev.Part.Reason != nil && ev.Part.Cost != nil {
			tk := ev.Part.Tokens
			got = append(got, openCodeStepTruth{*ev.Part.Reason, tk.Input, tk.Output, tk.Reasoning, tk.Cache.Read, tk.Cache.Write, *ev.Part.Cost})
		}
	}
	if len(got) != len(steps) {
		t.Fatalf("%s has %d complete step_finish events, want the README's %d", name, len(got), len(steps))
	}
	for i := range steps {
		if got[i] != steps[i] {
			t.Errorf("%s step %d = %+v, want the README's %+v", name, i+1, got[i], steps[i])
		}
	}

	acc := parseOpenCode(lines)
	s := acc.OpenCode()
	want, peak := sumOpenCodeSteps(steps)
	if s.StepFinishes != len(steps) {
		t.Errorf("%s: step_finish events = %d, want %d", name, s.StepFinishes, len(steps))
	}
	if acc.InputTokens != want.input {
		t.Errorf("%s: input = %d, want the README's sum %d", name, acc.InputTokens, want.input)
	}
	if acc.OutputTokens != want.output+want.reasoning {
		t.Errorf("%s: output = %d, want the README's output plus reasoning, %d+%d = %d",
			name, acc.OutputTokens, want.output, want.reasoning, want.output+want.reasoning)
	}
	if acc.CacheRead != want.read || acc.CacheCreated != want.write {
		t.Errorf("%s: cache read/write = %d/%d, want the README's %d/%d", name, acc.CacheRead, acc.CacheCreated, want.read, want.write)
	}
	if acc.PeakStepInputTokens != peak {
		t.Errorf("%s: peak step prompt = %d, want the README's largest step, %d", name, acc.PeakStepInputTokens, peak)
	}
	if !closeUSD(s.ReportedCostUSD, want.cost) {
		t.Errorf("%s: OpenCode's reported cost = %v, want the README's sum %v", name, s.ReportedCostUSD, want.cost)
	}
	if s.SessionID != openCodeCaptureParent {
		t.Errorf("%s: session = %q, want %q", name, s.SessionID, openCodeCaptureParent)
	}
	s.Finish(0)
	if markers := s.DriftMarkers(); len(markers) != 0 {
		t.Errorf("%s: a real capture produced drift markers: %q", name, markers)
	}
	return acc
}

// replayOpenCodeFold runs a capture through the opencode half of
// Manager.RunStage (parse, finish, apply) with a fake opencode that answers
// the fold's --version, db and export from the session tree and the per-session
// export totals recorded at capture time: the export-reader seam of #1624. It
// returns the accumulator, the RunResult, and the fake's argv log.
func replayOpenCodeFold(t *testing.T, name, dispatched string, sessions []openCodeCaptureSession) (*TokenAccumulator, *adapters.RunResult, string) {
	t.Helper()
	dir := t.TempDir()
	var rows []string
	for _, s := range sessions {
		body := sessionExport(s.tokens.Input, s.tokens.Output, s.tokens.Reasoning, s.tokens.Cache.Read, s.tokens.Cache.Write,
			s.cost, s.provider, s.model)
		if err := os.WriteFile(filepath.Join(dir, s.id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if s.parent != "" {
			rows = append(rows, fmt.Sprintf(`{"id":%q,"parent_id":%q}`, s.id, s.parent))
		}
	}
	listing := filepath.Join(dir, "rows.json")
	if err := os.WriteFile(listing, []byte("["+strings.Join(rows, ",")+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	bin := writeFakeOpenCode(t, fmt.Sprintf(`echo "$*" >> %[1]q
case "$1" in
--version) echo 1.18.30 ;;
db) cat %[2]q ;;
export) cat %[3]q/"$2".json ;;
esac
`, log, listing, dir))

	lines := openCodeFixtureLines(t, name)
	acc := parseOpenCode(lines)
	run := newOpenCodeRun(acc.OpenCode(), nil)
	runRoot := t.TempDir()
	run.fold = testFold(bin, runRoot)
	outcome := run.finish(context.Background(), openCodeExit{bin: bin, env: run.fold.env, runRoot: runRoot,
		exitCode: 0, dispatched: dispatched}, acc)
	result := runResultFromAccumulator(strings.Join(lines, "\n")+"\n", "", acc, &ServedModelTracker{})
	outcome.apply(result)
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsagePartial || len(result.DriftMarkers) != 0 {
		t.Errorf("%s: partial = %v, drift = %q; want a complete fold", name, result.UsagePartial, result.DriftMarkers)
	}
	if result.AdapterVersion != "1.18.30" {
		t.Errorf("%s: adapter version = %q, want 1.18.30", name, result.AdapterVersion)
	}
	return acc, result, string(calls)
}

// openCodeStageCost models ADR-022 § 3's intended stamp: it prices a replayed
// stage by the provider its RunResult records (ModelProvider, ServedModel),
// never by the adapter (ADR-022 § 1). Under that rule a local provider's model
// the registry holds no rate for is a stamped zero, and a registry id is
// priced from the registry's rate card. It is not the production call: the
// scheduler prices by the adapter, "opencode", which leaves a local model's
// stage record unstamped. The record is stamped this way only once #1630
// lands.
func openCodeStageCost(r *adapters.RunResult) (float64, bool) {
	return tokens.CalculateCostForAdapter(r.ModelProvider, r.ServedModel, tokens.TokenCounts{
		Input: r.InputTokens, Output: r.OutputTokens, CacheRead: r.CacheReadTokens,
		CacheCreation5m: r.CacheCreation5mTokens, CacheCreation1h: r.CacheCreation1hTokens,
	})
}

// openCodeLocalSteps is opencode_stream_local_capture.jsonl's ground truth:
// qwen/qwen3.8-27b on LM Studio, three steps (read, edit, stop). The model
// reports reasoning tokens, OpenCode prices the local provider at 0, and
// there is no cache pool.
var openCodeLocalSteps = []openCodeStepTruth{
	{"tool-calls", 5640, 44, 10, 0, 0, 0},
	{"tool-calls", 5809, 78, 7, 0, 0, 0},
	{"stop", 5916, 22, 30, 0, 0, 0},
}

// TestParseOpenCodeRealCaptureLocal: a real run on the lmstudio endpoint. The
// parser's totals are the README's sums of the three step_finish events,
// reasoning folded into output; the session's export total equals them; and
// the stage, served by provider lm-studio, is a stamped zero under ADR-022
// § 3's rule (openCodeStageCost; the stage record is stamped only once #1630
// lands).
func TestParseOpenCodeRealCaptureLocal(t *testing.T) {
	const name = "opencode_stream_local_capture.jsonl"
	checkOpenCodeCapture(t, name, openCodeLocalSteps)

	exported := openCodeTokens(17365, 144, 47, 0, 0)
	if sum, _ := sumOpenCodeSteps(openCodeLocalSteps); exported != openCodeTokens(sum.input, sum.output, sum.reasoning, sum.read, sum.write) {
		t.Errorf("the README's export total %+v is not its step sum %+v", exported, sum)
	}
	_, result, _ := replayOpenCodeFold(t, name, "lmstudio/qwen/qwen3.8-27b", []openCodeCaptureSession{
		{id: openCodeCaptureParent, tokens: exported, provider: "lmstudio", model: "qwen/qwen3.8-27b"},
	})
	if result.InputTokens != 17365 || result.OutputTokens != 144+47 {
		t.Errorf("RunResult input/output = %d/%d, want 17365/191", result.InputTokens, result.OutputTokens)
	}
	if result.ModelProvider != "lm-studio" || result.ServedModel != "lm-studio/qwen/qwen3.8-27b" ||
		result.UpstreamModel != "lmstudio/qwen/qwen3.8-27b" {
		t.Errorf("provider/served/upstream = %q/%q/%q, want lm-studio/lm-studio/qwen/qwen3.8-27b/lmstudio/qwen/qwen3.8-27b",
			result.ModelProvider, result.ServedModel, result.UpstreamModel)
	}
	if result.AdapterReportedCostUSD != 0 {
		t.Errorf("OpenCode's reported cost = %v, want the capture's 0", result.AdapterReportedCostUSD)
	}
	if cost, stamped := openCodeStageCost(result); cost != 0 || !stamped {
		t.Errorf("cost = %v, stamped = %v; ADR-022 § 3 stamps a local provider's stage at zero", cost, stamped)
	}
}

// openCodeRemoteSteps is opencode_stream_remote_capture.jsonl's ground truth:
// the same model and quant as the local capture, served by a second LM Studio
// endpoint under provider key lmstudio-remote, five steps (glob, read, edit,
// grep, stop). As on the lmstudio endpoint, OpenCode prices each step at 0
// and there is no cache pool.
var openCodeRemoteSteps = []openCodeStepTruth{
	{"tool-calls", 5678, 31, 23, 0, 0, 0},
	{"tool-calls", 5767, 43, 7, 0, 0, 0},
	{"tool-calls", 5931, 77, 28, 0, 0, 0},
	{"tool-calls", 6058, 41, 15, 0, 0, 0},
	{"stop", 6208, 26, 64, 0, 0, 0},
}

// TestParseOpenCodeRealCaptureRemote: a real run on the second local
// endpoint, lmstudio-remote. The stream names no provider, so the parser's
// totals are the README's step sums exactly as on the lmstudio endpoint; the
// label comes from the dispatched -m and the export's providerID, and the
// stage records the endpoint id: served and upstream model
// lmstudio-remote/qwen/qwen3.8-27b, never the lmstudio endpoint's
// lm-studio/qwen/qwen3.8-27b. Until declared endpoints resolve an id to its
// provider (#1678), the key normalizes to "other" (ADR-022 § 1), and "other"
// is never priced as a local zero: the stage stays unstamped.
func TestParseOpenCodeRealCaptureRemote(t *testing.T) {
	const name = "opencode_stream_remote_capture.jsonl"
	checkOpenCodeCapture(t, name, openCodeRemoteSteps)
	for i, line := range openCodeFixtureLines(t, name) {
		if strings.Contains(line, "lmstudio") {
			t.Errorf("%s line %d names a provider key; the stream carries none, so the label is the dispatch's", name, i+1)
		}
	}

	exported := openCodeTokens(29642, 218, 137, 0, 0)
	if sum, _ := sumOpenCodeSteps(openCodeRemoteSteps); exported != openCodeTokens(sum.input, sum.output, sum.reasoning, sum.read, sum.write) {
		t.Errorf("the README's export total %+v is not its step sum %+v", exported, sum)
	}
	_, result, _ := replayOpenCodeFold(t, name, "lmstudio-remote/qwen/qwen3.8-27b", []openCodeCaptureSession{
		{id: openCodeCaptureParent, tokens: exported, provider: "lmstudio-remote", model: "qwen/qwen3.8-27b"},
	})
	if result.InputTokens != 29642 || result.OutputTokens != 218+137 {
		t.Errorf("RunResult input/output = %d/%d, want 29642/355", result.InputTokens, result.OutputTokens)
	}
	if result.ModelProvider != "other" || result.ServedModel != "lmstudio-remote/qwen/qwen3.8-27b" ||
		result.UpstreamModel != "lmstudio-remote/qwen/qwen3.8-27b" {
		t.Errorf("provider/served/upstream = %q/%q/%q, want other/lmstudio-remote/qwen/qwen3.8-27b/lmstudio-remote/qwen/qwen3.8-27b",
			result.ModelProvider, result.ServedModel, result.UpstreamModel)
	}
	if result.AdapterReportedCostUSD != 0 {
		t.Errorf("OpenCode's reported cost = %v, want the capture's 0", result.AdapterReportedCostUSD)
	}
	if cost, stamped := openCodeStageCost(result); cost != 0 || stamped {
		t.Errorf("cost = %v, stamped = %v; an undeclared endpoint id is other, never a stamped local zero", cost, stamped)
	}
}

// openCodeCloudSteps is opencode_stream_cloud_capture.jsonl's ground truth:
// the repository's stub provider on 127.0.0.1, dispatched as xai/grok-4.6, so
// OpenCode prices each step from its bundled catalog. It stands in for a
// hosted provider's shape until #1680 captures one; the stub reports no cache
// pools.
var openCodeCloudSteps = []openCodeStepTruth{
	{"tool-calls", 1539, 8, 0, 0, 0, 0.003126},
	{"stop", 1550, 6, 0, 0, 0, 0.003136},
}

// TestParseOpenCodeRealCaptureCloud: the hosted shape, from the stub under a
// hosted provider key. OpenCode reports a non-zero cost per step; the parser
// keeps it only as the CLI's figure. The stage, served by a registry model,
// is priced from the registry, and that price is not OpenCode's.
func TestParseOpenCodeRealCaptureCloud(t *testing.T) {
	const name = "opencode_stream_cloud_capture.jsonl"
	acc := checkOpenCodeCapture(t, name, openCodeCloudSteps)
	if acc.OpenCode().ReportedCostUSD <= 0 {
		t.Fatalf("the cloud-shape capture reports no cost, so it no longer shows a priced provider")
	}

	_, result, _ := replayOpenCodeFold(t, name, "xai/grok-4.6", []openCodeCaptureSession{
		{id: openCodeCaptureParent, tokens: openCodeTokens(3089, 14, 0, 0, 0), cost: 0.006262, provider: "xai", model: "grok-4.6"},
	})
	if result.ModelProvider != "xai" || result.ServedModel != "grok-4.6" || result.UpstreamModel != "xai/grok-4.6" {
		t.Errorf("provider/served/upstream = %q/%q/%q, want xai/grok-4.6/xai/grok-4.6",
			result.ModelProvider, result.ServedModel, result.UpstreamModel)
	}
	if !closeUSD(result.AdapterReportedCostUSD, 0.006262) {
		t.Errorf("OpenCode's reported cost = %v, want the capture's 0.006262", result.AdapterReportedCostUSD)
	}
	cost, stamped := openCodeStageCost(result)
	if !stamped || cost <= 0 {
		t.Errorf("cost = %v, stamped = %v; a registry model's stage is priced, never unstamped", cost, stamped)
	}
	if closeUSD(cost, result.AdapterReportedCostUSD) {
		t.Errorf("the stage's price %v is OpenCode's catalog figure; ADR-022 § 3 re-prices from the registry", cost)
	}
}

// openCodeSubagentSteps is opencode_stream_subagent_capture.jsonl's ground
// truth: the run's own five steps. Its two subagent sessions' steps are not
// in the stream.
var openCodeSubagentSteps = []openCodeStepTruth{
	{"tool-calls", 5681, 110, 12, 0, 0, 0},
	{"tool-calls", 5915, 108, 60, 0, 0, 0},
	{"tool-calls", 6140, 45, 85, 0, 0, 0},
	{"tool-calls", 6386, 79, 23, 0, 0, 0},
	{"stop", 6510, 97, 70, 0, 0, 0},
}

// openCodeSubagentSessions is the subagent run's session tree and each
// session's export total, as recorded at capture time.
var openCodeSubagentSessions = []openCodeCaptureSession{
	{id: openCodeCaptureParent, tokens: openCodeTokens(30632, 439, 250, 0, 0), provider: "lmstudio", model: "qwen/qwen3.8-27b"},
	{id: "ses_fixture0000000000000000002", parent: openCodeCaptureParent, tokens: openCodeTokens(12832, 95, 18, 0, 0),
		provider: "lmstudio", model: "qwen/qwen3.8-27b"},
	{id: "ses_fixture0000000000000000003", parent: openCodeCaptureParent, tokens: openCodeTokens(6330, 49, 15, 0, 0),
		provider: "lmstudio", model: "qwen/qwen3.8-27b"},
}

// TestParseOpenCodeRealCaptureSubagent: a real run whose model used the task
// tool twice. The stream carries only the run's own session, so its sum is
// the parent's export total; the fold adds both subagent sessions, found in
// the session table, and the stage's usage equals the three recorded export
// totals. The second subagent's bash call was auto-rejected: the parent's
// task call failed with that rejection inside its own message, the parent
// went on to fix the bug, and the process exited 0. The notice on stderr is
// the subagent's, and it still decides the stage's marker (ADR-022 § 9).
func TestParseOpenCodeRealCaptureSubagent(t *testing.T) {
	const name = "opencode_stream_subagent_capture.jsonl"
	acc := checkOpenCodeCapture(t, name, openCodeSubagentSteps)
	if sum, _ := sumOpenCodeSteps(openCodeSubagentSteps); openCodeSubagentSessions[0].tokens !=
		openCodeTokens(sum.input, sum.output, sum.reasoning, sum.read, sum.write) {
		t.Errorf("the README's parent export total %+v is not the stream's step sum %+v", openCodeSubagentSessions[0].tokens, sum)
	}

	// The subagent sessions are named only inside the parent's task tool
	// events, never as an event's session, and the parser reads none of
	// those names: it finds the sessions in the session table.
	var tasks []string // each task call's status
	stepsAfterFailedTask := 0
	for _, line := range openCodeFixtureLines(t, name) {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				Tool  string `json:"tool"`
				State struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				} `json:"state"`
			} `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		for _, child := range openCodeSubagentSessions[1:] {
			if strings.Contains(line, child.id) && (ev.Type != "tool_use" || ev.Part.Tool != "task") {
				t.Errorf("subagent session %s is named outside a task tool event: %s", child.id, line)
			}
		}
		if ev.Type == "tool_use" && ev.Part.Tool == "task" {
			tasks = append(tasks, ev.Part.State.Status)
			if ev.Part.State.Status == "error" {
				want := "Subagent failed (task_id: " + openCodeSubagentSessions[2].id + "): " + openCodeRejectedToolError
				if ev.Part.State.Error != want {
					t.Errorf("the failed task's error = %q, want %q", ev.Part.State.Error, want)
				}
			}
		}
		if ev.Type == "step_finish" && len(tasks) == 2 {
			stepsAfterFailedTask++
		}
	}
	if len(tasks) != 2 || tasks[0] != "completed" || tasks[1] != "error" {
		t.Fatalf("task tool events = %q, want one completed then one failed", tasks)
	}
	if stepsAfterFailedTask != 4 {
		t.Errorf("step_finish events from the failed task on = %d, want 4: the parent continued after its subagent's rejection", stepsAfterFailedTask)
	}
	if n := acc.OpenCode().RejectedToolCalls; n != 0 {
		t.Errorf("rejected tool calls = %d; the task call failed on its subagent's rejection, not OpenCode's own", n)
	}

	// The fold: every descendant exported, and the stage's usage the sum of
	// the three sessions' export totals.
	folded, result, calls := replayOpenCodeFold(t, name, "lmstudio/qwen/qwen3.8-27b", openCodeSubagentSessions)
	var want OpenCodeTokens
	for _, s := range openCodeSubagentSessions {
		want.Input += s.tokens.Input
		want.Output += s.tokens.Output
		want.Reasoning += s.tokens.Reasoning
		want.Cache.Read += s.tokens.Cache.Read
		want.Cache.Write += s.tokens.Cache.Write
	}
	if folded.InputTokens != want.Input || folded.OutputTokens != want.Output+want.Reasoning {
		t.Errorf("folded input/output = %d/%d, want the export totals' %d/%d (the parent alone is 30632/689)",
			folded.InputTokens, folded.OutputTokens, want.Input, want.Output+want.Reasoning)
	}
	if folded.CacheRead != want.Cache.Read || folded.CacheCreated != want.Cache.Write {
		t.Errorf("folded cache read/write = %d/%d, want %d/%d", folded.CacheRead, folded.CacheCreated, want.Cache.Read, want.Cache.Write)
	}
	if result.InputTokens != want.Input || result.PeakStepInputTokens != 6510 {
		t.Errorf("RunResult input/peak = %d/%d, want %d/6510: a subagent's session total is not a step",
			result.InputTokens, result.PeakStepInputTokens, want.Input)
	}
	if cost, stamped := openCodeStageCost(result); cost != 0 || !stamped || result.AdapterReportedCostUSD != 0 {
		t.Errorf("cost = %v, stamped = %v, reported = %v; want ADR-022 § 3's stamped local zero", cost, stamped, result.AdapterReportedCostUSD)
	}
	for _, child := range openCodeSubagentSessions[1:] {
		if !strings.Contains(calls, "export "+child.id+" --sanitize --pure") {
			t.Errorf("subagent session %s was not exported:\n%s", child.id, calls)
		}
	}

	// Through a stage: the subagent's notice on stderr fails the exit-0 run,
	// with the marker for bash, and the parent's usage is kept.
	stream := readTestdata(t, name)
	stderr := readTestdata(t, "opencode_stream_subagent_stderr.txt")
	for _, tc := range []struct {
		allowed []string
		marker  string
	}{
		{[]string{"Read", "Edit", "Task", "Bash"}, PermissionRejectedMarker + " tool=bash"},
		{[]string{"Read", "Edit", "Task"}, PermissionDeniedMarker + " tool=bash"},
	} {
		staged, _ := openCodeStageRun(t, stream, stderr, 0, tc.allowed, nil)
		if staged.ExitCode != 1 || !strings.HasSuffix(staged.Stderr, tc.marker+"\n") {
			t.Errorf("allowed %q: exit %d, stderr %q; want exit 1 ending in %q", tc.allowed, staged.ExitCode, staged.Stderr, tc.marker)
		}
		if staged.InputTokens != 30632 || len(staged.DriftMarkers) != 0 {
			t.Errorf("allowed %q: input %d, drift %q; want the parent's 30632 and no drift", tc.allowed, staged.InputTokens, staged.DriftMarkers)
		}
	}
}
