package routing

import (
	"reflect"
	"testing"
)

func TestDefaultChangeRules_Shape(t *testing.T) {
	defs := DefaultChangeRules()
	byName := map[string]ChangeRule{}
	for _, r := range defs {
		byName[r.Name] = r
	}
	for _, name := range []string{"docs-only", "config-only", "high-risk-floor"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("default rule %q missing", name)
		}
	}
	// docs-only / config-only must carry change_types so they match predictively.
	if !reflect.DeepEqual(byName["docs-only"].ChangeTypes, []string{"docs"}) {
		t.Errorf("docs-only change_types = %v, want [docs]", byName["docs-only"].ChangeTypes)
	}
	if !reflect.DeepEqual(byName["config-only"].ChangeTypes, []string{"config"}) {
		t.Errorf("config-only change_types = %v, want [config]", byName["config-only"].ChangeTypes)
	}
	// high-risk-floor is glob-only (authoritative, post-dev) — no predictive
	// change_types, so it never fires inside Derive().
	if len(byName["high-risk-floor"].ChangeTypes) != 0 {
		t.Errorf("high-risk-floor must be glob-only (no change_types), got %v", byName["high-risk-floor"].ChangeTypes)
	}
}

func TestMergeChangeRules(t *testing.T) {
	t.Run("empty user → defaults unchanged", func(t *testing.T) {
		got := MergeChangeRules(nil)
		if !reflect.DeepEqual(got, DefaultChangeRules()) {
			t.Errorf("merge(nil) = %v, want defaults", got)
		}
	})

	t.Run("user rule with default name overrides in place, matched first", func(t *testing.T) {
		user := []ChangeRule{{Name: "docs-only", ChangeTypes: []string{"docs"}, SkipStages: []string{"feature-validate"}, OverrideRoute: "standard"}}
		got := MergeChangeRules(user)
		// docs-only appears exactly once, and it is the user's version.
		count := 0
		for _, r := range got {
			if r.Name == "docs-only" {
				count++
				if r.OverrideRoute != "standard" {
					t.Errorf("docs-only override_route = %q, want standard (user wins)", r.OverrideRoute)
				}
			}
		}
		if count != 1 {
			t.Errorf("docs-only appears %d times, want 1", count)
		}
		// User override is emitted before the remaining defaults.
		if got[0].Name != "docs-only" {
			t.Errorf("first merged rule = %q, want docs-only (user rules first)", got[0].Name)
		}
	})

	t.Run("new user rule prepended ahead of defaults", func(t *testing.T) {
		user := []ChangeRule{{Name: "generated", ChangeTypes: []string{"code"}, SkipStages: []string{"feature-validate"}}}
		got := MergeChangeRules(user)
		if got[0].Name != "generated" {
			t.Errorf("first merged rule = %q, want generated", got[0].Name)
		}
		if len(got) != len(DefaultChangeRules())+1 {
			t.Errorf("merged length = %d, want defaults+1", len(got))
		}
	})

	t.Run("duplicate user names collapse, last wins", func(t *testing.T) {
		user := []ChangeRule{
			{Name: "x", ChangeTypes: []string{"code"}, OverrideRoute: "trivial"},
			{Name: "x", ChangeTypes: []string{"code"}, OverrideRoute: "extensive"},
		}
		got := MergeChangeRules(user)
		seen := 0
		for _, r := range got {
			if r.Name == "x" {
				seen++
				if r.OverrideRoute != "extensive" {
					t.Errorf("x override_route = %q, want extensive (last def wins)", r.OverrideRoute)
				}
			}
		}
		if seen != 1 {
			t.Errorf("rule x appears %d times, want 1", seen)
		}
	})
}

func TestMatchChangeRulePredictive(t *testing.T) {
	rules := MergeChangeRules(nil)
	if r, ok := matchChangeRulePredictive(rules, "docs"); !ok || r.Name != "docs-only" {
		t.Errorf("docs → %q (ok=%v), want docs-only", r.Name, ok)
	}
	if r, ok := matchChangeRulePredictive(rules, "config"); !ok || r.Name != "config-only" {
		t.Errorf("config → %q (ok=%v), want config-only", r.Name, ok)
	}
	// "code" matches no default rule (high-risk-floor is glob-only).
	if r, ok := matchChangeRulePredictive(rules, "code"); ok {
		t.Errorf("code unexpectedly matched %q", r.Name)
	}
	// Glob-only rule (no change_types) is never matched predictively.
	globOnly := []ChangeRule{{Name: "vendored", Globs: []string{"vendor/**"}}}
	if _, ok := matchChangeRulePredictive(globOnly, "code"); ok {
		t.Error("glob-only rule matched predictively; want deferred to post-dev")
	}
}

// --- Derive() precedence integration ---

func skipSet(stages []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range stages {
		m[s] = true
	}
	return m
}

func TestDerive_DefaultDocsRuleApplied(t *testing.T) {
	got := Derive(DeriveInput{
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs", "size:S"},
	})
	if got.MatchedChangeRule != "docs-only" {
		t.Errorf("MatchedChangeRule = %q, want docs-only", got.MatchedChangeRule)
	}
	if got.SuggestedRoute != "trivial" {
		t.Errorf("SuggestedRoute = %q, want trivial", got.SuggestedRoute)
	}
	want := skipSet([]string{"feature-planning", "feature-validate"})
	if !reflect.DeepEqual(skipSet(got.SkipStages), want) {
		t.Errorf("SkipStages = %v, want planning+validate", got.SkipStages)
	}
}

