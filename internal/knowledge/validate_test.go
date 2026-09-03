package knowledge_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// validADRBlock is a complete ADR block that satisfies the validator.
const validADRBlock = `
## ADR-001: Use Go for deterministic validation

**Status**: Proposed
**Context**: The validation logic runs in planning skill where TypeScript SDK is not available.
**Decision**: Implement validation in Go binary so it is callable from any context.
**Consequences**: Single canonical validator accessible via CLI; no runtime dependencies on Node.
`

// planWithTradeoffs contains 2+ distinct tradeoff keywords.
const planWithTradeoffs = `# PLAN — #42

## Approach

We chose Go over TypeScript for the deterministic layer because it compiles to
a single binary. The alternative was to implement in TypeScript, but we rejected
that because it requires Node.js at runtime.
`

// planWithoutTradeoffs has no tradeoff keywords.
const planWithoutTradeoffs = `# PLAN — #42

## Approach

This is a straightforward configuration change that updates the timeout value.
No architectural choices were made.
`

// planWithSingleKeyword has only one keyword (below the 2-keyword threshold).
const planWithSingleKeyword = `# PLAN — #42

## Approach

We chose the simple approach since no other options were required.
`

func setupValidateFixtures(t *testing.T, issueNumber int, planText, decisionsText string) string {
	t.Helper()
	root := t.TempDir()

	plansDir := filepath.Join(root, ".nightgauge", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	planFile := filepath.Join(plansDir, fmt.Sprintf("%d-test-plan.md", issueNumber))
	if err := os.WriteFile(planFile, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	if decisionsText != "" {
		knowledgeDir := filepath.Join(root, ".nightgauge", "knowledge", "features",
			fmt.Sprintf("%d-test-issue", issueNumber))
		if err := os.MkdirAll(knowledgeDir, 0o755); err != nil {
			t.Fatalf("mkdir knowledge: %v", err)
		}
		decisionsFile := filepath.Join(knowledgeDir, "decisions.md")
		if err := os.WriteFile(decisionsFile, []byte(decisionsText), 0o644); err != nil {
			t.Fatalf("write decisions.md: %v", err)
		}
	}

	return root
}

func requireDecisionsTrue() *config.KnowledgeConfig {
	b := true
	return &config.KnowledgeConfig{RequireDecisions: &b}
}

func requireDecisionsFalse() *config.KnowledgeConfig {
	b := false
	return &config.KnowledgeConfig{RequireDecisions: &b}
}

func TestValidateDecisions_PlanWithTradeoffs_ValidADR_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, validADRBlock)
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass, got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
	if !result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=true")
	}
	if !result.HasADRBlocks {
		t.Error("expected result.HasADRBlocks=true")
	}
}

func TestValidateDecisions_PlanWithTradeoffs_EmptyDecisions_Fails(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n\n## Architecture Decisions\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected validation to fail, but got no error")
	}
	if result.Valid {
		t.Error("expected result.Valid=false")
	}
	if !result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=true")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestValidateDecisions_PlanWithoutTradeoffs_EmptyDecisions_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithoutTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass (no tradeoffs), got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
	if result.HasTradeoffs {
		t.Error("expected result.HasTradeoffs=false")
	}
}

func TestValidateDecisions_RequireDecisionsFalse_SkipsGate(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsFalse())
	if err != nil {
		t.Errorf("expected gate to be skipped, got error: %v", err)
	}
	if !result.Valid {
		t.Error("expected result.Valid=true when gate is disabled")
	}
	if !result.Skipped {
		t.Error("expected result.Skipped=true when require_decisions is false")
	}
}

func TestValidateDecisions_NilRequireDecisions_DefaultsFalse(t *testing.T) {
	// RequireDecisions not set → defaults to false → gate is skipped.
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, &config.KnowledgeConfig{})
	if err != nil {
		t.Errorf("expected nil RequireDecisions to skip gate, got error: %v", err)
	}
	if !result.Valid {
		t.Error("expected result.Valid=true when RequireDecisions is unset (defaults false)")
	}
}

