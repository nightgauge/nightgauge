package adapters

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

// TestKnownTransportServedModels pins the shared filter (#600 AC3) that
// backs knownCodexModels/knownGeminiModels/knownGrokModels: provider
// membership, non-deprecated status, AND transport-served status (or
// unexpressed/pending, additive #579 AC4) all gate inclusion. Exercised
// against a SYNTHETIC slice, not models.All(), because no current
// codex/gemini registry entry has an explicit served:false fact for its own
// provider+transport pair to exercise that branch against live data.
func TestKnownTransportServedModels(t *testing.T) {
	synthetic := []models.ModelDescriptor{
		{ID: "served", Provider: "openai", Transports: map[string]models.TransportFacts{
			models.TransportCLI: {Served: true},
		}},
		{ID: "unserved", Provider: "openai", Transports: map[string]models.TransportFacts{
			models.TransportCLI: {Served: false},
		}},
		{ID: "unexpressed", Provider: "openai"}, // no transports block at all — additive fail-open
		{ID: "deprecated-but-served", Provider: "openai", Deprecated: true, Transports: map[string]models.TransportFacts{
			models.TransportCLI: {Served: true},
		}},
		{ID: "other-provider", Provider: "google", Transports: map[string]models.TransportFacts{
			models.TransportCLI: {Served: true},
		}},
	}

	got := knownTransportServedModels(synthetic, "openai", models.TransportCLI)

	if !got["served"] {
		t.Error(`known["served"] = false, want true`)
	}
	if got["unserved"] {
		t.Error(`known["unserved"] = true, want false — transports.cli.served is explicitly false`)
	}
	if !got["unexpressed"] {
		t.Error(`known["unexpressed"] = false, want true — no transports fact must fail OPEN (#579 AC4)`)
	}
	if got["deprecated-but-served"] {
		t.Error(`known["deprecated-but-served"] = true, want false — deprecated entries are excluded regardless of transport`)
	}
	if got["other-provider"] {
		t.Error(`known["other-provider"] = true, want false — provider filter must exclude it`)
	}
	if len(got) != 2 {
		t.Errorf("knownTransportServedModels returned %d entries, want exactly 2 (served, unexpressed): %v", len(got), got)
	}
}

// TestKnownCodexModelsExcludesUnservedByDefault is the live-registry half of
// #600 AC3 for codex: knownCodexModels (feeding ValidateCodexModel's
// "valid models" remediation text) now filters through
// knownTransportServedModels/codexTransport instead of a bare
// provider+deprecated filter — the same served-filtering GEMINI_MODELS and
// GROK_MODELS already had in the SDK's modelPreflight.ts before #600.
func TestKnownCodexModelsExcludesUnservedByDefault(t *testing.T) {
	known := knownCodexModels()
	if len(known) == 0 {
		t.Fatal("knownCodexModels() returned no models")
	}
	transport := codexTransport()
	for id := range known {
		m, ok := models.Resolve("openai", id)
		if !ok {
			t.Errorf("knownCodexModels() returned %q, not resolvable via Resolve", id)
			continue
		}
		if served, knownFact := m.ServedByTransport(transport); knownFact && !served {
			t.Errorf("knownCodexModels() included %q, which explicitly declares transports.%s.served=false",
				id, transport)
		}
	}
}
