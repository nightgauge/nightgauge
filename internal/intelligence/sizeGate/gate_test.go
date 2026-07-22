package sizeGate

import (
	"testing"
)

func TestDefaultGateConfig(t *testing.T) {
	cfg := DefaultGateConfig()
	if cfg.MaxLocInTitle != 5000 {
		t.Errorf("MaxLocInTitle = %d, want 5000", cfg.MaxLocInTitle)
	}
	if cfg.DecomposedItemsMin != 2 {
		t.Errorf("DecomposedItemsMin = %d, want 2", cfg.DecomposedItemsMin)
	}
	if !cfg.LocPatternEnabled {
		t.Error("LocPatternEnabled = false, want true")
	}
	if !cfg.DecompositionCheckEnabled {
		t.Error("DecompositionCheckEnabled = false, want true")
	}
	if !cfg.RejectOnOversized {
		t.Error("RejectOnOversized = false, want true")
	}
}

func TestGateEvaluator_LocPattern(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		maxLoc    int
		wantAllow bool
	}{
		{
			name:      "exceeds threshold — comma-formatted",
			title:     "Refactor auth system: 8,500 LOC to rewrite",
			maxLoc:    5000,
			wantAllow: false,
		},
		{
			name:      "exceeds threshold — no comma",
			title:     "Migrate payment service 6000 LOC",
			maxLoc:    5000,
			wantAllow: false,
		},
		{
			name:      "at threshold — just over",
			title:     "5,001 LOC cleanup task",
			maxLoc:    5000,
			wantAllow: false,
		},
		{
			name:      "exactly at threshold — allowed",
			title:     "5,000 LOC refactor",
			maxLoc:    5000,
			wantAllow: true,
		},
		{
			name:      "below threshold — allowed",
			title:     "4,999 LOC feature implementation",
			maxLoc:    5000,
			wantAllow: true,
		},
		{
			name:      "small issue — no LOC reference",
			title:     "Add login modal button",
			maxLoc:    5000,
			wantAllow: true,
		},
		{
			name:      "no LOC reference at all",
			title:     "Fix null pointer in UserService",
			maxLoc:    5000,
			wantAllow: true,
		},
		{
			name:      "LOC lowercase — case insensitive",
			title:     "Huge 10,000 loc migration",
			maxLoc:    5000,
			wantAllow: false,
		},
		{
			name:      "custom threshold — small",
			title:     "Refactor 1000 LOC module",
			maxLoc:    500,
			wantAllow: false,
		},
		{
			name:      "custom threshold — passes",
			title:     "Refactor 400 LOC module",
			maxLoc:    500,
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultGateConfig()
			cfg.MaxLocInTitle = tt.maxLoc
			cfg.DecompositionCheckEnabled = false // isolate LOC heuristic

			g := NewGateEvaluator(cfg)
			result := g.Evaluate(tt.title, nil, 0)

			if result.Allowed != tt.wantAllow {
				t.Errorf("Evaluate(%q) Allowed = %v, want %v (reason: %q)",
					tt.title, result.Allowed, tt.wantAllow, result.Reason)
			}

			if !result.Allowed {
				if result.Reason == "" {
					t.Error("rejected result has empty Reason")
				}
				if result.Severity == "" {
					t.Error("rejected result has empty Severity")
				}
				if result.SuggestedAction == "" {
					t.Error("rejected result has empty SuggestedAction")
				}
				found := false
				for _, h := range result.HeuristicsApplied {
					if h == "loc-in-title" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("HeuristicsApplied does not contain 'loc-in-title': %v", result.HeuristicsApplied)
				}
			}
		})
	}
}

