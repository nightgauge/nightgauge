package routing

import (
	"reflect"
	"testing"
)

// selection_test.go pins the Go selection query (#581) to the SAME rungs the
// TS derivation produces (packages/nightgauge-sdk/tests/eval/
// selectionQuery.test.ts) — the cross-language parity pin for the candidate
// ladder, and the capability-absence discipline of spike #568 §4.3.

func TestTierBandsAscendingDerivesFromStrongestFirst(t *testing.T) {
	want := []string{TierHaiku, TierSonnet, TierOpus, TierFable}
	if got := TierBandsAscending(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TierBandsAscending() = %v, want %v", got, want)
	}
}

func TestIsTierBand(t *testing.T) {
	for _, band := range TierBandsStrongestFirst {
		if !IsTierBand(band) {
			t.Errorf("IsTierBand(%q) = false, want true", band)
		}
	}
	for _, notBand := range []string{"", "claude-sonnet-5", "grok-4.6", "medium"} {
		if IsTierBand(notBand) {
			t.Errorf("IsTierBand(%q) = true, want false", notBand)
		}
	}
}

func TestCandidateLadderAnthropicSpansModels(t *testing.T) {
	// Parity with the TS test: single-band models rung at their DECLARED
	// default effort; haiku declares no effort axis and thinking off.
	want := []EnvelopeRung{
		{Band: "fable", ModelID: "claude-fable-5", Effort: "high", Thinking: "on"},
		{Band: "opus", ModelID: "claude-opus-5", Effort: "high", Thinking: "on"},
		{Band: "sonnet", ModelID: "claude-sonnet-5", Effort: "high", Thinking: "on"},
		{Band: "haiku", ModelID: "claude-haiku-4-5-20251001", Effort: "", Thinking: "off"},
	}
	if got := CandidateLadder("anthropic", ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("CandidateLadder(anthropic) = %+v, want %+v", got, want)
	}
}

func TestCandidateLadderXaiDescendsThroughEffort(t *testing.T) {
	// The #532 band collapse expressed honestly: all four bands map to
	// grok-4.6, so the rungs descend through EFFORT within the one model — a
	// real cost/latency ladder instead of four identical band rungs.
	want := []EnvelopeRung{
		{Band: "fable", ModelID: "grok-4.6", Effort: "xhigh", Thinking: "on"},
		{Band: "opus", ModelID: "grok-4.6", Effort: "high", Thinking: "on"},
		{Band: "sonnet", ModelID: "grok-4.6", Effort: "medium", Thinking: "on"},
		{Band: "haiku", ModelID: "grok-4.6", Effort: "low", Thinking: "on"},
	}
	if got := CandidateLadder("xai", ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("CandidateLadder(xai) = %+v, want %+v", got, want)
	}
}

func TestCandidateLadderPartialMultiBandProviders(t *testing.T) {
	wantGoogle := []EnvelopeRung{
		{Band: "fable", ModelID: "gemini-2.5-pro", Effort: "high"},
		{Band: "opus", ModelID: "gemini-2.5-pro", Effort: "medium"},
		{Band: "sonnet", ModelID: "gemini-2.5-flash", Effort: "high"},
		{Band: "haiku", ModelID: "gemini-2.5-flash", Effort: "medium"},
	}
	if got := CandidateLadder("google", ""); !reflect.DeepEqual(got, wantGoogle) {
		t.Fatalf("CandidateLadder(google) = %+v, want %+v", got, wantGoogle)
	}

	wantOpenAI := []EnvelopeRung{
		{Band: "fable", ModelID: "gpt-5.6-sol", Effort: "xhigh"},
		{Band: "opus", ModelID: "gpt-5.6-sol", Effort: "high"},
		{Band: "sonnet", ModelID: "gpt-5.6-terra"},
		{Band: "haiku", ModelID: "gpt-5.6-luna"},
	}
	if got := CandidateLadder("openai", ""); !reflect.DeepEqual(got, wantOpenAI) {
		t.Fatalf("CandidateLadder(openai) = %+v, want %+v", got, wantOpenAI)
	}
}

func TestCandidateLadderLocalProvidersEmpty(t *testing.T) {
	for _, provider := range []string{"ollama", "lm-studio", "other-unknown"} {
		if got := CandidateLadder(provider, ""); len(got) != 0 {
			t.Errorf("CandidateLadder(%q) = %+v, want empty", provider, got)
		}
	}
}

