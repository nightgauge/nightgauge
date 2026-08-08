package tokens

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Cache-pricing oracle tests (#358).
//
// The oracle is the vendor's own `total_cost_usd`, read out of a REAL captured
// Claude CLI transcript — never a hand-authored number. Hand-authoring the
// expectation is how #166/#300 stayed green against a fiction: the formula and
// the test would agree with each other and disagree with the bill.
//
// The fixtures live in internal/execution/testdata (their capture provenance is
// documented in that directory's README, which forbids synthesizing a
// replacement). They are READ here, never written: this package owns the
// pricing formula, internal/execution owns the parser.
//
// Tier coverage — both cache-creation rates are bill-proven, by different
// fixtures. The primary capture's cache creation is 100% 1h-tier, so it
// bill-proves the 1h rate on its own. The subagent capture carries a turn-level
// split (7890 5m / 5460 1h), so reproducing its bill to delta-zero proves BOTH
// tiers: a wrong 5m rate cannot hide behind a correct 1h rate there.
const fixtureDir = "../../execution/testdata"

// streamEnvelope is the subset of a Claude CLI stream-json line this oracle
// reads. Field names are the CLI's, not ours.
type streamEnvelope struct {
	Type         string   `json:"type"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
		// CacheCreation is the per-tier split Anthropic bills on: a 5-minute
		// TTL write at 1.25x base input and a 1-hour TTL write at 2.0x.
		CacheCreation struct {
			Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens              int     `json:"inputTokens"`
		OutputTokens             int     `json:"outputTokens"`
		CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
		CostUSD                  float64 `json:"costUSD"`
	} `json:"modelUsage"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
			CacheCreation        struct {
				Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// readFixture parses every line of a captured stream-json transcript.
func readFixture(t *testing.T, name string) []streamEnvelope {
	t.Helper()
	path := filepath.Join(fixtureDir, name)
	f, err := os.Open(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []streamEnvelope
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev streamEnvelope
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("parse fixture %s: %v", path, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("fixture %s parsed to zero events — the oracle would pass vacuously", path)
	}
	return out
}

// soleModel returns the single model id an envelope's modelUsage names. The
// oracle must price the model the vendor actually billed, not one we assume.
func soleModel(t *testing.T, ev streamEnvelope) string {
	t.Helper()
	if len(ev.ModelUsage) != 1 {
		t.Fatalf("expected exactly one model in modelUsage, got %d", len(ev.ModelUsage))
	}
	for id := range ev.ModelUsage {
		return id
	}
	return ""
}

// costTolerance is the match window: 1e-9 dollars is a billionth of a dollar, far below
// any rounding the vendor applies, so a passing assertion means the formula
// reproduces the bill exactly.
const costTolerance = 1e-9

// TestCalculateCost_MatchesVendorTotalOnRealCapture prices the counts from a
// real `result` envelope and requires the answer to equal the vendor's own
// total_cost_usd on the same envelope.
func TestCalculateCost_MatchesVendorTotalOnRealCapture(t *testing.T) {
	events := readFixture(t, "claude_stream_real_capture.jsonl")

	checked := 0
	for i, ev := range events {
		if ev.Type != "result" || ev.TotalCostUSD == nil {
			continue
		}
		model := soleModel(t, ev)
		got := CalculateCost(model, TokenCounts{
			Input:           ev.Usage.InputTokens,
			Output:          ev.Usage.OutputTokens,
			CacheRead:       ev.Usage.CacheReadInputTokens,
			CacheCreation5m: ev.Usage.CacheCreation.Ephemeral5m,
			CacheCreation1h: ev.Usage.CacheCreation.Ephemeral1h,
		})
		want := *ev.TotalCostUSD
		if math.Abs(got-want) > costTolerance {
			t.Errorf("event %d: CalculateCost(%s, in=%d out=%d cacheRead=%d cache5m=%d cache1h=%d) = %.10f, vendor total_cost_usd = %.10f (off by %.10f, %.2fx)",
				i, model, ev.Usage.InputTokens, ev.Usage.OutputTokens,
				ev.Usage.CacheReadInputTokens, ev.Usage.CacheCreation.Ephemeral5m,
				ev.Usage.CacheCreation.Ephemeral1h, got, want, want-got, want/got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no result envelope carrying total_cost_usd found — the oracle checked nothing")
	}
}

// TestCalculateCost_MatchesVendorTotalOnSubagentCapture prices the SESSION
// cumulative counts of the subagent capture.
//
// This fixture's envelopes are not individually self-describing: its README
// records that `usage` is a per-envelope DELTA that excludes subagent turns
// while `total_cost_usd` is session-cumulative. The final envelope's
// `modelUsage` is the only complete count in the file, and the per-tier
// cache-creation split lives only on the assistant turns — so the oracle sums
// the deduped turns for the split and cross-checks that sum against
// modelUsage before pricing anything.
func TestCalculateCost_MatchesVendorTotalOnSubagentCapture(t *testing.T) {
	events := readFixture(t, "claude_stream_subagent_multi_result.jsonl")

	// Per-tier cache-creation split, summed over DISTINCT assistant turns (the
	// CLI repeats a turn's usage once per content block).
	seen := map[string]bool{}
	var turnCache5m, turnCache1h, turnInput, turnCacheRead int
	for _, ev := range events {
		if ev.Type != "assistant" || ev.Message.ID == "" || seen[ev.Message.ID] {
			continue
		}
		seen[ev.Message.ID] = true
		turnInput += ev.Message.Usage.InputTokens
		turnCacheRead += ev.Message.Usage.CacheReadInputTokens
		turnCache5m += ev.Message.Usage.CacheCreation.Ephemeral5m
		turnCache1h += ev.Message.Usage.CacheCreation.Ephemeral1h
	}
	if len(seen) == 0 {
		t.Fatal("no assistant turns found — the tier split would be vacuously zero")
	}

	var finals []streamEnvelope
	for _, ev := range events {
		if ev.Type == "result" && ev.TotalCostUSD != nil {
			finals = append(finals, ev)
		}
	}
	if len(finals) < 2 {
		t.Fatalf("expected the multi-result capture to carry >= 2 result envelopes, got %d", len(finals))
	}
	// total_cost_usd is session-cumulative, so it may never decrease.
	for i := 1; i < len(finals); i++ {
		if *finals[i].TotalCostUSD < *finals[i-1].TotalCostUSD {
			t.Fatalf("total_cost_usd decreased across envelopes (%f -> %f): the fixture is no longer session-cumulative and this oracle's premise is void",
				*finals[i-1].TotalCostUSD, *finals[i].TotalCostUSD)
		}
	}

	last := finals[len(finals)-1]
	model := soleModel(t, last)
	mu := last.ModelUsage[model]

	// Cross-check the parsed split against the vendor's own cumulative totals
	// before trusting it. A mismatch means the fixture was recaptured with a
	// different shape, not that the formula is wrong.
	if turnCache5m+turnCache1h != mu.CacheCreationInputTokens {
		t.Fatalf("turn-summed cache creation %d (5m %d + 1h %d) != modelUsage cumulative %d",
			turnCache5m+turnCache1h, turnCache5m, turnCache1h, mu.CacheCreationInputTokens)
	}
	if turnInput != mu.InputTokens || turnCacheRead != mu.CacheReadInputTokens {
		t.Fatalf("turn-summed input/cacheRead (%d/%d) != modelUsage cumulative (%d/%d)",
			turnInput, turnCacheRead, mu.InputTokens, mu.CacheReadInputTokens)
	}

	got := CalculateCost(model, TokenCounts{
		Input:           mu.InputTokens,
		Output:          mu.OutputTokens,
		CacheRead:       mu.CacheReadInputTokens,
		CacheCreation5m: turnCache5m,
		CacheCreation1h: turnCache1h,
	})
	want := *last.TotalCostUSD
	if math.Abs(got-want) > costTolerance {
		t.Errorf("CalculateCost(%s, in=%d out=%d cacheRead=%d cache5m=%d cache1h=%d) = %.10f, vendor total_cost_usd = %.10f (off by %.10f, %.2fx)",
			model, mu.InputTokens, mu.OutputTokens, mu.CacheReadInputTokens,
			turnCache5m, turnCache1h, got, want, want-got, want/got)
	}
}
