package knowledge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// mkTempRoot creates a temporary directory and returns a cleanup function.
func mkTempRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "knowledge-test-*")
	if err != nil {
		t.Fatalf("mkTempRoot: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestScaffold_CreatesDirectoryAndFiles(t *testing.T) {
	root := mkTempRoot(t)

	result, err := knowledge.Scaffold(root, 42, "Add photo upload", []string{"User can upload JPEG", "Max 5 MB"})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	if result.Skipped {
		t.Error("expected Skipped=false on first call")
	}
	if len(result.FilesCreated) != 2 {
		t.Errorf("expected 2 files created, got %d: %v", len(result.FilesCreated), result.FilesCreated)
	}

	prdAbs := filepath.Join(root, result.PRDPath)
	prdContent, err := os.ReadFile(prdAbs)
	if err != nil {
		t.Fatalf("read PRD.md: %v", err)
	}
	if !strings.Contains(string(prdContent), "# PRD: #42 — Add photo upload") {
		t.Errorf("PRD.md missing expected title, got:\n%s", prdContent)
	}
	if !strings.Contains(string(prdContent), "User can upload JPEG") {
		t.Errorf("PRD.md missing acceptance criteria, got:\n%s", prdContent)
	}
	// The scaffold seeds the full PRD structure, including the embedded TRD
	// (Technical Approach) and embedded QRD (Quality & Non-Functional
	// Requirements) sections. These headings MUST stay in parity with the
	// TypeScript SDK's KnowledgeService.renderPrdBody().
	for _, heading := range []string{
		"## Summary",
		"## User Story",
		"## Acceptance Criteria",
		"## Technical Approach",
		"## Quality & Non-Functional Requirements",
		"## Out of Scope",
		"## Status",
	} {
		if !strings.Contains(string(prdContent), heading) {
			t.Errorf("PRD.md missing %q section, got:\n%s", heading, prdContent)
		}
	}

	decisionsAbs := filepath.Join(root, result.DecisionsPath)
	decisionsContent, err := os.ReadFile(decisionsAbs)
	if err != nil {
		t.Fatalf("read decisions.md: %v", err)
	}
	if !strings.Contains(string(decisionsContent), "# Decisions: #42 — Add photo upload") {
		t.Errorf("decisions.md missing expected title, got:\n%s", decisionsContent)
	}
}

func TestScaffold_Idempotent(t *testing.T) {
	root := mkTempRoot(t)

	first, err := knowledge.Scaffold(root, 100, "Test Issue", nil)
	if err != nil {
		t.Fatalf("first Scaffold: %v", err)
	}
	if first.Skipped {
		t.Error("expected Skipped=false on first call")
	}

	second, err := knowledge.Scaffold(root, 100, "Test Issue", nil)
	if err != nil {
		t.Fatalf("second Scaffold: %v", err)
	}
	if !second.Skipped {
		t.Error("expected Skipped=true on second call")
	}
	if second.KnowledgePath != first.KnowledgePath {
		t.Errorf("knowledge path changed between calls: %q vs %q", first.KnowledgePath, second.KnowledgePath)
	}

	// Verify original files are unchanged.
	prdAbs := filepath.Join(root, first.PRDPath)
	content, err := os.ReadFile(prdAbs)
	if err != nil {
		t.Fatalf("read PRD.md after idempotent call: %v", err)
	}
	if !strings.Contains(string(content), "# PRD: #100 — Test Issue") {
		t.Errorf("PRD.md was overwritten by second call")
	}
}

func TestPruneEmpty_DeletesEmptyFiles(t *testing.T) {
	root := mkTempRoot(t)

	// Create a knowledge entry with only boilerplate content.
	result, err := knowledge.Scaffold(root, 200, "Empty Feature", nil)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	if result.Skipped {
		t.Fatal("expected directory to be created")
	}

	pruned, err := knowledge.PruneEmpty(root, false)
	if err != nil {
		t.Fatalf("PruneEmpty: %v", err)
	}
	if len(pruned) != 1 {
		t.Errorf("expected 1 pruned entry, got %d: %v", len(pruned), pruned)
	}

	// Verify directory was removed.
	absPath := filepath.Join(root, result.KnowledgePath)
	if _, statErr := os.Stat(absPath); !os.IsNotExist(statErr) {
		t.Error("expected directory to be deleted after prune")
	}
}

func TestPruneEmpty_DryRun(t *testing.T) {
	root := mkTempRoot(t)

	result, err := knowledge.Scaffold(root, 201, "Dry Run Feature", nil)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	pruned, err := knowledge.PruneEmpty(root, true)
	if err != nil {
		t.Fatalf("PruneEmpty dry-run: %v", err)
	}
	if len(pruned) != 1 {
		t.Errorf("expected 1 pruned entry in dry-run, got %d", len(pruned))
	}

	// Verify directory still exists in dry-run.
	absPath := filepath.Join(root, result.KnowledgePath)
	if _, statErr := os.Stat(absPath); statErr != nil {
		t.Error("expected directory to survive dry-run prune")
	}
}

func TestPruneEmpty_PreservesSubstantiveFiles(t *testing.T) {
	root := mkTempRoot(t)

	result, err := knowledge.Scaffold(root, 300, "Substantive Feature", nil)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	// Overwrite PRD.md with substantive content (≥30 real chars).
	prdAbs := filepath.Join(root, result.PRDPath)
	substantiveContent := "This PRD has enough real content to pass the substantive threshold.\n"
	if err := os.WriteFile(prdAbs, []byte(substantiveContent), 0o644); err != nil {
		t.Fatalf("write substantive PRD.md: %v", err)
	}

	pruned, err := knowledge.PruneEmpty(root, false)
	if err != nil {
		t.Fatalf("PruneEmpty: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned entries for substantive file, got %d: %v", len(pruned), pruned)
	}

	// Verify directory was NOT removed.
	absPath := filepath.Join(root, result.KnowledgePath)
	if _, statErr := os.Stat(absPath); statErr != nil {
		t.Error("expected substantive directory to survive prune")
	}
}

func TestGenerateIndex_WritesIndex(t *testing.T) {
	root := mkTempRoot(t)

	// Scaffold two entries.
	_, err := knowledge.Scaffold(root, 10, "First Feature", nil)
	if err != nil {
		t.Fatalf("Scaffold 10: %v", err)
	}
	_, err = knowledge.Scaffold(root, 20, "Second Feature", nil)
	if err != nil {
		t.Fatalf("Scaffold 20: %v", err)
	}

	relPath, err := knowledge.GenerateIndex(root)
	if err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}

	readmePath := filepath.Join(root, relPath)
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	body := string(content)
	if !strings.Contains(body, "# Knowledge Base Index") {
		t.Error("README.md missing index heading")
	}
	if !strings.Contains(body, "#10") {
		t.Errorf("README.md missing entry for issue #10:\n%s", body)
	}
	if !strings.Contains(body, "#20") {
		t.Errorf("README.md missing entry for issue #20:\n%s", body)
	}
	if !strings.Contains(body, "Total entries:** 2") {
		t.Errorf("README.md missing total entries count:\n%s", body)
	}
}

