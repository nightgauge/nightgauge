package sizeGate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// ─── Capacity-aware sizing (#1655) ──────────────────────────────────────────

// Capacity-aware sizing (#1655). These pin the ADR 023 capacity table as the
// gate reads it: changing the table so that a 32k window admits M turns
// TestCapacityGate_WindowAgainstSize red.

// capacityLog collects the lines the gate logs.
type capacityLog struct{ lines []string }

func (c *capacityLog) logf(format string, args ...any) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func evaluateWithCapacity(t *testing.T, cfg GateConfig, in GateInput) (*GateResult, *capacityLog) {
	t.Helper()
	logs := &capacityLog{}
	return NewGateEvaluator(cfg).WithLogger(logs.logf).EvaluateIssue(in), logs
}

func TestCapacityGate_WindowAgainstSize(t *testing.T) {
	tests := []struct {
		name      string
		window    int
		label     string
		wantAllow bool
		wantCap   string
	}{
		{"32k rejects M", 32768, "size:M", false, "S"},
		{"32k admits S", 32768, "size:S", true, "S"},
		{"32k admits XS", 32768, "size:XS", true, "S"},
		{"131k admits M", 131072, "size:M", true, "M"},
		{"131k rejects L", 131072, "size:L", false, "M"},
		{"200k admits L", 200000, "size:L", true, "L"},
		{"8k rejects S", 8192, "size:S", false, "XS"},
		{"1M admits XL", 1000000, "size:XL", true, "XL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultGateConfig()
			// Sub-issues satisfy the L/XL decomposition heuristic so only
			// the capacity check can reject.
			res, _ := evaluateWithCapacity(t, cfg, GateInput{
				Title: "Some work", Labels: []string{tt.label}, SubIssues: 3,
				Capacity: &CapacityInput{Window: tt.window},
			})
			if res.Allowed != tt.wantAllow {
				t.Fatalf("Allowed = %v, want %v (reason %q)", res.Allowed, tt.wantAllow, res.Reason)
			}
			if res.Capacity == nil || res.Capacity.MaxSize != tt.wantCap {
				t.Fatalf("capacity = %+v, want cap %s", res.Capacity, tt.wantCap)
			}
			if !tt.wantAllow {
				size := strings.TrimPrefix(tt.label, "size:")
				for _, want := range []string{"size " + size, fmt.Sprint(tt.window), "cap " + tt.wantCap, RecoveryDecompose} {
					if !strings.Contains(res.Reason, want) {
						t.Errorf("reason %q does not name %q", res.Reason, want)
					}
				}
				if !reflect.DeepEqual(res.HeuristicsApplied, []string{"capacity"}) {
					t.Errorf("HeuristicsApplied = %v, want [capacity]", res.HeuristicsApplied)
				}
			}
		})
	}
}

// Without the capacity input the gate is exactly the pre-#1655 gate: each
// case's verdict, reason and heuristics are pinned to the values it produced
// before capacity existed, and no capacity verdict or log line appears.
func TestCapacityGate_NoCapacityInputIsUnchanged(t *testing.T) {
	cases := []struct {
		in         GateInput
		allowed    bool
		reason     string
		heuristics []string
	}{
		{GateInput{Title: "Add login button", Labels: []string{"size:M"}}, true, "", []string{}},
		{GateInput{Title: "Refactor 8,500 LOC", Labels: []string{"size:S"}}, false,
			"issue title references 8500 LOC (threshold: 5000) — issue is too large for a single pipeline run",
			[]string{"loc-in-title"}},
		{GateInput{Title: "Big work", Labels: []string{"size:L"}}, false,
			"size:L issue has 0 sub-issue(s) but requires at least 2 for pipeline processing",
			[]string{"size-without-decomposition"}},
		{GateInput{Title: "Big work", Labels: []string{"size:XL"}, SubIssues: 2}, true, "", []string{}},
		{GateInput{Title: "No size", Body: "<!-- " + CapacityDecomposedMarker + " -->", Size: "XL"}, true, "", []string{}},
	}
	for _, c := range cases {
		got, logs := evaluateWithCapacity(t, DefaultGateConfig(), c.in)
		if got.Allowed != c.allowed || got.Reason != c.reason || !reflect.DeepEqual(got.HeuristicsApplied, c.heuristics) {
			t.Errorf("%+v: got allowed=%v reason=%q heuristics=%v, want %v %q %v",
				c.in, got.Allowed, got.Reason, got.HeuristicsApplied, c.allowed, c.reason, c.heuristics)
		}
		if got.Capacity != nil || got.RoutedModel != "" || len(logs.lines) != 0 {
			t.Errorf("%+v: capacity ran without being requested: %+v %q %v", c.in, got.Capacity, got.RoutedModel, logs.lines)
		}
	}
}

// The table's row boundaries, each side of every threshold.
func TestMaxSizeForWindow_Boundaries(t *testing.T) {
	for _, tc := range []struct {
		window int
		want   string
		known  bool
	}{
		{-1, "", false}, {0, "", false}, {1, "XS", true},
		{31999, "XS", true}, {32000, "S", true}, {32768, "S", true},
		{127999, "S", true}, {128000, "M", true}, {131072, "M", true},
		{199999, "M", true}, {200000, "L", true},
		{399999, "L", true}, {400000, "XL", true}, {1000000, "XL", true},
	} {
		got, known := MaxSizeForWindow(tc.window)
		if got != tc.want || known != tc.known {
			t.Errorf("MaxSizeForWindow(%d) = (%q, %v), want (%q, %v)", tc.window, got, known, tc.want, tc.known)
		}
	}
}

