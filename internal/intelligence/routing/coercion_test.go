package routing

import (
	"reflect"
	"testing"
)

func TestCoerceComplexityScore(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  int
	}{
		{"valid score 3", 3, 3},
		{"valid score 1", 1, 1},
		{"valid score 8", 8, 8},
		{"above max clamped to 8", 10, 8},
		{"below min clamped to 1", 0, 1},
		{"float 3.7 truncated to 3", float64(3.7), 3},
		{"float 10.0 clamped to 8", float64(10.0), 8},
		{"nil defaults to 3", nil, 3},
		{"string defaults to 3", "medium", 3},
		{"negative clamped to 1", -5, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceComplexityScore(tt.input)
			if got != tt.want {
				t.Errorf("coerceComplexityScore(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerceChangeType(t *testing.T) {
	tests := []struct {
		name   string
		input  interface{}
		labels []string
		want   string
	}{
		// Valid values pass through unchanged
		{"valid code", "code", nil, "code"},
		{"valid docs", "docs", nil, "docs"},
		{"valid config", "config", nil, "config"},
		// Aliases
		{"alias code_change", "code_change", nil, "code"},
		{"alias code_modification", "code_modification", nil, "code"},
		{"alias documentation", "documentation", nil, "docs"},
		{"alias doc", "doc", nil, "docs"},
		{"alias configuration", "configuration", nil, "config"},
		{"alias conf", "conf", nil, "config"},
		// Case insensitive
		{"uppercase DOCS", "DOCS", nil, "docs"},
		{"uppercase CODE", "CODE", nil, "code"},
		{"mixed case Code_Change", "Code_Change", nil, "code"},
		// Hyphen normalization
		{"hyphen code-change", "code-change", nil, "code"},
		// Label inference
		{"nil with docs label", nil, []string{"type:docs"}, "docs"},
		{"nil with no label defaults to code", nil, nil, "code"},
		{"unknown with config label", "unknown_type", []string{"configuration"}, "config"},
		// Unknown defaults to code
		{"unknown value", "feature_work", nil, "code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceChangeType(tt.input, tt.labels)
			if got != tt.want {
				t.Errorf("coerceChangeType(%v, %v) = %v, want %v", tt.input, tt.labels, got, tt.want)
			}
		})
	}
}

func TestCoerceSuggestedRoute(t *testing.T) {
	tests := []struct {
		name            string
		input           interface{}
		complexityScore int
		want            string
	}{
		// Valid values pass through unchanged
		{"valid trivial", "trivial", 1, "trivial"},
		{"valid standard", "standard", 3, "standard"},
		{"valid extensive", "extensive", 5, "extensive"},
		// Aliases
		{"alias trivial_route", "trivial_route", 1, "trivial"},
		{"alias quick", "quick", 1, "trivial"},
		{"alias simple", "simple", 1, "trivial"},
		{"alias extensive_route", "extensive_route", 5, "extensive"},
		{"alias complex", "complex", 5, "extensive"},
		{"alias deep", "deep", 5, "extensive"},
		// Case insensitive
		{"uppercase TRIVIAL", "TRIVIAL", 1, "trivial"},
		{"mixed case Standard", "Standard", 3, "standard"},
		// Fallback to complexity-based route
		{"nil score 1 → trivial", nil, 1, "trivial"},
		{"nil score 2 → trivial", nil, 2, "trivial"},
		{"nil score 3 → standard", nil, 3, "standard"},
		{"nil score 4 → standard", nil, 4, "standard"},
		{"nil score 5 → extensive", nil, 5, "extensive"},
		{"nil score 8 → extensive", nil, 8, "extensive"},
		{"unknown route score 3 → standard", "STANDARD_ROUTE", 3, "standard"},
		{"unknown route score 1 → trivial", "UNKNOWN", 1, "trivial"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceSuggestedRoute(tt.input, tt.complexityScore, false)
			if got != tt.want {
				t.Errorf("coerceSuggestedRoute(%v, %v) = %v, want %v", tt.input, tt.complexityScore, got, tt.want)
			}
		})
	}
}

func TestCoerceSkipStages(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  []string
	}{
		{"nil returns empty", nil, []string{}},
		{"empty array returns empty", []interface{}{}, []string{}},
		{"valid stages pass through", []interface{}{"feature-planning", "feature-validate"}, []string{"feature-planning", "feature-validate"}},
		{"invalid stage filtered out", []interface{}{"issue-pickup", "feature-validate"}, []string{"feature-validate"}},
		{"feature-dev filtered out (not skippable)", []interface{}{"feature-dev", "pr-create"}, []string{"pr-create"}},
		{"all valid stages", []interface{}{"feature-planning", "feature-validate", "pr-create", "pr-merge"}, []string{"feature-planning", "feature-validate", "pr-create", "pr-merge"}},
		{"mixed valid and invalid", []interface{}{"issue-pickup", "feature-validate", "unknown-stage"}, []string{"feature-validate"}},
		{"uppercase normalized", []interface{}{"FEATURE-PLANNING", "pr-create"}, []string{"feature-planning", "pr-create"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceSkipStages(tt.input, false)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("coerceSkipStages(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerceRouting(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		got := CoerceRouting(nil, nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("valid routing unchanged", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 3,
			"suggested_route":  "standard",
			"skip_stages":      []interface{}{},
			"rationale":        "M-size code change",
		}
		got := CoerceRouting(input, nil)
		if got["change_type"] != "code" {
			t.Errorf("change_type: got %v, want code", got["change_type"])
		}
		if got["complexity_score"] != 3 {
			t.Errorf("complexity_score: got %v, want 3", got["complexity_score"])
		}
		if got["suggested_route"] != "standard" {
			t.Errorf("suggested_route: got %v, want standard", got["suggested_route"])
		}
	})

	t.Run("coerces code_change to code", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code_change",
			"complexity_score": 3,
			"suggested_route":  "standard",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["change_type"] != "code" {
			t.Errorf("change_type: got %v, want code", got["change_type"])
		}
	})

	t.Run("complexity 10 clamped to 8", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 10,
			"suggested_route":  "extensive",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["complexity_score"] != 8 {
			t.Errorf("complexity_score: got %v, want 8", got["complexity_score"])
		}
	})

	t.Run("complexity 0 clamped to 1", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 0,
			"suggested_route":  "trivial",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["complexity_score"] != 1 {
			t.Errorf("complexity_score: got %v, want 1", got["complexity_score"])
		}
	})

	t.Run("quick route alias maps to trivial", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 1,
			"suggested_route":  "quick",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["suggested_route"] != "trivial" {
			t.Errorf("suggested_route: got %v, want trivial", got["suggested_route"])
		}
	})

	t.Run("invalid skip_stages filtered", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 3,
			"suggested_route":  "standard",
			"skip_stages":      []interface{}{"issue-pickup", "feature-validate"},
		}
		got := CoerceRouting(input, nil)
		stages, ok := got["skip_stages"].([]string)
		if !ok {
			t.Fatalf("skip_stages not []string: %T", got["skip_stages"])
		}
		if len(stages) != 1 || stages[0] != "feature-validate" {
			t.Errorf("skip_stages: got %v, want [feature-validate]", stages)
		}
	})

	t.Run("DOCS uppercase change_type normalized", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "DOCS",
			"complexity_score": 1,
			"suggested_route":  "trivial",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["change_type"] != "docs" {
			t.Errorf("change_type: got %v, want docs", got["change_type"])
		}
	})

	t.Run("invalid route recalculated from score=1 → trivial", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code",
			"complexity_score": 1,
			"suggested_route":  "INVALID_ROUTE",
			"skip_stages":      []interface{}{},
		}
		got := CoerceRouting(input, nil)
		if got["suggested_route"] != "trivial" {
			t.Errorf("suggested_route: got %v, want trivial", got["suggested_route"])
		}
	})

	t.Run("does not mutate input map", func(t *testing.T) {
		input := map[string]interface{}{
			"change_type":      "code_change",
			"complexity_score": 10,
			"suggested_route":  "standard",
			"skip_stages":      []interface{}{},
		}
		_ = CoerceRouting(input, nil)
		if input["change_type"] != "code_change" {
			t.Errorf("input mutated: change_type = %v", input["change_type"])
		}
		if input["complexity_score"] != 10 {
			t.Errorf("input mutated: complexity_score = %v", input["complexity_score"])
		}
	})
}
