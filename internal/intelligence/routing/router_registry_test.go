package routing

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

// Regression guard for #50: routing model IDs (and therefore the scheduler's
// pr-create large-diff and feature-validate haiku-disable escalation targets,
// which dispatch ModelSonnet) must always resolve to current, non-deprecated
// registry models. A model rotation that deprecates a dated ID must never
// strand routing on it again.
func TestRoutingModelsAreCurrentRegistryModels(t *testing.T) {
	cases := []struct {
		tier string
		id   string
	}{
		{"haiku", ModelHaiku},
		{"sonnet", ModelSonnet},
		{"opus", ModelOpus},
		{"fable", ModelFable},
	}
	for _, c := range cases {
		m, ok := models.Get(c.id)
		if !ok {
			t.Errorf("Model%s = %q is not in the model registry", c.tier, c.id)
			continue
		}
		if m.Deprecated {
			t.Errorf("Model%s = %q is marked deprecated in the registry", c.tier, c.id)
		}
		if !m.HasTier(c.tier) {
			t.Errorf("Model%s = %q has registry tiers %v, want to include %q", c.tier, c.id, m.Tiers, c.tier)
		}
	}
}

func TestModelPricingReadsRegistryRates(t *testing.T) {
	for _, tier := range []string{"haiku", "sonnet", "opus", "fable"} {
		m, ok := models.Get(tier)
		if !ok {
			t.Fatalf("registry has no current model for tier %q", tier)
		}
		in, out := modelPricing(m.ID)
		if in != m.Rates.Input || out != m.Rates.Output {
			t.Errorf("modelPricing(%q) = %v/%v, want registry rates %v/%v",
				m.ID, in, out, m.Rates.Input, m.Rates.Output)
		}
	}

	// Unknown models price at a truthful $0 (user-configured local models),
	// never a fabricated tier default (#56).
	in, out := modelPricing("some-unknown-model")
	if in != 0 || out != 0 {
		t.Errorf("modelPricing(unknown) = %v/%v, want 0/0", in, out)
	}
}