func TestCapacityGate_UnknownSizeAppliesNoCapAndLogsOnce(t *testing.T) {
	res, logs := evaluateWithCapacity(t, DefaultGateConfig(), GateInput{
		Title: "Unsized work", Capacity: &CapacityInput{Window: 32768},
	})
	if !res.Allowed {
		t.Fatalf("an unknown size must be allowed, got %q", res.Reason)
	}
	if len(logs.lines) != 1 || !strings.HasPrefix(logs.lines[0], "capacity: size unknown") {
		t.Fatalf("log = %q, want exactly one line starting %q", logs.lines, "capacity: size unknown")
	}
}

// An unknown window applies no cap: size:L with no sub-issues is judged by
// the existing L/XL heuristic alone.
func TestCapacityGate_UnknownWindowFallsToTheLargeHeuristic(t *testing.T) {
	res, logs := evaluateWithCapacity(t, DefaultGateConfig(), GateInput{
		Title: "Large work", Labels: []string{"size:L"}, Capacity: &CapacityInput{Window: 0},
	})
	if res.Allowed {
		t.Fatal("size:L with no sub-issues must still be rejected by the L/XL heuristic")
	}
	if !reflect.DeepEqual(res.HeuristicsApplied, []string{"size-without-decomposition"}) {
		t.Errorf("HeuristicsApplied = %v, want only the L/XL heuristic", res.HeuristicsApplied)
	}
	if len(logs.lines) != 1 || !strings.HasPrefix(logs.lines[0], "capacity: window unknown") {
		t.Errorf("log = %q, want one %q line", logs.lines, "capacity: window unknown")
	}
}

// One level of decomposition: a capacity-forced child still over the cap
// recovers by human decomposition, never by a second automatic one.
func TestCapacityGate_OverCapacityChildRequiresHumanDecomposition(t *testing.T) {
	parent, _ := evaluateWithCapacity(t, DefaultGateConfig(), GateInput{
		Title: "Parent", Labels: []string{"size:M"}, Capacity: &CapacityInput{Window: 32768},
	})
	if parent.Allowed || parent.Capacity.Recovery != RecoveryDecompose {
		t.Fatalf("parent: allowed=%v recovery=%q, want a rejection recovering by %q",
			parent.Allowed, parent.Capacity.Recovery, RecoveryDecompose)
	}

	child, logs := evaluateWithCapacity(t, DefaultGateConfig(), GateInput{
		Title:    "Child",
		Labels:   []string{"size:M"},
		Body:     "<!-- " + CapacityDecomposedMarker + " -->\n## Summary\n",
		Capacity: &CapacityInput{Window: 32768},
	})
	if child.Allowed {
		t.Fatal("an over-capacity child must not be allowed")
	}
	if !strings.Contains(child.Reason, "requires human decomposition") {
		t.Errorf("reason %q does not say %q", child.Reason, "requires human decomposition")
	}
	if child.Capacity.Recovery != RecoveryHumanDecomposition {
		t.Errorf("recovery = %q, want %q", child.Capacity.Recovery, RecoveryHumanDecomposition)
	}
	if len(logs.lines) != 1 {
		t.Errorf("the check logged %d lines, want exactly 1: %q", len(logs.lines), logs.lines)
	}
}

func TestCapacityGate_SoftRouteTakesTheFirstAdmittingFallback(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.SoftRoute = true
	res, _ := evaluateWithCapacity(t, cfg, GateInput{
		Title: "Medium work", Labels: []string{"size:M"},
		Capacity: &CapacityInput{Window: 32768, Fallbacks: []CapacityCandidate{
			{Model: "unknown-window", Window: 0},
			{Model: "also-32k", Window: 32768},
			{Model: "local-131k", Window: 131072},
			{Model: "hosted-1m", Window: 1000000},
		}},
	})
	if !res.Allowed || res.RoutedModel != "local-131k" {
		t.Fatalf("allowed=%v routed=%q, want allowed on local-131k", res.Allowed, res.RoutedModel)
	}

	res, _ = evaluateWithCapacity(t, cfg, GateInput{
		Title: "Medium work", Labels: []string{"size:M"},
		Capacity: &CapacityInput{Window: 32768, Fallbacks: []CapacityCandidate{{Model: "also-32k", Window: 32768}}},
	})
	if res.Allowed || res.RoutedModel != "" {
		t.Fatalf("with no admitting fallback: allowed=%v routed=%q, want a rejection", res.Allowed, res.RoutedModel)
	}
}

func TestLoadGateConfigFromYAML_CapacityRoutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "pipeline:\n  size_gate:\n    routes:\n      reject_action: soft-route\n" +
		"      capacity_fallback_models:\n        - lmstudio/qwen-131k\n        - \" \"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadGateConfigFromYAML(path)
	if !cfg.SoftRoute || !cfg.CapacityEnabled {
		t.Errorf("SoftRoute=%v CapacityEnabled=%v, want both true", cfg.SoftRoute, cfg.CapacityEnabled)
	}
	if !reflect.DeepEqual(cfg.CapacityFallbackModels, []string{"lmstudio/qwen-131k"}) {
		t.Errorf("CapacityFallbackModels = %v", cfg.CapacityFallbackModels)
	}

	if err := os.WriteFile(path, []byte("pipeline:\n  size_gate:\n    enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if LoadGateConfigFromYAML(path).CapacityEnabled {
		t.Error("size_gate.enabled: false must turn the capacity check off")
	}
}
