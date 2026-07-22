// stream.go parses NDJSON output from AI CLI adapters (Claude, Codex, Gemini)
// to extract token usage, tool calls, and other events. Supports multiple
// output formats with a unified TokenAccumulator.
package execution

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// StreamEvent represents a single NDJSON event from Claude's stream-json output.
type StreamEvent struct {
	Type string `json:"type"`

	// For "result" events
	Result *StreamResult `json:"result,omitempty"`

	// For "content_block_start" / "content_block_delta" events
	ContentBlock *ContentBlock `json:"content_block,omitempty"`

	// For "message" events
	Message *StreamMessage `json:"message,omitempty"`

	// Session ID for conversation resumption
	SessionID string `json:"session_id,omitempty"`

	// Subtype for tool_use events
	Subtype string `json:"subtype,omitempty"`

	// Model is set on system/init events: the requested model, canonicalized
	// by the CLI (#91).
	Model string `json:"model,omitempty"`

	// OriginalModel/FallbackModel/RefusalCategory are set on the CLI's
	// system/model_refusal_fallback event (#91): a safety refusal makes the
	// CLI silently retry the turn on a fallback model and still exit 0.
	// See docs/spikes/fable-5-behavior-porting.md §8.3 for a captured event.
	OriginalModel   string `json:"original_model,omitempty"`
	FallbackModel   string `json:"fallback_model,omitempty"`
	RefusalCategory string `json:"api_refusal_category,omitempty"`
}

// StreamResult contains token usage from a "result" event.
type StreamResult struct {
	// Token usage is in different places depending on Claude CLI version
	Usage *TokenUsage `json:"usage,omitempty"`

	// Legacy fields
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// StreamMessage contains message-level data.
type StreamMessage struct {
	Usage *TokenUsage `json:"usage,omitempty"`

	// Model is the model that served this message. After a refusal fallback
	// every assistant message reports the fallback model, so the LAST
	// observed value is the stage's served model (#91).
	Model string `json:"model,omitempty"`
}

// TokenUsage holds token count data from Claude's output.
type TokenUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	CacheCreationInput int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInput     int `json:"cache_read_input_tokens,omitempty"`
}

// ContentBlock represents a content block in the stream.
type ContentBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input string `json:"input,omitempty"`
}

// TokenAccumulator tracks cumulative token usage across a stage.
type TokenAccumulator struct {
	InputTokens  int
	OutputTokens int
	CacheCreated int
	CacheRead    int
	// PremiumRequests is the copilot billing unit. The GitHub Copilot CLI is
	// subscription-based and emits no token counts — its measurable consumption
	// is the premium-request count from its stats footer (#52). Zero for
	// token-metered adapters (claude/codex/gemini).
	PremiumRequests int
}

// ParseStreamLine parses a single NDJSON line from Claude's stream-json output.
// Returns the parsed event and whether token usage was updated.
func (acc *TokenAccumulator) ParseStreamLine(line string) (*StreamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil, false
	}

	var event StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return nil, false
	}

	tokenUpdated := false

	// Extract token usage from various event types
	switch event.Type {
	case "result":
		if event.Result != nil {
			if event.Result.Usage != nil {
				acc.updateFromUsage(event.Result.Usage)
				tokenUpdated = true
			} else if event.Result.InputTokens > 0 || event.Result.OutputTokens > 0 {
				// Legacy format
				acc.InputTokens = event.Result.InputTokens
				acc.OutputTokens = event.Result.OutputTokens
				tokenUpdated = true
			}
		}

	case "message":
		if event.Message != nil && event.Message.Usage != nil {
			acc.updateFromUsage(event.Message.Usage)
			tokenUpdated = true
		}
	}

	return &event, tokenUpdated
}

func (acc *TokenAccumulator) updateFromUsage(usage *TokenUsage) {
	// Claude reports cumulative totals, not deltas.
	// Take the max to handle out-of-order events.
	if usage.InputTokens > acc.InputTokens {
		acc.InputTokens = usage.InputTokens
	}
	if usage.OutputTokens > acc.OutputTokens {
		acc.OutputTokens = usage.OutputTokens
	}
	if usage.CacheCreationInput > acc.CacheCreated {
		acc.CacheCreated = usage.CacheCreationInput
	}
	if usage.CacheReadInput > acc.CacheRead {
		acc.CacheRead = usage.CacheReadInput
	}
}