func TestDerive_DefaultConfigRuleApplied(t *testing.T) {
	got := Derive(DeriveInput{
		Title:     "tweak the .yaml file",
		BoardSize: "S",
	})
	if got.ChangeType != "config" {
		t.Fatalf("precondition: ChangeType = %q, want config", got.ChangeType)
	}
	if got.MatchedChangeRule != "config-only" {
		t.Errorf("MatchedChangeRule = %q, want config-only", got.MatchedChangeRule)
	}
	if !reflect.DeepEqual(got.SkipStages, []string{"feature-validate"}) {
		t.Errorf("SkipStages = %v, want [feature-validate]", got.SkipStages)
	}
}

func TestDerive_ForceFullPipelineBeatsRules(t *testing.T) {
	got := Derive(DeriveInput{
		Title:             "update CONTRIBUTING.md",
		Labels:            []string{"type:docs", "size:S"},
		ForceFullPipeline: true,
	})
	if len(got.SkipStages) != 0 {
		t.Errorf("SkipStages = %v, want [] (force_full_pipeline)", got.SkipStages)
	}
	if got.MatchedChangeRule != "" {
		t.Errorf("MatchedChangeRule = %q, want empty (rules bypassed)", got.MatchedChangeRule)
	}
}

func TestDerive_RiskFloorBeatsRules(t *testing.T) {
	// A docs issue that also touches a high-risk component must NOT be
	// fast-tracked by the docs-only rule — the risk floor wins.
	got := Derive(DeriveInput{
		Title:  "update auth docs",
		Labels: []string{"type:docs", "size:S", "component:security"},
	})
	if !got.RiskHigh {
		t.Fatalf("precondition: RiskHigh = false, want true")
	}
	if len(got.SkipStages) != 0 {
		t.Errorf("SkipStages = %v, want [] (risk floor)", got.SkipStages)
	}
	if got.SuggestedRoute != "extensive" {
		t.Errorf("SuggestedRoute = %q, want extensive (risk floor)", got.SuggestedRoute)
	}
	if got.MatchedChangeRule != "" {
		t.Errorf("MatchedChangeRule = %q, want empty (rules bypassed)", got.MatchedChangeRule)
	}
}

func TestDerive_UserRuleOverridesDefault(t *testing.T) {
	// User redefines docs-only to be less aggressive (route standard, only skip
	// validate) — the user's rule must win over the built-in default.
	got := Derive(DeriveInput{
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs", "size:S"},
		ChangeRules: []ChangeRule{
			{Name: "docs-only", ChangeTypes: []string{"docs"}, SkipStages: []string{"feature-validate"}, OverrideRoute: "standard"},
		},
	})
	if got.MatchedChangeRule != "docs-only" {
		t.Errorf("MatchedChangeRule = %q, want docs-only", got.MatchedChangeRule)
	}
	if got.SuggestedRoute != "standard" {
		t.Errorf("SuggestedRoute = %q, want standard (user override)", got.SuggestedRoute)
	}
	if !reflect.DeepEqual(got.SkipStages, []string{"feature-validate"}) {
		t.Errorf("SkipStages = %v, want [feature-validate]", got.SkipStages)
	}
}

func TestDerive_NewUserRuleMatchesCode(t *testing.T) {
	// A brand-new user rule can fast-track a "code" change that no default
	// covers (e.g. generated files).
	got := Derive(DeriveInput{
		Title:     "regenerate IPC client",
		Labels:    []string{"type:feature"},
		BoardSize: "M",
		ChangeRules: []ChangeRule{
			{Name: "generated", ChangeTypes: []string{"code"}, SkipStages: []string{"feature-validate"}, OverrideRoute: "trivial"},
		},
	})
	if got.MatchedChangeRule != "generated" {
		t.Errorf("MatchedChangeRule = %q, want generated", got.MatchedChangeRule)
	}
	if got.SuggestedRoute != "trivial" {
		t.Errorf("SuggestedRoute = %q, want trivial", got.SuggestedRoute)
	}
	if !reflect.DeepEqual(got.SkipStages, []string{"feature-validate"}) {
		t.Errorf("SkipStages = %v, want [feature-validate]", got.SkipStages)
	}
}

func TestDerive_InvalidRuleValuesSanitized(t *testing.T) {
	got := Derive(DeriveInput{
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs", "size:S"},
		ChangeRules: []ChangeRule{
			{Name: "docs-only", ChangeTypes: []string{"docs"}, SkipStages: []string{"feature-validate", "not-a-stage"}, OverrideRoute: "bogus"},
		},
	})
	// Invalid override_route is ignored → complexity-derived route stands.
	// (docs + S derives trivial on its own.)
	if got.SuggestedRoute != "trivial" {
		t.Errorf("SuggestedRoute = %q, want trivial (invalid override ignored)", got.SuggestedRoute)
	}
	// Invalid skip stage filtered out.
	if !reflect.DeepEqual(got.SkipStages, []string{"feature-validate"}) {
		t.Errorf("SkipStages = %v, want [feature-validate] (invalid filtered)", got.SkipStages)
	}
}

func TestDerive_CodeChangeNoRuleNoChange(t *testing.T) {
	// A plain code change matches no default rule → behavior identical to before
	// change_rules existed.
	got := Derive(DeriveInput{
		Title:         "implement parser",
		Labels:        []string{"type:feature", "size:M", "priority:high"},
		BoardSize:     "M",
		BoardPriority: "P1",
	})
	if got.MatchedChangeRule != "" {
		t.Errorf("MatchedChangeRule = %q, want empty", got.MatchedChangeRule)
	}
	if got.SuggestedRoute != "standard" {
		t.Errorf("SuggestedRoute = %q, want standard", got.SuggestedRoute)
	}
	if len(got.SkipStages) != 0 {
		t.Errorf("SkipStages = %v, want []", got.SkipStages)
	}
}
