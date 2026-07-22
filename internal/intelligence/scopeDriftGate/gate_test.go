package scopeDriftGate

import (
	"testing"
)

func TestDefaultGateConfig(t *testing.T) {
	cfg := DefaultGateConfig()
	if !cfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.EnforcementMode != EnforcementWarn {
		t.Errorf("EnforcementMode = %q, want %q", cfg.EnforcementMode, EnforcementWarn)
	}
	if cfg.BypassLabel != "scope:cross-cutting" {
		t.Errorf("BypassLabel = %q, want %q", cfg.BypassLabel, "scope:cross-cutting")
	}
	if len(cfg.AllowlistDocs) == 0 {
		t.Error("AllowlistDocs is empty")
	}
	if len(cfg.AllowlistChore) == 0 {
		t.Error("AllowlistChore is empty")
	}
}

func TestMatchesPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Exact match
		{"Makefile", "Makefile", true},
		{"Makefile", "src/Makefile", false},
		// Doublestar prefix
		{"docs/**", "docs/index.md", true},
		{"docs/**", "docs/sub/dir/file.md", true},
		{"docs/**", "docs", true},
		{"docs/**", "src/docs/index.md", false},
		{"docs/**", "documentation/index.md", false},
		// Root .md
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		// README* — root-anchored single-segment glob (no implicit basename match)
		{"README*", "README.md", true},
		{"README*", "README", true},
		{"README*", "docs/README.md", false},
		{"README*", "src/code.go", false},
		// **/README* — explicit "any depth" form
		{"**/README*", "README.md", true},
		{"**/README*", "docs/README.md", true},
		{"**/README*", "docs/sub/README.md", true},
		{"**/README*", "docs/intro.md", false},
		// .github prefix
		{".github/**", ".github/CODEOWNERS", true},
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", "github/workflows/ci.yml", false},
		// Middle doublestar
		{"src/**/*.test.ts", "src/foo/bar.test.ts", true},
		{"src/**/*.test.ts", "src/bar.test.ts", true}, // ** matches zero segments
		{"src/**/*.test.ts", "src/foo/bar.ts", false},
		// Empty pattern guard
		{"", "anything", false},
	}

	for _, tc := range tests {
		t.Run(tc.pattern+"::"+tc.path, func(t *testing.T) {
			got := matchesPattern(tc.path, tc.pattern)
			if got != tc.want {
				t.Errorf("matchesPattern(%q, %q) = %v, want %v", tc.path, tc.pattern, got, tc.want)
			}
		})
	}
}

func TestEvaluate_DocsAllowed(t *testing.T) {
	g := NewGateEvaluator(DefaultGateConfig())
	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{
		"docs/CONFIGURATION.md",
		"docs/sub/page.md",
		"README.md",
		".github/CODEOWNERS",
	})
	if !res.Allowed {
		t.Fatalf("expected Allowed=true, got false (reason: %q, drifted: %v)", res.Reason, res.DriftedFiles)
	}
	if len(res.DriftedFiles) != 0 {
		t.Errorf("expected zero DriftedFiles, got %v", res.DriftedFiles)
	}
	if len(res.AllowedFiles) != 4 {
		t.Errorf("expected 4 AllowedFiles, got %d", len(res.AllowedFiles))
	}
}

func TestEvaluate_DocsDriftWarnMode(t *testing.T) {
	g := NewGateEvaluator(DefaultGateConfig())
	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{
		"docs/CONFIGURATION.md",
		"src/main.go",
		"packages/db/src/index.ts",
	})
	if !res.Allowed {
		t.Fatalf("warn mode must Allow=true even with drift, got Allowed=false (reason: %q)", res.Reason)
	}
	if len(res.DriftedFiles) != 2 {
		t.Errorf("expected 2 DriftedFiles, got %v", res.DriftedFiles)
	}
	found := false
	for _, h := range res.HeuristicsApplied {
		if h == "allowlist-mismatch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("HeuristicsApplied missing 'allowlist-mismatch': %v", res.HeuristicsApplied)
	}
	if res.SuggestedAction == "" {
		t.Error("SuggestedAction is empty when drift detected")
	}
}

func TestEvaluate_DocsDriftStrictMode(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{
		"docs/CONFIGURATION.md",
		"src/main.go",
	})
	if res.Allowed {
		t.Fatalf("strict mode must reject drift, got Allowed=true (reason: %q)", res.Reason)
	}
	if len(res.DriftedFiles) != 1 || res.DriftedFiles[0] != "src/main.go" {
		t.Errorf("expected exactly src/main.go drifted, got %v", res.DriftedFiles)
	}
	if res.Reason == "" {
		t.Error("Reason is empty on rejection")
	}
}

