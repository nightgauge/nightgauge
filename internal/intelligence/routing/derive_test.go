package routing

import (
	"reflect"
	"testing"
)

func TestComplexityFromSize(t *testing.T) {
	tests := []struct {
		name       string
		size       string
		priority   string
		changeType string
		want       int
	}{
		// Base case: size only — Fibonacci values pass through.
		{"XS no priority", "XS", "", "code", 1},
		{"S no priority", "S", "", "code", 2},
		{"M no priority", "M", "", "code", 3},
		{"L no priority", "L", "", "code", 5},
		{"XL no priority", "XL", "", "code", 8},

		// Priority multiplier rounded to nearest Fibonacci.
		{"M + high (1.2x → 3.6 → 3)", "M", "high", "code", 3},
		{"M + critical (1.5x → 4.5 → 5)", "M", "critical", "code", 5},
		{"S + high (1.2x → 2.4 → 2)", "S", "high", "code", 2},
		{"S + critical (1.5x → 3.0 → 3)", "S", "critical", "code", 3},
		{"L + high (1.2x → 6 → 5)", "L", "high", "code", 5},
		{"L + critical (1.5x → 7.5 → 8)", "L", "critical", "code", 8},
		{"XS + critical (1.5x → 1.5 → 1, ties keep first)", "XS", "critical", "code", 1},
		{"XS + high (1.2x → 1.2 → 1)", "XS", "high", "code", 1},

		// Non-code change caps base at 2 before multiplier.
		{"M + docs (cap 2, no priority)", "M", "", "docs", 2},
		{"L + docs (cap 2, critical → 3)", "L", "critical", "docs", 3},
		{"M + config (cap 2)", "M", "", "config", 2},

		// Unknown size defaults to base 3.
		{"unknown size defaults to 3", "", "", "code", 3},
		{"junk size defaults to 3", "ZZ", "", "code", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := complexityFromSize(tt.size, tt.priority, tt.changeType)
			if got != tt.want {
				t.Errorf("complexityFromSize(%q,%q,%q) = %d, want %d", tt.size, tt.priority, tt.changeType, got, tt.want)
			}
		})
	}
}

func TestDeriveTaskType(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		title  string
		body   string
		want   string
	}{
		{"verification label", []string{"type:verification"}, "x", "", "verification"},
		{"docs label → docs-only", []string{"type:docs"}, "x", "", "docs-only"},
		{"bug label → bugfix", []string{"type:bug"}, "x", "", "bugfix"},
		{"refactor label", []string{"type:refactor"}, "x", "", "refactor"},
		{"chore label", []string{"type:chore"}, "x", "", "chore"},
		{"test label routed as chore", []string{"type:test"}, "x", "", "chore"},
		{"feature label", []string{"type:feature"}, "x", "", "feature"},
		{"no label → feature default", nil, "do something", "", "feature"},
		{"verify keyword in title", nil, "verify deployment", "", "verification"},
		{"audit keyword", nil, "audit security model", "", "verification"},
		{"docs keyword without code indicator", nil, "update readme", "rewrite the documentation", "docs-only"},
		{"docs keyword with code indicator → feature", nil, "update readme", "implement function for parser", "feature"},
		{"label uppercase normalized", []string{"Type:Bug"}, "x", "", "bugfix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveTaskType(tt.labels, tt.title, tt.body)
			if got != tt.want {
				t.Errorf("deriveTaskType(%v, %q, %q) = %q, want %q", tt.labels, tt.title, tt.body, got, tt.want)
			}
		})
	}
}

func TestDetectFoundationTask(t *testing.T) {
	tests := []struct {
		name     string
		taskType string
		title    string
		want     bool
	}{
		{"chore + scaffold", "chore", "scaffold the auth module", true},
		{"chore + setup", "chore", "setup CI pipeline", true},
		{"chore + bootstrap", "chore", "bootstrap project", true},
		{"chore + initialize", "chore", "initialize npm workspace", true},
		{"chore + initialise (UK spelling)", "chore", "initialise the cache", true},
		{"chore + init", "chore", "init monorepo", true},
		{"chore + configure", "chore", "configure GitHub actions", true},
		{"chore + scaffolding (boundary)", "chore", "scaffolding tests", false},
		{"chore + unrelated title", "chore", "remove dead code", false},
		{"feature + scaffold (not chore)", "feature", "scaffold new module", false},
		{"bugfix + setup (not chore)", "bugfix", "setup wrong order", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectFoundationTask(tt.taskType, tt.title)
			if got != tt.want {
				t.Errorf("detectFoundationTask(%q, %q) = %v, want %v", tt.taskType, tt.title, got, tt.want)
			}
		})
	}
}