func TestGenerateIndex_EmptyKnowledgeRoot(t *testing.T) {
	root := mkTempRoot(t)

	// GenerateIndex on empty root should not error.
	relPath, err := knowledge.GenerateIndex(root)
	if err != nil {
		t.Fatalf("GenerateIndex on empty root: %v", err)
	}
	if relPath == "" {
		t.Error("expected a non-empty README.md path")
	}
}

func TestStats_EmptyReturnsEmptySlice(t *testing.T) {
	root := mkTempRoot(t)
	stats, err := knowledge.Stats(root)
	if err != nil {
		t.Fatalf("Stats on empty root: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("expected 0 entries, got %d", len(stats))
	}
}

func TestStats_ReturnsPerIssueByteCounts(t *testing.T) {
	root := mkTempRoot(t)

	// Scaffold two issues.
	_, err := knowledge.Scaffold(root, 42, "Add photo upload", nil)
	if err != nil {
		t.Fatalf("Scaffold 42: %v", err)
	}
	_, err = knowledge.Scaffold(root, 99, "Fix login bug", nil)
	if err != nil {
		t.Fatalf("Scaffold 99: %v", err)
	}

	// Write a fake outcomes.md for issue 42.
	outcomesPath := filepath.Join(root, ".nightgauge", "knowledge", "features", "42-add-photo-upload", "outcomes.md")
	if writeErr := os.WriteFile(outcomesPath, []byte("## Outcome\nAll good.\n"), 0o644); writeErr != nil {
		t.Fatalf("write outcomes.md: %v", writeErr)
	}

	stats, err := knowledge.Stats(root)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(stats))
	}

	// Find issue 42 stats.
	var s42 *knowledge.IssueStats
	for i := range stats {
		if stats[i].IssueNumber == 42 {
			s42 = &stats[i]
		}
	}
	if s42 == nil {
		t.Fatal("stats missing entry for issue #42")
	}
	if s42.PRDBytes == 0 {
		t.Error("expected non-zero PRDBytes for issue #42")
	}
	if s42.DecisionsBytes == 0 {
		t.Error("expected non-zero DecisionsBytes for issue #42")
	}
	if s42.OutcomesBytes == 0 {
		t.Error("expected non-zero OutcomesBytes for issue #42 (outcomes.md was written)")
	}
	if s42.LastWrite == "" {
		t.Error("expected non-empty LastWrite for issue #42")
	}
}