func TestEvaluate_BypassLabel(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeDocs,
		[]string{"type:docs", "scope:cross-cutting"},
		[]string{"src/main.go", "packages/db/src/index.ts"})
	if !res.Allowed {
		t.Fatalf("bypass label must allow even strict + drift, got Allowed=false")
	}
	if !res.Bypassed {
		t.Error("expected Bypassed=true")
	}
	if len(res.DriftedFiles) != 0 {
		t.Errorf("expected DriftedFiles to be empty when bypass active, got %v", res.DriftedFiles)
	}
	found := false
	for _, h := range res.HeuristicsApplied {
		if h == "bypass-label" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("HeuristicsApplied missing 'bypass-label': %v", res.HeuristicsApplied)
	}
}

func TestEvaluate_ChoreUsesChoreAllowlist(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	cfg.AllowlistChore = []string{"configs/**", "*.md"}
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeChore, []string{"type:chore"}, []string{
		"configs/release.toml",
		"CHANGELOG.md",
	})
	if !res.Allowed {
		t.Fatalf("expected Allowed=true with custom chore allowlist, got false (drifted: %v)", res.DriftedFiles)
	}

	// Now a docs-allowlist-only file should drift under custom chore allowlist.
	res = g.Evaluate(IssueTypeChore, []string{"type:chore"}, []string{
		"docs/CONFIGURATION.md", // not in custom chore allowlist
	})
	if res.Allowed {
		t.Fatalf("expected docs path to drift under custom chore allowlist (strict)")
	}
}

func TestEvaluate_ChoreFallsBackToDocsAllowlist(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.AllowlistChore = nil // not set
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeChore, []string{"type:chore"}, []string{
		"docs/CONFIGURATION.md",
	})
	if !res.Allowed {
		t.Fatalf("expected chore to inherit docs allowlist when AllowlistChore is empty")
	}
}

func TestEvaluate_UnknownIssueTypeIsAllowed(t *testing.T) {
	g := NewGateEvaluator(DefaultGateConfig())
	res := g.Evaluate("feature", []string{"type:feature"}, []string{
		"src/main.go",
		"packages/db/src/index.ts",
	})
	if !res.Allowed {
		t.Fatalf("non-docs/chore issues must always be allowed, got Allowed=false")
	}
	if len(res.DriftedFiles) != 0 {
		t.Errorf("expected DriftedFiles to be empty for unknown issue type, got %v", res.DriftedFiles)
	}
}

func TestEvaluate_EmptyChangedFiles(t *testing.T) {
	g := NewGateEvaluator(DefaultGateConfig())
	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, nil)
	if !res.Allowed {
		t.Fatalf("empty file list must be allowed, got Allowed=false")
	}
	if len(res.DriftedFiles) != 0 {
		t.Errorf("expected DriftedFiles empty, got %v", res.DriftedFiles)
	}
}

func TestEvaluate_DisabledGate(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = false
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{
		"src/main.go",
	})
	if !res.Allowed {
		t.Fatal("disabled gate must always allow")
	}
	if len(res.DriftedFiles) != 0 {
		t.Errorf("disabled gate must not compute DriftedFiles, got %v", res.DriftedFiles)
	}
}

func TestEvaluate_PartialDrift(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{
		"docs/CONFIGURATION.md",    // allowed
		"README.md",                // allowed
		"src/main.go",              // drifted
		"packages/db/src/index.ts", // drifted
	})
	if res.Allowed {
		t.Fatalf("expected Allowed=false with drift in strict mode")
	}
	if len(res.AllowedFiles) != 2 {
		t.Errorf("expected 2 AllowedFiles, got %v", res.AllowedFiles)
	}
	if len(res.DriftedFiles) != 2 {
		t.Errorf("expected 2 DriftedFiles, got %v", res.DriftedFiles)
	}
}

func TestEvaluate_CustomBypassLabel(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	cfg.BypassLabel = "scope:custom-bypass"
	g := NewGateEvaluator(cfg)

	// Default bypass label should NOT trigger.
	res := g.Evaluate(IssueTypeDocs,
		[]string{"type:docs", "scope:cross-cutting"},
		[]string{"src/main.go"})
	if res.Allowed {
		t.Fatalf("default bypass label must not apply when custom label is set")
	}

	// Custom bypass label triggers.
	res = g.Evaluate(IssueTypeDocs,
		[]string{"type:docs", "scope:custom-bypass"},
		[]string{"src/main.go"})
	if !res.Allowed || !res.Bypassed {
		t.Fatalf("custom bypass label failed to trigger; Allowed=%v Bypassed=%v", res.Allowed, res.Bypassed)
	}
}

func TestEvaluate_ResultPopulation(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.EnforcementMode = EnforcementStrict
	g := NewGateEvaluator(cfg)

	res := g.Evaluate(IssueTypeDocs, []string{"type:docs"}, []string{"src/main.go"})
	if res.IssueType != IssueTypeDocs {
		t.Errorf("IssueType = %q, want %q", res.IssueType, IssueTypeDocs)
	}
	if res.EnforcementMode != EnforcementStrict {
		t.Errorf("EnforcementMode = %q, want %q", res.EnforcementMode, EnforcementStrict)
	}
}