// Total returns the total token count (input + output).
func (acc *TokenAccumulator) Total() int {
	return acc.InputTokens + acc.OutputTokens
}

// ModelRefusalFallback describes the claude CLI's internal model swap on a
// safety refusal (#91): the CLI emits a system/model_refusal_fallback event,
// silently retries the turn on the fallback model, and the session still
// exits 0. See docs/spikes/fable-5-behavior-porting.md §8.3.
type ModelRefusalFallback struct {
	OriginalModel   string
	FallbackModel   string
	RefusalCategory string
}

// ServedModelTracker derives the model that ACTUALLY served a stage from the
// stream, which is not guaranteed to be the requested one: the CLI's refusal
// fallback swaps models mid-session without failing the run (#91).
// Attribution only — the fallback is CLI safety behavior and is never
// suppressed or retried here.
type ServedModelTracker struct {
	// ServedModel is the last model observed in the stream: seeded by
	// system/init (the canonicalized requested model), overridden by each
	// message's model and by a refusal fallback event. Empty when the stream
	// carried no model information (non-claude adapters, plain-text output).
	ServedModel string
	// Fallback is non-nil once a model_refusal_fallback event was observed.
	Fallback *ModelRefusalFallback
}

// Observe updates the tracker from one parsed stream event. It returns the
// fallback record when THIS event is a model_refusal_fallback, as the
// caller's hook for the one-time observable log line.
func (t *ServedModelTracker) Observe(event *StreamEvent) *ModelRefusalFallback {
	if t == nil || event == nil {
		return nil
	}
	switch event.Type {
	case "system":
		if event.Subtype == "model_refusal_fallback" && event.FallbackModel != "" {
			fb := &ModelRefusalFallback{
				OriginalModel:   event.OriginalModel,
				FallbackModel:   event.FallbackModel,
				RefusalCategory: event.RefusalCategory,
			}
			t.Fallback = fb
			t.ServedModel = event.FallbackModel
			return fb
		}
		if event.Subtype == "init" && event.Model != "" && t.ServedModel == "" {
			t.ServedModel = event.Model
		}
	case "assistant", "message":
		if event.Message != nil && event.Message.Model != "" {
			t.ServedModel = event.Message.Model
		}
	}
	return nil
}

// --- Codex NDJSON stream parsing ---

// codexEvent represents a Codex CLI NDJSON event.
type codexEvent struct {
	Type  string      `json:"type"`
	Item  *codexItem  `json:"item,omitempty"`
	Usage *codexUsage `json:"usage,omitempty"`
}

type codexItem struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	Status           string `json:"status,omitempty"`
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
}

// codexUsage is the token payload on a `turn.completed` event. Following the
// OpenAI convention, InputTokens is the cache-inclusive prompt total and
// CachedInputTokens its cached subset (mapped onto CacheRead). @see Issue #4027
type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens"`
}

// ParseCodexStreamLine parses a single NDJSON line from Codex CLI output.
// Token usage is extracted from the `turn.completed` event's `usage` payload
// (#4027, superseding the earlier "no native token counts" assumption).
func (acc *TokenAccumulator) ParseCodexStreamLine(line string) (*StreamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil, false
	}

	var raw codexEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, false
	}

	// Map Codex events to unified StreamEvent
	event := &StreamEvent{Type: raw.Type}
	tokenUpdated := false
	if raw.Type == "item.completed" && raw.Item != nil {
		if raw.Item.Type == "agent_message" {
			event.Type = "message"
			event.Subtype = "text"
		}
	}

	// turn.completed carries PER-TURN token usage. Unlike Claude/Gemini (which
	// report cumulative totals → max), Codex reports each turn independently, so
	// sum across turns to total the invocation. input_tokens is cache-inclusive;
	// clamp negatives, clamp the cached subset to the prompt total, and store
	// only the non-cached remainder so input/cacheRead are disjoint pools (the
	// same convention the SDK and Claude parsers use). @see Issue #4027
	if raw.Type == "turn.completed" && raw.Usage != nil {
		u := raw.Usage
		input := u.InputTokens
		if input < 0 {
			input = 0
		}
		output := u.OutputTokens
		if output < 0 {
			output = 0
		}
		cached := u.CachedInputTokens
		if cached < 0 {
			cached = 0
		}
		if cached > input {
			cached = input
		}
		acc.InputTokens += input - cached
		acc.OutputTokens += output
		acc.CacheRead += cached
		tokenUpdated = true
	}

	return event, tokenUpdated
}

