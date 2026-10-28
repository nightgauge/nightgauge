package routing

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ladderBands(rungs []EnvelopeRung) []string {
	out := make([]string, len(rungs))
	for i, r := range rungs {
		out[i] = r.Band + ":" + r.ModelID + "@" + r.Effort
	}
	return out
}

func TestCandidateLadder_ZeroExclusionsIsIdentity(t *testing.T) {
	for _, p := range []string{"anthropic", "xai", "openai", "google", "ollama"} {
		got := ladderBands(CandidateLadderExcluding(p, "", Exclusions{}))
		want := ladderBands(CandidateLadder(p, ""))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: zero exclusions changed the ladder: %v vs %v", p, got, want)
		}
	}
}

func TestCandidateLadder_ExcludedModels(t *testing.T) {
	rungs := CandidateLadderExcluding("anthropic", "", Exclusions{Models: []string{"claude-fable-5-1"}})
	if len(rungs) == 0 || rungs[0].ModelID != "claude-opus-5-5" {
		t.Fatalf("strongest rung with Fable excluded = %v, want claude-opus-5-5", ladderBands(rungs))
	}
	for _, r := range rungs {
		if r.ModelID == "claude-fable-5-1" {
			t.Fatalf("excluded model still on the ladder: %v", ladderBands(rungs))
		}
	}
}

func TestCandidateLadder_ExcludedEffortsDescend(t *testing.T) {
	rungs := CandidateLadderExcluding("xai", "", Exclusions{Efforts: []string{"xhigh", "max"}})
	if len(rungs) == 0 || rungs[0].Effort != "high" {
		t.Fatalf("xai top effort with xhigh,max excluded = %v, want high", ladderBands(rungs))
	}
	for _, r := range rungs {
		if r.Effort == "xhigh" || r.Effort == "max" {
			t.Fatalf("excluded effort still on the ladder: %v", ladderBands(rungs))
		}
	}
}

func TestCandidateLadder_AllEffortsExcludedIsEmpty(t *testing.T) {
	all := Exclusions{Efforts: []string{"low", "medium", "high", "xhigh", "max"}}
	if rungs := CandidateLadderExcluding("xai", "", all); len(rungs) != 0 {
		t.Fatalf("xai: every effort excluded but ladder = %v", ladderBands(rungs))
	}
	if _, ok := EligibleBand("xai", TierOpus, all); ok {
		t.Fatal("xai: every effort excluded must leave no eligible band")
	}
	// A model with no effort axis is untouched by excluded efforts.
	for _, r := range CandidateLadderExcluding("anthropic", "", all) {
		if r.Effort != "" {
			t.Fatalf("anthropic: rung with an excluded effort survived: %v", r)
		}
	}
}

func TestEligibleBand(t *testing.T) {
	fable := Exclusions{Models: []string{"claude-fable-5-1"}}
	if b, ok := EligibleBand("anthropic", TierFable, fable); !ok || b != TierOpus {
		t.Fatalf("EligibleBand(fable, fable excluded) = %q,%v; want opus", b, ok)
	}
	if b, ok := EligibleBand("anthropic", TierSonnet, fable); !ok || b != TierSonnet {
		t.Fatalf("EligibleBand(sonnet) = %q,%v; want sonnet untouched", b, ok)
	}
	// Never widens: haiku excluded leaves nothing at or below haiku.
	haiku := Exclusions{Models: []string{"claude-haiku-4-5"}}
	if _, ok := resolveProviderModel("anthropic", TierHaiku); ok {
		if desc, _ := resolveProviderModel("anthropic", TierHaiku); desc.ID == "claude-haiku-4-5" {
			if b, ok := EligibleBand("anthropic", TierHaiku, haiku); ok {
				t.Fatalf("EligibleBand(haiku, haiku excluded) = %q, want none (never widen)", b)
			}
		}
	}
	if b, ok := EligibleBand("anthropic", "my-local-model", fable); !ok || b != "my-local-model" {
		t.Fatalf("non-band model must pass through, got %q,%v", b, ok)
	}
}

