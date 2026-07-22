package knowledge_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// writeFile creates a file at path with the given content, creating parent dirs as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// writeWorkspaceConfig writes a minimal .vscode/nightgauge-workspace.yaml.
func writeWorkspaceConfig(t *testing.T, wsRoot string, repos []struct{ name, path string }) {
	t.Helper()
	lines := "workspace:\n  name: test\n\nrepositories:\n"
	for _, r := range repos {
		lines += "  - name: " + r.name + "\n    path: " + r.path + "\n"
	}
	writeFile(t, filepath.Join(wsRoot, ".vscode", "nightgauge-workspace.yaml"), lines)
}

// --- ScanCrossRepoKnowledge tests ---

func TestScanCrossRepoKnowledge_MultipleRepos(t *testing.T) {
	root := mkTempRoot(t)

	// Two sibling repos with knowledge dirs.
	for _, repo := range []struct{ name, relPath string }{
		{"platform", "platform"},
		{"dashboard", "dashboard"},
	} {
		dir := filepath.Join(root, repo.relPath, ".nightgauge", "knowledge", "features", "1-slug")
		writeFile(t, filepath.Join(dir, "PRD.md"), "# PRD")
		writeFile(t, filepath.Join(dir, "decisions.md"), "# Decisions")
	}

	writeWorkspaceConfig(t, root, []struct{ name, path string }{
		{"platform", "./platform"},
		{"dashboard", "./dashboard"},
	})

	entries, err := knowledge.ScanCrossRepoKnowledge(root, 20)
	if err != nil {
		t.Fatalf("ScanCrossRepoKnowledge: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 repo entries, got %d", len(entries))
	}
	for _, e := range entries {
		if len(e.Entries) != 2 {
			t.Errorf("repo %s: expected 2 entries, got %d: %v", e.Repo, len(e.Entries), e.Entries)
		}
	}
}

func TestScanCrossRepoKnowledge_LimitEnforcement(t *testing.T) {
	root := mkTempRoot(t)

	knowledgeDir := filepath.Join(root, "repo-a", ".nightgauge", "knowledge", "features", "1-slug")
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(knowledgeDir, "file"+string(rune('a'+i%26))+".md"), "# content")
	}

	writeWorkspaceConfig(t, root, []struct{ name, path string }{
		{"repo-a", "./repo-a"},
	})

	entries, err := knowledge.ScanCrossRepoKnowledge(root, 10)
	if err != nil {
		t.Fatalf("ScanCrossRepoKnowledge: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 repo entry, got %d", len(entries))
	}
	if len(entries[0].Entries) > 10 {
		t.Errorf("expected at most 10 entries, got %d", len(entries[0].Entries))
	}
}

func TestScanCrossRepoKnowledge_MissingKnowledgeDir(t *testing.T) {
	root := mkTempRoot(t)

	// repo-b exists but has no knowledge dir.
	_ = os.MkdirAll(filepath.Join(root, "repo-b"), 0o755)
	writeWorkspaceConfig(t, root, []struct{ name, path string }{
		{"repo-b", "./repo-b"},
	})

	entries, err := knowledge.ScanCrossRepoKnowledge(root, 20)
	if err != nil {
		t.Fatalf("ScanCrossRepoKnowledge: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for repo with no knowledge dir, got %d", len(entries))
	}
}

func TestScanCrossRepoKnowledge_MissingWorkspaceConfig(t *testing.T) {
	root := mkTempRoot(t)

	entries, err := knowledge.ScanCrossRepoKnowledge(root, 20)
	if err != nil {
		t.Fatalf("ScanCrossRepoKnowledge should not error on missing config, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

func TestScanCrossRepoKnowledge_ExcludesReadme(t *testing.T) {
	root := mkTempRoot(t)

	dir := filepath.Join(root, "repo-x", ".nightgauge", "knowledge", "features", "1-slug")
	writeFile(t, filepath.Join(dir, "README.md"), "# README")
	writeFile(t, filepath.Join(dir, "PRD.md"), "# PRD")

	writeWorkspaceConfig(t, root, []struct{ name, path string }{
		{"repo-x", "./repo-x"},
	})

	entries, err := knowledge.ScanCrossRepoKnowledge(root, 20)
	if err != nil {
		t.Fatalf("ScanCrossRepoKnowledge: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	for _, f := range entries[0].Entries {
		if f == "README.md" {
			t.Error("README.md should be excluded from entries")
		}
	}
}

// --- ScanWorkspaceKB tests ---

func TestScanWorkspaceKB_AllCategories(t *testing.T) {
	root := mkTempRoot(t)

	for _, cat := range []string{"product", "cross-repo", "architecture"} {
		dir := filepath.Join(root, ".nightgauge", "knowledge", cat)
		writeFile(t, filepath.Join(dir, "entry-a.md"), "# A")
		writeFile(t, filepath.Join(dir, "entry-b.md"), "# B")
	}

	entries, err := knowledge.ScanWorkspaceKB(root, 20)
	if err != nil {
		t.Fatalf("ScanWorkspaceKB: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 namespace entries, got %d", len(entries))
	}
}

func TestScanWorkspaceKB_GlobalLimit(t *testing.T) {
	root := mkTempRoot(t)

	// 5 files in each category = 15 total, limit 7 should stop at 7.
	for _, cat := range []string{"product", "cross-repo", "architecture"} {
		dir := filepath.Join(root, ".nightgauge", "knowledge", cat)
		for i := 0; i < 5; i++ {
			writeFile(t, filepath.Join(dir, "entry"+string(rune('a'+i))+".md"), "# x")
		}
	}

	entries, err := knowledge.ScanWorkspaceKB(root, 7)
	if err != nil {
		t.Fatalf("ScanWorkspaceKB: %v", err)
	}
	total := 0
	for _, e := range entries {
		total += len(e.Entries)
	}
	if total > 7 {
		t.Errorf("expected at most 7 total entries, got %d", total)
	}
}

func TestScanWorkspaceKB_MissingCategories(t *testing.T) {
	root := mkTempRoot(t)

	// Only product/ exists.
	dir := filepath.Join(root, ".nightgauge", "knowledge", "product")
	writeFile(t, filepath.Join(dir, "overview.md"), "# Overview")

	entries, err := knowledge.ScanWorkspaceKB(root, 20)
	if err != nil {
		t.Fatalf("ScanWorkspaceKB: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 namespace entry, got %d", len(entries))
	}
	if entries[0].Namespace != "product" {
		t.Errorf("expected namespace 'product', got %q", entries[0].Namespace)
	}
}

func TestScanWorkspaceKB_ExcludesReadme(t *testing.T) {
	root := mkTempRoot(t)

	dir := filepath.Join(root, ".nightgauge", "knowledge", "product")
	writeFile(t, filepath.Join(dir, "README.md"), "# README")
	writeFile(t, filepath.Join(dir, "real.md"), "# Real")

	entries, err := knowledge.ScanWorkspaceKB(root, 20)
	if err != nil {
		t.Fatalf("ScanWorkspaceKB: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	for _, f := range entries[0].Entries {
		if f == "README.md" {
			t.Error("README.md should be excluded")
		}
	}
}

func TestScanWorkspaceKB_EmptyKnowledgeRoot(t *testing.T) {
	root := mkTempRoot(t)

	entries, err := knowledge.ScanWorkspaceKB(root, 20)
	if err != nil {
		t.Fatalf("ScanWorkspaceKB: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}