func TestResolveSize(t *testing.T) {
	tests := []struct {
		name       string
		boardSize  string
		labels     []string
		foundation bool
		want       string
	}{
		{"foundation → XS", "L", []string{"size:L"}, true, "XS"},
		{"board size wins over label", "M", []string{"size:L"}, false, "M"},
		{"label fallback", "", []string{"size:S"}, false, "S"},
		{"label uppercase", "", []string{"size:xl"}, false, "XL"},
		{"default M", "", nil, false, "M"},
		{"unknown board size falls through to label", "ZZ", []string{"size:S"}, false, "S"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSize(tt.boardSize, tt.labels, tt.foundation)
			if got != tt.want {
				t.Errorf("resolveSize(%q, %v, %v) = %q, want %q", tt.boardSize, tt.labels, tt.foundation, got, tt.want)
			}
		})
	}
}

func TestResolvePriority(t *testing.T) {
	tests := []struct {
		name          string
		boardPriority string
		labels        []string
		want          string
	}{
		{"P0 → critical", "P0", nil, "critical"},
		{"P1 → high", "P1", nil, "high"},
		{"P2 → medium", "P2", nil, "medium"},
		{"P3 → low", "P3", nil, "low"},
		{"label fallback", "", []string{"priority:critical"}, "critical"},
		{"empty → empty", "", nil, ""},
		{"unknown board priority falls to label", "P9", []string{"priority:high"}, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePriority(tt.boardPriority, tt.labels)
			if got != tt.want {
				t.Errorf("resolvePriority(%q, %v) = %q, want %q", tt.boardPriority, tt.labels, got, tt.want)
			}
		})
	}
}

func TestDeriveChangeType(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		title  string
		body   string
		want   string
	}{
		{"type:docs label → docs", []string{"type:docs"}, "x", "", "docs"},
		{"type:feature → code", []string{"type:feature"}, "x", "", "code"},
		{"type:bug → code", []string{"type:bug"}, "x", "", "code"},
		{"docs keyword without code indicator", nil, "update readme", "improve documentation", "docs"},
		{"docs keyword with code indicator → code via fallback", nil, "implement function for parser", "", "code"},
		{"config-only", nil, "tweak the .yaml file", "", "config"},
		{"feature keyword in body forces code", nil, "tweak yaml", "user can configure new feature", "code"},
		{"default code", nil, "implement parser", "", "code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveChangeType(tt.labels, tt.title, tt.body)
			if got != tt.want {
				t.Errorf("deriveChangeType(%v, %q, %q) = %q, want %q", tt.labels, tt.title, tt.body, got, tt.want)
			}
		})
	}
}

func TestRouteForDecision(t *testing.T) {
	tests := []struct {
		name       string
		changeType string
		complexity int
		size       string
		priority   string
		want       string
	}{
		{"complexity 1 → trivial", "code", 1, "M", "high", "trivial"},
		{"complexity 2 → trivial", "code", 2, "M", "high", "trivial"},
		{"docs + XS → trivial", "docs", 3, "XS", "", "trivial"},
		{"docs + L → extensive (large size)", "docs", 3, "L", "", "extensive"},
		{"L size → extensive", "code", 3, "L", "", "extensive"},
		{"XL size → extensive", "code", 3, "XL", "", "extensive"},
		{"critical priority → extensive", "code", 3, "M", "critical", "extensive"},
		{"complexity 5 → extensive", "code", 5, "M", "high", "extensive"},
		{"M + high (canonical issue #3062 case)", "code", 3, "M", "high", "standard"},
		{"M + medium → standard", "code", 3, "M", "medium", "standard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeForDecision(tt.changeType, tt.complexity, tt.size, tt.priority, false)
			if got != tt.want {
				t.Errorf("routeForDecision(%q, %d, %q, %q) = %q, want %q", tt.changeType, tt.complexity, tt.size, tt.priority, got, tt.want)
			}
		})
	}
}

func TestSkipStagesFor(t *testing.T) {
	tests := []struct {
		name       string
		taskType   string
		complexity int
		foundation bool
		changeType string
		want       []string
	}{
		{"feature complexity 3 → none skipped", "feature", 3, false, "code", []string{}},
		{"complexity 2 → skip planning + validate", "feature", 2, false, "code", []string{"feature-planning", "feature-validate"}},
		{"chore complexity 3 → skip planning only", "chore", 3, false, "code", []string{"feature-planning"}},
		{"foundation → skip planning + validate", "chore", 4, true, "code", []string{"feature-planning", "feature-validate"}},
		{"docs change → skip validate", "feature", 3, false, "docs", []string{"feature-validate"}},
		{"config change → skip validate", "feature", 3, false, "config", []string{"feature-validate"}},
		{"docs-only task → skip validate", "docs-only", 3, false, "code", []string{"feature-validate"}},
		{"chore + complexity 2 → skip both (deduped)", "chore", 2, false, "code", []string{"feature-planning", "feature-validate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipStagesFor(tt.taskType, tt.complexity, tt.foundation, tt.changeType, false)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("skipStagesFor(%q, %d, %v, %q) = %v, want %v", tt.taskType, tt.complexity, tt.foundation, tt.changeType, got, tt.want)
			}
		})
	}
}