func TestApplyExclusions_FloorAndCeilingLower(t *testing.T) {
	env := ModeEnvelope{Floor: TierFable, Ceiling: TierFable}
	got := ApplyExclusions(env, "anthropic", Exclusions{Models: []string{"claude-fable-5-1"}})
	if got.Floor != TierOpus || got.Ceiling != TierOpus {
		t.Fatalf("ApplyExclusions = %+v, want floor/ceiling opus", got)
	}
	if same := ApplyExclusions(env, "anthropic", Exclusions{}); same != env {
		t.Fatalf("zero exclusions changed envelope: %+v", same)
	}
}

func TestEligibleEffort(t *testing.T) {
	ex := Exclusions{Efforts: []string{"xhigh", "max"}}
	if got := EligibleEffort("xai", TierOpus, "xhigh", ex); got == "xhigh" || got == "max" || got == "" {
		t.Fatalf("EligibleEffort(xhigh excluded) = %q", got)
	}
	if got := EligibleEffort("anthropic", TierOpus, "medium", ex); got != "medium" {
		t.Fatalf("non-excluded effort changed: %q", got)
	}
}

func TestRoutedTierEnvelopeForWorkspace_AppliesExclusions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "model_routing:\n  excluded_models: [claude-fable-5-1]\n"
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RoutedTierEnvelopeForWorkspace(root, ModeFrontier, "feature-dev").Ceiling; got != TierOpus {
		t.Fatalf("frontier feature-dev ceiling with Fable excluded = %q, want opus", got)
	}
	if got := RoutedTierEnvelopeForWorkspace(t.TempDir(), ModeFrontier, "feature-dev").Ceiling; got != TierFable {
		t.Fatalf("frontier feature-dev ceiling without exclusions = %q, want fable", got)
	}
}

func TestNoEligibleRungError_NamesEverything(t *testing.T) {
	var err error = &NoEligibleRungError{Stage: "feature-dev", Provider: "anthropic",
		ExcludedModels: []string{"claude-fable-5-1"}, ExcludedEfforts: []string{"low", "max"}}
	var target *NoEligibleRungError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed")
	}
	for _, want := range []string{"feature-dev", "anthropic", "claude-fable-5-1", "low, max"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestEscalationLadderExcluding(t *testing.T) {
	ex := Exclusions{Models: []string{"claude-opus-5-5"}}
	for _, b := range EscalationLadderExcluding("anthropic", ex) {
		if b == TierOpus {
			t.Fatalf("excluded opus still an escalation target")
		}
	}
	if strings.Join(EscalationLadderExcluding("anthropic", Exclusions{}), ",") != strings.Join(EscalationLadder("anthropic"), ",") {
		t.Fatal("zero exclusions changed escalation ladder")
	}
}

func TestAdviseBand_ExcludedTopEntryNeverWins(t *testing.T) {
	advice := RoutingAdvice{Entries: []RoutingAdviceEntry{
		{JobClass: "bugfix", ModelID: "claude-opus-5-5", Backoff: "exact", Advisable: true, MeanQuality: 0.99, MeanCostUsd: 2},
		{JobClass: "bugfix", ModelID: "claude-sonnet-4-6", Backoff: "exact", Advisable: true, MeanQuality: 0.8, MeanCostUsd: 1},
	}}
	if TierBand("claude-sonnet-4-6") == "" {
		t.Skip("registry has no claude-sonnet-4-6")
	}
	if got := AdviseBand(advice, "bugfix", ModeMaximum, Envelope(ModeMaximum), Exclusions{Models: []string{"claude-opus-5-5"}}); got == TierOpus {
		t.Fatalf("excluded top-quality entry won: %q", got)
	}
	if got := AdviseBand(advice, "bugfix", ModeMaximum, Envelope(ModeMaximum), Exclusions{}); got != TierOpus {
		t.Fatalf("without exclusions want opus, got %q", got)
	}
}