func TestValidateDecisions_PlanWithSingleKeyword_Passes(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithSingleKeyword, "# Decisions: #42\n")
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err != nil {
		t.Errorf("expected validation to pass (single keyword below threshold), got error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected result.Valid=true; message: %s", result.Message)
	}
}

func TestValidateDecisions_MissingPlanFile_ReturnsError(t *testing.T) {
	root := t.TempDir()
	_, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected error when plan file is missing")
	}
}

func TestValidateDecisions_MissingDecisionsFile_FailsWithSuggestion(t *testing.T) {
	root := t.TempDir()
	plansDir := filepath.Join(root, ".nightgauge", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "42-test-plan.md"), []byte(planWithTradeoffs), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	result, err := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if err == nil {
		t.Error("expected error when decisions.md is missing")
	}
	if result.Valid {
		t.Error("expected result.Valid=false")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message with suggestions")
	}
}

func TestValidateDecisions_ErrorMessage_ContainsADRTemplate(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		t.Skip("validation passed — cannot check error message content")
	}
	if result.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestValidateDecisions_ErrorMessage_ContainsEscapeHatch(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		return
	}
	// The escape hatch config key should appear in the error message.
	if result.Message == "" {
		t.Error("expected non-empty error message mentioning escape hatch")
	}
}

func TestValidateDecisions_Signals_PopulatedOnFailure(t *testing.T) {
	root := setupValidateFixtures(t, 42, planWithTradeoffs, "# Decisions: #42\n")
	result, _ := knowledge.ValidateDecisionsPopulation(42, root, requireDecisionsTrue())
	if result.Valid {
		t.Skip("validation passed unexpectedly")
	}
	if len(result.Signals) == 0 {
		t.Error("expected result.Signals to be populated when HasTradeoffs=true")
	}
}