func TestCandidateLadderTransportFilterIsNoOpToday(t *testing.T) {
	// No current registry entry declares served:false on a banded model, so
	// the transport filter must be a no-op — absent facts pass through
	// (#579 AC4: unexpressed is never read as unserved).
	for _, provider := range []string{"anthropic", "openai", "google", "xai", "copilot"} {
		open := CandidateLadder(provider, "")
		cli := CandidateLadder(provider, "cli")
		if !reflect.DeepEqual(open, cli) {
			t.Errorf("CandidateLadder(%q, cli) diverged from unfiltered: %+v vs %+v", provider, cli, open)
		}
	}
}

func TestCandidateLadderOrderingIsDeclaredOrder(t *testing.T) {
	// Capability discipline (spike §4.3): ordering is exactly the declared
	// band order — nothing reorders it, because no capability fact exists to
	// reorder by, and inventing one is forbidden.
	for _, provider := range []string{"anthropic", "openai", "google", "xai", "copilot"} {
		got := CandidateLadder(provider, "")
		rank := -1
		for _, rung := range got {
			r := 0
			for i, b := range TierBandsStrongestFirst {
				if b == rung.Band {
					r = i
				}
			}
			if r <= rank {
				t.Errorf("CandidateLadder(%q): band %q out of declared order in %+v", provider, rung.Band, got)
			}
			rank = r
		}
	}
}

func TestResolveBandEnvelope(t *testing.T) {
	rung, ok := ResolveBandEnvelope("xai", "sonnet", "")
	if !ok {
		t.Fatal("ResolveBandEnvelope(xai, sonnet) not found")
	}
	want := EnvelopeRung{Band: "sonnet", ModelID: "grok-4.6", Effort: "medium", Thinking: "on"}
	if rung != want {
		t.Fatalf("ResolveBandEnvelope(xai, sonnet) = %+v, want %+v", rung, want)
	}

	if _, ok := ResolveBandEnvelope("ollama", "sonnet", ""); ok {
		t.Fatal("ResolveBandEnvelope(ollama, sonnet) resolved; local providers have no registry ladder")
	}
}

func TestEscalationLadderDerivation(t *testing.T) {
	// Membership from the registry, order from the band ladder, ceiling from
	// policy — must reproduce the pre-cutover [haiku sonnet opus] walk.
	want := []string{TierHaiku, TierSonnet, TierOpus}
	if got := EscalationLadder("anthropic"); !reflect.DeepEqual(got, want) {
		t.Fatalf("EscalationLadder(anthropic) = %v, want %v", got, want)
	}

	for _, provider := range []string{"anthropic", "openai", "google", "xai", "copilot"} {
		for _, band := range EscalationLadder(provider) {
			if band == TierFable {
				t.Errorf("EscalationLadder(%q) contains fable — the frontier tier is explicit-opt-in only", provider)
			}
		}
	}

	if got := EscalationLadder("ollama"); len(got) != 0 {
		t.Errorf("EscalationLadder(ollama) = %v, want empty", got)
	}
}

// TestBandRankSpaceEquivalentToRungOrder pins the structural fact that keeps
// the band-rank clamps (ClampToEnvelope / tierRank / stageBaseModel) CORRECT
// without a rung-native conversion (#606 deliverable 4): for every provider
// with a registry ladder, bands ↔ rungs are 1:1 (each band answers for at
// most one rung) and the rungs' band order is strictly descending in
// capability rank — so comparing two clamp inputs in band-rank space is
// equivalent to comparing their rung indices for every reachable case. The
// #606 same-model effort descent does not break this: its substituted value
// is still a BAND (the rung's Band), and the effort half rides beside the
// band clamps (StickyEffort applied after ClampEffortToEnvelope), never
// through them. If a registry change ever makes a band answer for two rungs
// — or reorders rungs against the band ladder — this fails, and the clamps
// must be converted to rung-native comparisons before that change lands.
func TestBandRankSpaceEquivalentToRungOrder(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "google", "xai", "copilot"} {
		rungs := CandidateLadder(provider, "")
		seen := map[string]bool{}
		prevRank := -1
		for i, rung := range rungs {
			if seen[rung.Band] {
				t.Fatalf("%s: band %q answers for two rungs — band-rank clamps are no longer equivalent to rung-native comparison", provider, rung.Band)
			}
			seen[rung.Band] = true
			rank := tierRank(rung.Band)
			if rank < 0 {
				t.Fatalf("%s: rung %d band %q is not in the band ladder", provider, i, rung.Band)
			}
			if prevRank != -1 && rank >= prevRank {
				t.Fatalf("%s: rung order diverges from band capability order at %q (rank %d after %d)", provider, rung.Band, rank, prevRank)
			}
			prevRank = rank
		}
	}
}