// --- Gemini stream-json NDJSON parsing ---

// geminiEvent represents a Gemini CLI stream-json NDJSON event.
type geminiEvent struct {
	Type     string         `json:"type"`
	Status   string         `json:"status,omitempty"`
	Stats    map[string]int `json:"stats,omitempty"`
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content,omitempty"`
	Severity string         `json:"severity,omitempty"`
	Message  string         `json:"message,omitempty"`
	Error    *geminiError   `json:"error,omitempty"`
	Result   *geminiResult  `json:"result,omitempty"`
}

type geminiError struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
}

type geminiResult struct {
	Usage *geminiResultUsage `json:"usage,omitempty"`
}

type geminiResultUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Input        int `json:"input,omitempty"` // alternate key
	Cached       int `json:"cached,omitempty"`
}

// normalizeCacheInclusiveInput splits a cache-INCLUSIVE prompt total into its
// disjoint (non-cached, cached) components: clamps negatives and clamps the
// cached subset to the prompt total. Gemini reports the prompt count
// cache-inclusive (it already contains the cached subset), so storing it
// verbatim alongside the cached subset would double-count cached tokens in
// TotalTokens(). Mirrors the inline #4027 Codex normalization. (#4036)
func normalizeCacheInclusiveInput(input, cached int) (nonCached, cachedClamped int) {
	if input < 0 {
		input = 0
	}
	if cached < 0 {
		cached = 0
	}
	if cached > input {
		cached = input
	}
	return input - cached, cached
}

// ParseGeminiStreamLine parses a single NDJSON line from Gemini CLI stream-json output.
// Extracts token usage from "result" events with stats or result.usage fields.
func (acc *TokenAccumulator) ParseGeminiStreamLine(line string) (*StreamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return nil, false
	}

	var raw geminiEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, false
	}

	event := &StreamEvent{Type: raw.Type}
	tokenUpdated := false

	switch raw.Type {
	case "result":
		// Token usage from stats field (primary)
		if raw.Stats != nil {
			inputTokens := raw.Stats["input_tokens"]
			if inputTokens == 0 {
				inputTokens = raw.Stats["input"]
			}
			outputTokens := raw.Stats["output_tokens"]
			// Gemini's input count is cache-INCLUSIVE; store only the non-cached
			// remainder so input/cacheRead stay disjoint pools (no double-count
			// in TotalTokens). Mirrors the #4027 Codex normalization. (#4036)
			nonCachedInput, cached := normalizeCacheInclusiveInput(inputTokens, raw.Stats["cached"])

			if nonCachedInput > acc.InputTokens {
				acc.InputTokens = nonCachedInput
				tokenUpdated = true
			}
			if outputTokens > acc.OutputTokens {
				acc.OutputTokens = outputTokens
				tokenUpdated = true
			}
			if cached > acc.CacheRead {
				acc.CacheRead = cached
				tokenUpdated = true
			}
		}

		// Fallback: token usage from result.usage field
		if raw.Result != nil && raw.Result.Usage != nil {
			u := raw.Result.Usage
			inputTokens := u.InputTokens
			if inputTokens == 0 {
				inputTokens = u.Input
			}
			// Cache-inclusive input → disjoint (non-cached, cached). #4036
			nonCachedInput, cached := normalizeCacheInclusiveInput(inputTokens, u.Cached)
			if nonCachedInput > acc.InputTokens {
				acc.InputTokens = nonCachedInput
				tokenUpdated = true
			}
			if u.OutputTokens > acc.OutputTokens {
				acc.OutputTokens = u.OutputTokens
				tokenUpdated = true
			}
			if cached > acc.CacheRead {
				acc.CacheRead = cached
				tokenUpdated = true
			}
		}

	case "message":
		if raw.Role == "assistant" {
			event.Subtype = "text"
		}
	}

	return event, tokenUpdated
}