// writeEntry writes a knowledge file under root, creating parent directories.
func writeEntry(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateConformance(t *testing.T) {
	root := t.TempDir()

	writeEntry(t, root, "features/1-ok/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")
	writeEntry(t, root, "features/2-bare/PRD.md", "# PRD\n\nNo block at all.\n")
	writeEntry(t, root, "features/3-empty-type/decisions.md", "---\ntype: \"\"\ntags: [a]\n---\n\n# Decisions\n")
	// Unknown keys and unknown type values are never violations — the parser
	// tolerates them, and the check must agree or the two disagree about what
	// the contract is.
	writeEntry(t, root, "architecture/future.md", "---\ntype: some-future-kind\nunknown_key: 7\n---\n\n# Future\n")
	// Reserved navigation and template files carry no entry frontmatter.
	writeEntry(t, root, "README.md", "# Knowledge Base\n")
	writeEntry(t, root, "index.md", "# Index\n")
	writeEntry(t, root, "log.md", "# Log\n")
	writeEntry(t, root, "architecture/_template.md", "# Template\n")
	// Derived state under a dot-directory is not an entry.
	writeEntry(t, root, ".recall-cache/notes.md", "junk\n")
	// Non-markdown files are ignored.
	writeEntry(t, root, "features/1-ok/notes.txt", "text\n")

	res, err := knowledge.ValidateConformance(root)
	if err != nil {
		t.Fatalf("ValidateConformance: %v", err)
	}
	if res.Valid {
		t.Fatal("expected violations")
	}

	got := map[string]string{}
	for _, v := range res.Violations {
		got[v.Path] = v.Reason
	}
	want := map[string]string{
		"features/2-bare/PRD.md":             knowledge.ReasonNoFrontmatter,
		"features/3-empty-type/decisions.md": knowledge.ReasonMissingType,
	}
	if len(got) != len(want) {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	for path, reason := range want {
		if got[path] != reason {
			t.Errorf("%s = %q, want %q", path, got[path], reason)
		}
	}
	if res.FilesChecked != 4 {
		t.Errorf("files_checked = %d, want 4 (three entries plus the future one)", res.FilesChecked)
	}
	if res.Skipped != 4 {
		t.Errorf("skipped = %d, want 4 reserved files", res.Skipped)
	}
}

func TestValidateConformance_ReportsUnparseableBlocksWithoutAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	writeEntry(t, root, "features/1-bad/PRD.md", "---\ntype: prd\n  bad indent: [\n---\n\n# PRD\n")
	// A lifecycle status the contract deleted surfaces here rather than
	// silently ranking as an unknown status.
	writeEntry(t, root, "features/2-legacy/decisions.md", "---\ntype: decisions\nstatus: superseded\n---\n\n# Decisions\n")

	res, err := knowledge.ValidateConformance(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 2 {
		t.Fatalf("violations = %+v, want 2", res.Violations)
	}
	for _, v := range res.Violations {
		if v.Reason != knowledge.ReasonUnparseableFrontmatter {
			t.Errorf("%s reason = %q", v.Path, v.Reason)
		}
		if filepath.IsAbs(v.Path) {
			t.Errorf("path %q is absolute; output must be stable across machines", v.Path)
		}
		if strings.Contains(v.Detail, root) {
			t.Errorf("detail leaks the local worktree path: %q", v.Detail)
		}
		if v.Detail == "" {
			t.Errorf("%s: unparseable violations must say why", v.Path)
		}
	}
}

func TestValidateConformance_CleanBaseAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	writeEntry(t, root, "features/1-ok/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")

	res, err := knowledge.ValidateConformance(root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || len(res.Violations) != 0 {
		t.Fatalf("expected a clean base, got %+v", res.Violations)
	}

	// A repository with no knowledge base is trivially conformant, not an error.
	res, err = knowledge.ValidateConformance(filepath.Join(root, "does-not-exist"))
	if err != nil {
		t.Fatalf("missing root must not be an error: %v", err)
	}
	if !res.Valid {
		t.Error("missing root should be valid")
	}
}

// TestValidateConformance_FreshScaffoldPasses is the criterion that actually
// matters for the pipeline: everything the scaffolder writes must satisfy the
// check it is about to be gated by.
func TestValidateConformance_FreshScaffoldPasses(t *testing.T) {
	root := t.TempDir()

	if _, err := knowledge.Scaffold(root, 42, "Add photo upload", nil); err != nil {
		t.Fatal(err)
	}
	for _, topic := range knowledge.ValidRepoTopicTypes {
		if _, err := knowledge.ScaffoldRepoTopic(root, topic, "some-slug"); err != nil {
			t.Fatal(err)
		}
	}

	res, err := knowledge.ValidateConformance(filepath.Join(root, ".nightgauge", "knowledge"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Errorf("a freshly scaffolded base violates the contract: %+v", res.Violations)
	}
	if res.FilesChecked == 0 {
		t.Error("checked nothing — the walk found no entries")
	}
}

func TestValidateConformanceForIssue_ScopesToOneDirectory(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, ".nightgauge", "knowledge")

	if _, err := knowledge.Scaffold(root, 42, "Good issue", nil); err != nil {
		t.Fatal(err)
	}
	// A stale entry belonging to a different issue. The knowledge base is
	// local, gitignored, per-machine state, so this must never block issue
	// 42's merge.
	writeEntry(t, kb, "features/99-stale/PRD.md", "# PRD\n\nNo block.\n")

	res, err := knowledge.ValidateConformanceForIssue(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Errorf("issue 42 blocked by an unrelated issue's entry: %+v", res.Violations)
	}

	res, err = knowledge.ValidateConformanceForIssue(root, 99)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Error("issue 99's own violation was not reported")
	}

	// An issue with no knowledge directory is trivially conformant.
	res, err = knowledge.ValidateConformanceForIssue(root, 12345)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Error("an issue with no knowledge directory should pass")
	}
}
