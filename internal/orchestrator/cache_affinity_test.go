package orchestrator

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type affinityFixture struct {
	Rates map[string]AffinityRates `json:"rates"`
	Cases []struct {
		Name  string `json:"name"`
		Input struct {
			Stage            string `json:"stage"`
			Candidate        string `json:"candidate"`
			Previous         string `json:"previous"`
			SameProvider     bool   `json:"same_provider"`
			PrevWithinExpiry bool   `json:"prev_within_expiry"`
			ImplicitCache    bool   `json:"implicit_cache"`
			WarmPrefixTokens int    `json:"warm_prefix_tokens"`
		} `json:"input"`
		Want struct {
			Model      string  `json:"model"`
			Kept       bool    `json:"kept"`
			PenaltyUSD float64 `json:"penalty_usd"`
			SavingUSD  float64 `json:"saving_usd"`
			Reason     string  `json:"reason"`
		} `json:"want"`
	} `json:"cases"`
}

// TestApplyCacheAffinity_SharedFixture runs the fixture the TS mirror runs.
func TestApplyCacheAffinity_SharedFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/cache_affinity_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx affinityFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := ApplyCacheAffinity(AffinityInput{
				Stage:            c.Input.Stage,
				Candidate:        c.Input.Candidate,
				Previous:         c.Input.Previous,
				SameProvider:     c.Input.SameProvider,
				PrevWithinExpiry: c.Input.PrevWithinExpiry,
				ImplicitCache:    c.Input.ImplicitCache,
				WarmPrefixTokens: c.Input.WarmPrefixTokens,
				Rates: func(m string) (AffinityRates, bool) {
					r, ok := fx.Rates[m]
					return r, ok
				},
			})
			w := c.Want
			if got.Model != w.Model || got.Kept != w.Kept || got.Reason != w.Reason ||
				math.Abs(got.PenaltyUSD-w.PenaltyUSD) > 1e-9 || math.Abs(got.SavingUSD-w.SavingUSD) > 1e-9 {
				t.Errorf("ApplyCacheAffinity = %+v, want %+v", got, w)
			}
		})
	}
}
