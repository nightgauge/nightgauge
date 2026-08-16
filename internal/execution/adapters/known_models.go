package adapters

import "github.com/nightgauge/nightgauge/internal/models"

// knownTransportServedModels filters all to the CLOSED set for provider that
// are also reachable through transport (#579, #600): non-deprecated entries
// whose transports[transport].served fact is not explicitly false. A model
// with no declared fact for transport (unexpressed/pending) still counts as
// known — additive enforcement, #579 AC4.
//
// Shared by knownCodexModels/knownGeminiModels/knownGrokModels (the CLOSED
// adapters whose preflight builds a "valid models" suggestion list, #600
// AC3) rather than each keeping its own copy of the same filter loop. Takes
// the model slice as a parameter (not reading models.All() directly) so the
// served:false exclusion branch is unit-testable against synthetic data: no
// current codex/gemini registry entry has an explicit served:false fact for
// its own provider+transport pair (only xai's grok-build-0.1 does, and that
// is already covered directly against the live registry by
// TestValidateGrokModel/TestGrokBuild01UnselectableForBothReasonsIndependently).
func knownTransportServedModels(all []models.ModelDescriptor, provider, transport string) map[string]bool {
	known := make(map[string]bool)
	for _, m := range all {
		if m.Provider != provider || m.Deprecated {
			continue
		}
		if served, knownFact := m.ServedByTransport(transport); knownFact && !served {
			continue
		}
		known[m.ID] = true
	}
	return known
}
