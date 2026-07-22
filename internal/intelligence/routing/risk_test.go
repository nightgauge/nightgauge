package routing

import (
	"reflect"
	"testing"
)

func TestIsHighRisk(t *testing.T) {
	tests := []struct {
		name        string
		labels      []string
		wantHigh    bool
		wantReasons []string
	}{
		{"no labels", nil, false, []string{}},
		{"benign labels", []string{"type:feature", "size:M", "priority:high"}, false, []string{}},
		{"security component", []string{"type:feature", "component:security"}, true, []string{"component:security"}},
		{"billing component", []string{"component:billing"}, true, []string{"component:billing"}},
		{"migration substring", []string{"area:migration-tooling"}, true, []string{"area:migration-tooling"}},
		{"public-api", []string{"public-api"}, true, []string{"public-api"}},
		{"explicit escape hatch", []string{"risk:high"}, true, []string{"risk:high"}},
		{"escape hatch hyphen variant", []string{"risk-high"}, true, []string{"risk-high"}},
		{"case insensitive", []string{"COMPONENT:Security"}, true, []string{"component:security"}},
		{"dedup distinct slugs", []string{"component:security", "security-audit"}, true, []string{"component:security", "security-audit"}},
		{"same slug twice → single reason", []string{"component:billing", "component:billing"}, true, []string{"component:billing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			high, reasons := isHighRisk(tt.labels)
			if high != tt.wantHigh {
				t.Errorf("isHighRisk(%v) high = %v, want %v", tt.labels, high, tt.wantHigh)
			}
			if !reflect.DeepEqual(reasons, tt.wantReasons) {
				t.Errorf("isHighRisk(%v) reasons = %v, want %v", tt.labels, reasons, tt.wantReasons)
			}
		})
	}
}

// TestRiskFloorInvariant_Derive asserts the named RISK_FLOOR invariant (#4093):
// a high-risk classification forces the extensive route and skips no stages,
// even for a trivially small (complexity ≤ 2) change that would otherwise route
// trivial and skip feature-planning + feature-validate.
func TestRiskFloorInvariant_Derive(t *testing.T) {
	// Baseline: tiny code change with no risk label → trivial + skips.
	baseline := Derive(DeriveInput{
		Title:     "tweak copy",
		Labels:    []string{"type:feature", "size:XS"},
		BoardSize: "XS",
	})
	if baseline.SuggestedRoute != "trivial" {
		t.Fatalf("precondition: baseline route = %q, want trivial", baseline.SuggestedRoute)
	}
	if len(baseline.SkipStages) == 0 {
		t.Fatalf("precondition: baseline should skip stages, got none")
	}
	if baseline.RiskHigh {
		t.Fatalf("precondition: baseline must not be high-risk")
	}

	// Same tiny change, now touching a high-risk area → forced extensive, no skips.
	highRisk := Derive(DeriveInput{
		Title:     "tweak copy",
		Labels:    []string{"type:feature", "size:XS", "component:security"},
		BoardSize: "XS",
	})
	if !highRisk.RiskHigh {
		t.Errorf("RiskHigh = false, want true for component:security")
	}
	if highRisk.SuggestedRoute != "extensive" {
		t.Errorf("SuggestedRoute = %q, want extensive (risk floor)", highRisk.SuggestedRoute)
	}
	if len(highRisk.SkipStages) != 0 {
		t.Errorf("SkipStages = %v, want [] (full pipeline forced)", highRisk.SkipStages)
	}
	// feature-validate must never be skipped on a high-risk issue — the gates
	// in #4097/#4099 hang off it.
	for _, s := range highRisk.SkipStages {
		if s == "feature-validate" || s == "feature-planning" {
			t.Errorf("high-risk skipped %q; the full pipeline must run", s)
		}
	}
	if len(highRisk.RiskReasons) == 0 {
		t.Errorf("RiskReasons empty, want the matched label slug")
	}
}

// TestRiskFloorParity asserts both routing derivation paths — Derive() and the
// defensive read-time CoerceRouting() — yield the SAME non-skipping, extensive
// decision for a high-risk + low-complexity input. A risk override in one path
// that the other silently re-trimmed would be a runtime divergence (#4093).
func TestRiskFloorParity(t *testing.T) {
	labels := []string{"type:feature", "size:XS", "component:billing"}

	derived := Derive(DeriveInput{Title: "small billing tweak", Labels: labels, BoardSize: "XS"})

	// CoerceRouting starts from a persisted map that was computed WITHOUT the
	// risk signal (trivial route, skipping both stages) — it must be floored.
	coerced := CoerceRouting(map[string]interface{}{
		"change_type":      "code",
		"complexity_score": 1,
		"suggested_route":  "trivial",
		"skip_stages":      []interface{}{"feature-planning", "feature-validate"},
	}, labels)

	if derived.SuggestedRoute != "extensive" {
		t.Errorf("Derive route = %q, want extensive", derived.SuggestedRoute)
	}
	if coerced["suggested_route"] != "extensive" {
		t.Errorf("Coerce route = %v, want extensive", coerced["suggested_route"])
	}

	coercedSkips, _ := coerced["skip_stages"].([]string)
	if len(derived.SkipStages) != 0 || len(coercedSkips) != 0 {
		t.Errorf("parity broken: Derive skips=%v, Coerce skips=%v, want both empty", derived.SkipStages, coercedSkips)
	}

	if coerced["risk_high"] != true {
		t.Errorf("Coerce risk_high = %v, want true", coerced["risk_high"])
	}
}