// --- Copilot plain-text stats-footer parsing ---
//
// The GitHub Copilot CLI does NOT emit NDJSON — it prints the agent's response
// as plain text followed by a human-readable stats footer (suppressed only by
// the `-s` flag, which the adapter deliberately does not pass). The footer
// carries the CLI's own premium-request estimate and a session id, e.g.:
//
//	Session ID: 221b5571-3998-47e1-b57a-552cf9078947
//	Duration: 50s
//	Usage: Total usage est: 3 Premium requests
//	Total code changes: 12 lines added, 4 lines removed
//
// Copilot is subscription-based and reports no token counts, so the accumulator
// records the real premium-request count (the billable unit) instead of the
// silent-zero token totals the Claude parser produced when copilot fell through
// to it (#52).

// copilotPremiumRequestsRe extracts the premium-request estimate from the stats
// footer. Tolerates the "Total usage est: N" prefix and a bare "N Premium
// requests", singular/plural, and a fractional count (some models bill <1).
var copilotPremiumRequestsRe = regexp.MustCompile(`(?i)([\d]+(?:\.[\d]+)?)\s+premium\s+requests?\b`)

// copilotSessionIDRe extracts the session id from the "Session ID: <id>" footer line.
var copilotSessionIDRe = regexp.MustCompile(`(?i)^\s*session id:\s*(\S+)`)

// ParseCopilotStreamLine parses a single plain-text line from GitHub Copilot CLI
// output. It updates PremiumRequests from the stats-footer usage line and
// surfaces the session id; every line maps to a text message event so phase
// progress still starts. Copilot emits no token counts, so InputTokens/
// OutputTokens are never touched here (#52).
func (acc *TokenAccumulator) ParseCopilotStreamLine(line string) (*StreamEvent, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, false
	}

	event := &StreamEvent{Type: "message", Subtype: "text"}

	if m := copilotSessionIDRe.FindStringSubmatch(trimmed); m != nil {
		event.Type = "system"
		event.Subtype = "session"
		event.SessionID = m[1]
	}

	tokenUpdated := false
	if m := copilotPremiumRequestsRe.FindStringSubmatch(trimmed); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 {
			// Round to the nearest whole request for the integer accumulator; the
			// footer is cumulative for the invocation, so take the max across lines
			// (a defensive no-op — the footer prints once). Never fabricate a count.
			rounded := int(v + 0.5)
			if rounded > acc.PremiumRequests {
				acc.PremiumRequests = rounded
			}
			tokenUpdated = true
		}
	}

	return event, tokenUpdated
}

// AdapterStreamFormat identifies which stream parser to use.
type AdapterStreamFormat string

const (
	StreamFormatClaude  AdapterStreamFormat = "claude"
	StreamFormatCodex   AdapterStreamFormat = "codex"
	StreamFormatGemini  AdapterStreamFormat = "gemini"
	StreamFormatCopilot AdapterStreamFormat = "copilot"
)

// ParseLine dispatches to the correct stream parser based on adapter format.
func (acc *TokenAccumulator) ParseLine(format AdapterStreamFormat, line string) (*StreamEvent, bool) {
	switch format {
	case StreamFormatCodex:
		return acc.ParseCodexStreamLine(line)
	case StreamFormatGemini:
		return acc.ParseGeminiStreamLine(line)
	case StreamFormatCopilot:
		return acc.ParseCopilotStreamLine(line)
	default:
		return acc.ParseStreamLine(line)
	}
}

// StreamFormatForAdapter returns the stream format for a given adapter name.
func StreamFormatForAdapter(adapterName string) AdapterStreamFormat {
	switch {
	case strings.HasPrefix(adapterName, "codex"):
		return StreamFormatCodex
	case strings.HasPrefix(adapterName, "gemini"):
		return StreamFormatGemini
	case strings.HasPrefix(adapterName, "copilot"):
		return StreamFormatCopilot
	default:
		return StreamFormatClaude
	}
}