func TestScaffoldRepoTopic_CreatesEntryAndCategoryFiles(t *testing.T) {
	cases := []struct {
		name      string
		topicType knowledge.RepoTopicType
		slug      string
	}{
		{"architecture entry", knowledge.RepoTopicArchitecture, "six-stage-pipeline"},
		{"glossary entry", knowledge.RepoTopicGlossary, "knowledge-path"},
		{"runbook entry", knowledge.RepoTopicRunbook, "recover-stuck-autonomous"},
		{"post-mortem entry", knowledge.RepoTopicPostMortem, "incident-2026-01"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := mkTempRoot(t)

			result, err := knowledge.ScaffoldRepoTopic(root, tc.topicType, tc.slug)
			if err != nil {
				t.Fatalf("ScaffoldRepoTopic: %v", err)
			}

			if result.Skipped {
				t.Error("expected Skipped=false on first call")
			}

			// Entry file must exist.
			entryAbs := filepath.Join(root, result.FilePath)
			content, err := os.ReadFile(entryAbs)
			if err != nil {
				t.Fatalf("read entry file: %v", err)
			}
			if !strings.Contains(string(content), tc.slug) {
				t.Errorf("entry file missing slug %q:\n%s", tc.slug, content)
			}
			if !strings.Contains(string(content), "---") {
				t.Errorf("entry file missing YAML frontmatter:\n%s", content)
			}

			// README.md and _template.md must exist (category was new).
			categoryDir := filepath.Join(root, result.KnowledgePath)
			for _, fname := range []string{"README.md", "_template.md"} {
				if _, err := os.Stat(filepath.Join(categoryDir, fname)); err != nil {
					t.Errorf("expected %s in category dir: %v", fname, err)
				}
			}

			// FilesCreated must include the entry + README + _template.
			if len(result.FilesCreated) < 3 {
				t.Errorf("expected ≥3 files created, got %d: %v", len(result.FilesCreated), result.FilesCreated)
			}
		})
	}
}

func TestScaffoldRepoTopic_Idempotent(t *testing.T) {
	root := mkTempRoot(t)

	first, err := knowledge.ScaffoldRepoTopic(root, knowledge.RepoTopicGlossary, "wave")
	if err != nil {
		t.Fatalf("first ScaffoldRepoTopic: %v", err)
	}
	if first.Skipped {
		t.Error("expected Skipped=false on first call")
	}

	second, err := knowledge.ScaffoldRepoTopic(root, knowledge.RepoTopicGlossary, "wave")
	if err != nil {
		t.Fatalf("second ScaffoldRepoTopic: %v", err)
	}
	if !second.Skipped {
		t.Error("expected Skipped=true on second call")
	}
	if second.FilePath != first.FilePath {
		t.Errorf("file path changed between calls: %q vs %q", first.FilePath, second.FilePath)
	}

	// Verify original file is unchanged.
	entryAbs := filepath.Join(root, first.FilePath)
	content, err := os.ReadFile(entryAbs)
	if err != nil {
		t.Fatalf("read entry file after idempotent call: %v", err)
	}
	if !strings.Contains(string(content), "wave") {
		t.Errorf("entry file content changed after second call")
	}
}

func TestScaffoldRepoTopic_SecondEntrySkipsReadmeAndTemplate(t *testing.T) {
	root := mkTempRoot(t)

	first, err := knowledge.ScaffoldRepoTopic(root, knowledge.RepoTopicRunbook, "first-runbook")
	if err != nil {
		t.Fatalf("first ScaffoldRepoTopic: %v", err)
	}

	second, err := knowledge.ScaffoldRepoTopic(root, knowledge.RepoTopicRunbook, "second-runbook")
	if err != nil {
		t.Fatalf("second ScaffoldRepoTopic: %v", err)
	}
	if second.Skipped {
		t.Error("expected Skipped=false for a new slug in existing category")
	}

	// Only the entry file should be in FilesCreated (not README or _template again).
	if len(second.FilesCreated) != 1 {
		t.Errorf("expected 1 file created for second entry in existing category, got %d: %v", len(second.FilesCreated), second.FilesCreated)
	}
	if second.FilesCreated[0] != "second-runbook.md" {
		t.Errorf("expected second-runbook.md, got %q", second.FilesCreated[0])
	}
	_ = first
}