func TestGateEvaluator_Decomposition(t *testing.T) {
	tests := []struct {
		name           string
		labels         []string
		subIssuesCount int
		minRequired    int
		wantAllow      bool
		wantHeuristic  string
	}{
		{
			name:           "size:L with no sub-issues — rejected",
			labels:         []string{"size:L", "priority:high"},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      false,
			wantHeuristic:  "size-without-decomposition",
		},
		{
			name:           "size:L with 1 sub-issue — rejected (below min)",
			labels:         []string{"size:L"},
			subIssuesCount: 1,
			minRequired:    2,
			wantAllow:      false,
		},
		{
			name:           "size:L with exactly min sub-issues — allowed",
			labels:         []string{"size:L"},
			subIssuesCount: 2,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "size:L with more than min — allowed",
			labels:         []string{"size:L"},
			subIssuesCount: 5,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "size:XL with no sub-issues — rejected",
			labels:         []string{"size:XL"},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      false,
		},
		{
			name:           "size:XL with sufficient sub-issues — allowed",
			labels:         []string{"size:XL"},
			subIssuesCount: 3,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "size:M with no sub-issues — allowed (no requirement)",
			labels:         []string{"size:M"},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "size:S — allowed",
			labels:         []string{"size:S"},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "no labels — allowed",
			labels:         []string{},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      true,
		},
		{
			name:           "both size:L and size:XL labels — XL takes precedence",
			labels:         []string{"size:L", "size:XL"},
			subIssuesCount: 0,
			minRequired:    2,
			wantAllow:      false,
		},
		{
			name:           "custom min=1 — size:L with 1 sub-issue passes",
			labels:         []string{"size:L"},
			subIssuesCount: 1,
			minRequired:    1,
			wantAllow:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultGateConfig()
			cfg.DecomposedItemsMin = tt.minRequired
			cfg.LocPatternEnabled = false // isolate decomposition heuristic

			g := NewGateEvaluator(cfg)
			result := g.Evaluate("Normal issue title", tt.labels, tt.subIssuesCount)

			if result.Allowed != tt.wantAllow {
				t.Errorf("Evaluate(labels=%v, subIssues=%d) Allowed = %v, want %v (reason: %q)",
					tt.labels, tt.subIssuesCount, result.Allowed, tt.wantAllow, result.Reason)
			}

			if !result.Allowed && tt.wantHeuristic != "" {
				found := false
				for _, h := range result.HeuristicsApplied {
					if h == tt.wantHeuristic {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("HeuristicsApplied does not contain %q: %v", tt.wantHeuristic, result.HeuristicsApplied)
				}
			}
		})
	}
}

func TestGateEvaluator_Combined(t *testing.T) {
	tests := []struct {
		name           string
		title          string
		labels         []string
		subIssuesCount int
		wantAllow      bool
	}{
		{
			name:           "normal issue — passes all heuristics",
			title:          "Add user profile page",
			labels:         []string{"size:M", "type:feature"},
			subIssuesCount: 0,
			wantAllow:      true,
		},
		{
			name:           "oversized LOC + size:XL + no decomposition — LOC heuristic fires first",
			title:          "10,000 LOC payment system redesign",
			labels:         []string{"size:XL"},
			subIssuesCount: 0,
			wantAllow:      false,
		},
		{
			name:           "no LOC + size:L + no decomposition — decomposition heuristic fires",
			title:          "Redesign billing module",
			labels:         []string{"size:L"},
			subIssuesCount: 0,
			wantAllow:      false,
		},
		{
			name:           "valid LOC + size:L + sufficient decomposition — passes",
			title:          "Refactor auth: 3,000 LOC",
			labels:         []string{"size:L"},
			subIssuesCount: 3,
			wantAllow:      true,
		},
		{
			name:           "oversized LOC with sufficient decomposition — LOC still rejects",
			title:          "8,000 LOC migration",
			labels:         []string{"size:XL"},
			subIssuesCount: 5,
			wantAllow:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGateEvaluator(DefaultGateConfig())
			result := g.Evaluate(tt.title, tt.labels, tt.subIssuesCount)

			if result.Allowed != tt.wantAllow {
				t.Errorf("Evaluate(%q, labels=%v, subIssues=%d) Allowed = %v, want %v (reason: %q)",
					tt.title, tt.labels, tt.subIssuesCount, result.Allowed, tt.wantAllow, result.Reason)
			}
		})
	}
}

func TestGateEvaluator_DisabledHeuristics(t *testing.T) {
	t.Run("both heuristics disabled — always passes", func(t *testing.T) {
		cfg := DefaultGateConfig()
		cfg.LocPatternEnabled = false
		cfg.DecompositionCheckEnabled = false

		g := NewGateEvaluator(cfg)
		result := g.Evaluate("10,000 LOC giant feature", []string{"size:XL"}, 0)

		if !result.Allowed {
			t.Errorf("expected Allowed=true with all heuristics disabled, got false (reason: %q)", result.Reason)
		}
		if len(result.HeuristicsApplied) != 0 {
			t.Errorf("expected empty HeuristicsApplied, got %v", result.HeuristicsApplied)
		}
	})
}

func TestGateEvaluator_AllowedResultHasNoReason(t *testing.T) {
	g := NewGateEvaluator(DefaultGateConfig())
	result := g.Evaluate("Add login button", []string{"size:S"}, 0)

	if !result.Allowed {
		t.Fatal("expected Allowed=true")
	}
	if result.Reason != "" {
		t.Errorf("allowed result has non-empty Reason: %q", result.Reason)
	}
	if result.SuggestedAction != "" {
		t.Errorf("allowed result has non-empty SuggestedAction: %q", result.SuggestedAction)
	}
	if result.Severity != "" {
		t.Errorf("allowed result has non-empty Severity: %q", result.Severity)
	}
}