func TestDocumentationScopeFor(t *testing.T) {
	tests := []struct {
		name     string
		size     string
		taskType string
		priority string
		want     string
	}{
		{"XS + bug → minimal", "XS", "bugfix", "", "minimal"},
		{"S + bug → targeted", "S", "bugfix", "", "targeted"},
		{"S + docs → targeted", "S", "docs-only", "", "targeted"},
		{"L → extended", "L", "feature", "", "extended"},
		{"XL → extended", "XL", "feature", "", "extended"},
		{"critical priority → extended", "M", "feature", "critical", "extended"},
		{"M + feature → standard", "M", "feature", "high", "standard"},
		{"S + feature → standard", "S", "feature", "high", "standard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := documentationScopeFor(tt.size, tt.taskType, tt.priority)
			if got != tt.want {
				t.Errorf("documentationScopeFor(%q, %q, %q) = %q, want %q", tt.size, tt.taskType, tt.priority, got, tt.want)
			}
		})
	}
}

func TestDerive_ParityWithIssue3062(t *testing.T) {
	// Issue #3062 routing block: M size, type:feature, priority:high.
	// Spot-check parity: change_type=code, task_type=feature,
	// complexity_score=3, suggested_route=standard, skip_stages=[],
	// foundation_task=false, documentation_scope=standard.
	got := Derive(DeriveInput{
		Title:         "feat(go-binary): nightgauge issue route — pipeline-stage label/board-field routing",
		Body:          "",
		Labels:        []string{"type:feature", "priority:high", "size:M", "component:go-binary", "improvement", "reliability"},
		BoardSize:     "M",
		BoardPriority: "P1",
	})

	if got.ChangeType != "code" {
		t.Errorf("ChangeType = %q, want code", got.ChangeType)
	}
	if got.TaskType != "feature" {
		t.Errorf("TaskType = %q, want feature", got.TaskType)
	}
	if got.ComplexityScore != 3 {
		t.Errorf("ComplexityScore = %d, want 3", got.ComplexityScore)
	}
	if got.SuggestedRoute != "standard" {
		t.Errorf("SuggestedRoute = %q, want standard", got.SuggestedRoute)
	}
	if len(got.SkipStages) != 0 {
		t.Errorf("SkipStages = %v, want []", got.SkipStages)
	}
	if got.FoundationTask {
		t.Error("FoundationTask = true, want false")
	}
	if got.DocumentationScope != "standard" {
		t.Errorf("DocumentationScope = %q, want standard", got.DocumentationScope)
	}
	if got.EffectiveSize != "M" {
		t.Errorf("EffectiveSize = %q, want M", got.EffectiveSize)
	}
	if got.EffectivePriority != "high" {
		t.Errorf("EffectivePriority = %q, want high", got.EffectivePriority)
	}
}

func TestDerive_FoundationOverride(t *testing.T) {
	// Foundation task: type:chore + scaffold title forces XS/trivial routing.
	got := Derive(DeriveInput{
		Title:  "scaffold the new auth module",
		Labels: []string{"type:chore", "size:M"},
	})
	if !got.FoundationTask {
		t.Error("expected FoundationTask=true")
	}
	if got.EffectiveSize != "XS" {
		t.Errorf("EffectiveSize = %q, want XS", got.EffectiveSize)
	}
	if got.SuggestedRoute != "trivial" {
		t.Errorf("SuggestedRoute = %q, want trivial", got.SuggestedRoute)
	}
	wantSkips := map[string]bool{"feature-planning": true, "feature-validate": true}
	for _, s := range got.SkipStages {
		if !wantSkips[s] {
			t.Errorf("unexpected skip stage: %s", s)
		}
		delete(wantSkips, s)
	}
	if len(wantSkips) > 0 {
		t.Errorf("missing skip stages: %v", wantSkips)
	}
}

func TestDerive_DocsOnlySkippedValidation(t *testing.T) {
	got := Derive(DeriveInput{
		Title:  "update CONTRIBUTING.md",
		Labels: []string{"type:docs", "size:S"},
	})
	if got.TaskType != "docs-only" {
		t.Errorf("TaskType = %q, want docs-only", got.TaskType)
	}
	if got.ChangeType != "docs" {
		t.Errorf("ChangeType = %q, want docs", got.ChangeType)
	}
	hasValidate := false
	for _, s := range got.SkipStages {
		if s == "feature-validate" {
			hasValidate = true
		}
	}
	if !hasValidate {
		t.Errorf("expected feature-validate in SkipStages, got %v", got.SkipStages)
	}
}

func TestDerive_LCriticalGoesExtensive(t *testing.T) {
	got := Derive(DeriveInput{
		Title:         "rewrite the orchestrator",
		Labels:        []string{"type:refactor"},
		BoardSize:     "L",
		BoardPriority: "P0",
	})
	if got.SuggestedRoute != "extensive" {
		t.Errorf("SuggestedRoute = %q, want extensive", got.SuggestedRoute)
	}
	if got.ComplexityScore != 8 {
		t.Errorf("ComplexityScore = %d, want 8", got.ComplexityScore)
	}
	if got.DocumentationScope != "extended" {
		t.Errorf("DocumentationScope = %q, want extended", got.DocumentationScope)
	}
}