func TestIsValidRepoTopicType(t *testing.T) {
	valid := []knowledge.RepoTopicType{
		knowledge.RepoTopicArchitecture,
		knowledge.RepoTopicGlossary,
		knowledge.RepoTopicRunbook,
		knowledge.RepoTopicPostMortem,
	}
	for _, v := range valid {
		if !knowledge.IsValidRepoTopicType(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if knowledge.IsValidRepoTopicType("invalid") {
		t.Error("expected \"invalid\" to be invalid")
	}
}

func TestScaffold_EmitsKnowledgeScaffoldEvent(t *testing.T) {
	root := mkTempRoot(t)

	var buf strings.Builder
	_, err := knowledge.Scaffold(root, 123, "Test telemetry", nil, &buf)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "[knowledge]") {
		t.Errorf("expected [knowledge] prefix in telemetry output, got: %q", out)
	}
	if !strings.Contains(out, "knowledge.scaffold") {
		t.Errorf("expected knowledge.scaffold event in output, got: %q", out)
	}
	if !strings.Contains(out, "issue=123") {
		t.Errorf("expected issue=123 in telemetry output, got: %q", out)
	}
}

func TestScaffoldWithConfig_EnabledTrue(t *testing.T) {
	root := mkTempRoot(t)

	result, err := knowledge.ScaffoldWithConfig(root, 500, "Config Enabled", []string{"Criterion A"}, true, true)
	if err != nil {
		t.Fatalf("ScaffoldWithConfig: %v", err)
	}
	if result.Skipped {
		t.Errorf("expected Skipped=false when knowledgeEnabled=true, got skip_reason=%q", result.SkipReason)
	}
	if result.SkipReason != "" {
		t.Errorf("expected empty SkipReason, got %q", result.SkipReason)
	}
	if len(result.FilesCreated) != 2 {
		t.Errorf("expected 2 files created, got %d", len(result.FilesCreated))
	}

	// Verify files exist on disk.
	prdAbs := filepath.Join(root, result.PRDPath)
	if _, err := os.Stat(prdAbs); err != nil {
		t.Errorf("PRD.md not found: %v", err)
	}
}

func TestScaffoldWithConfig_EnabledFalse_SkipsSilently(t *testing.T) {
	root := mkTempRoot(t)

	result, err := knowledge.ScaffoldWithConfig(root, 501, "Config Disabled", nil, false, true)
	if err != nil {
		t.Fatalf("ScaffoldWithConfig: %v", err)
	}
	if !result.Skipped {
		t.Error("expected Skipped=true when knowledgeEnabled=false")
	}
	if result.SkipReason != "knowledge.enabled=false in config" {
		t.Errorf("unexpected SkipReason: %q", result.SkipReason)
	}
	if result.KnowledgePath != "" {
		t.Errorf("expected empty KnowledgePath when skipped, got %q", result.KnowledgePath)
	}

	// Verify NO files were created.
	knowledgeDir := filepath.Join(root, ".nightgauge", "knowledge")
	if _, err := os.Stat(knowledgeDir); err == nil {
		t.Error("expected no knowledge directory to be created when disabled")
	}
}

func TestScaffoldWithConfig_IdempotentVsDisabled_DistinctSkipReasons(t *testing.T) {
	root := mkTempRoot(t)

	// First call — enabled, creates files.
	first, err := knowledge.ScaffoldWithConfig(root, 502, "Idempotent Test", nil, true, true)
	if err != nil {
		t.Fatalf("first ScaffoldWithConfig: %v", err)
	}
	if first.Skipped {
		t.Fatal("expected Skipped=false on first call")
	}

	// Second call — enabled, directory exists → idempotent skip (no SkipReason).
	second, err := knowledge.ScaffoldWithConfig(root, 502, "Idempotent Test", nil, true, true)
	if err != nil {
		t.Fatalf("second ScaffoldWithConfig: %v", err)
	}
	if !second.Skipped {
		t.Error("expected Skipped=true on second call (directory exists)")
	}
	if second.SkipReason != "" {
		t.Errorf("idempotent skip should have empty SkipReason, got %q", second.SkipReason)
	}

	// Third call — disabled → config skip (has SkipReason).
	third, err := knowledge.ScaffoldWithConfig(root, 503, "New Issue Disabled", nil, false, true)
	if err != nil {
		t.Fatalf("third ScaffoldWithConfig: %v", err)
	}
	if !third.Skipped {
		t.Error("expected Skipped=true when disabled")
	}
	if third.SkipReason == "" {
		t.Error("expected non-empty SkipReason when disabled")
	}

	// Confirm the two skip reasons are distinct.
	if second.SkipReason == third.SkipReason {
		t.Errorf("idempotent and disabled skips should have different SkipReason values; both got %q", second.SkipReason)
	}
}
