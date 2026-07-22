package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/workspace"
)

// makeWorkspace creates a temp dir with the workspace marker file.
func makeWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	vscodeDir := filepath.Join(root, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		t.Fatalf("create .vscode: %v", err)
	}
	marker := filepath.Join(vscodeDir, "nightgauge-workspace.yaml")
	if err := os.WriteFile(marker, []byte("repositories: []\n"), 0644); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	return root
}

func TestCreate_HappyPath(t *testing.T) {
	root := makeWorkspace(t)

	result, err := workspace.Create(workspace.CreateInput{
		WorkspaceRoot: root,
		Category:      "product",
		Slug:          "my-feature",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	expectedPath := filepath.Join(root, ".nightgauge", "knowledge", "product", "my-feature")
	if result.KnowledgePath != expectedPath {
		t.Errorf("KnowledgePath = %q, want %q", result.KnowledgePath, expectedPath)
	}
	if result.Skipped {
		t.Error("Skipped = true, want false")
	}
	if len(result.FilesCreated) != 2 {
		t.Errorf("FilesCreated = %v, want 2 entries", result.FilesCreated)
	}

	// Verify files exist on disk
	if _, err := os.Stat(result.PRDPath); err != nil {
		t.Errorf("PRD.md not found: %v", err)
	}
	if _, err := os.Stat(result.DecisionsPath); err != nil {
		t.Errorf("decisions.md not found: %v", err)
	}
}

func TestCreate_Idempotency(t *testing.T) {
	root := makeWorkspace(t)
	input := workspace.CreateInput{
		WorkspaceRoot: root,
		Category:      "product",
		Slug:          "idempotent-slug",
	}

	_, err := workspace.Create(input)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Overwrite PRD.md to detect if second call clobbers it
	prdPath := filepath.Join(root, ".nightgauge", "knowledge", "product", "idempotent-slug", "PRD.md")
	sentinel := "sentinel content — must survive second Create"
	if err := os.WriteFile(prdPath, []byte(sentinel), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	result2, err := workspace.Create(input)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !result2.Skipped {
		t.Error("Skipped = false on second call, want true")
	}
	if len(result2.FilesCreated) != 0 {
		t.Errorf("FilesCreated = %v, want empty on skip", result2.FilesCreated)
	}

	// Sentinel must be unchanged
	data, _ := os.ReadFile(prdPath)
	if string(data) != sentinel {
		t.Errorf("PRD.md was overwritten on second Create: got %q", string(data))
	}
}

func TestCreate_WithRepos_Frontmatter(t *testing.T) {
	root := makeWorkspace(t)

	_, err := workspace.Create(workspace.CreateInput{
		WorkspaceRoot: root,
		Category:      "cross-repo",
		Slug:          "auth-flow",
		Repos:         []string{"nightgauge", "acme-platform"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prdPath := filepath.Join(root, ".nightgauge", "knowledge", "cross-repo", "auth-flow", "PRD.md")
	data, err := os.ReadFile(prdPath)
	if err != nil {
		t.Fatalf("read PRD.md: %v", err)
	}
	prd := string(data)
	if !strings.HasPrefix(prd, "---\n") {
		t.Errorf("PRD.md missing frontmatter, got:\n%s", prd)
	}
	if !strings.Contains(prd, "- nightgauge\n") {
		t.Errorf("PRD.md missing repo in frontmatter, got:\n%s", prd)
	}
	if !strings.Contains(prd, "- acme-platform\n") {
		t.Errorf("PRD.md missing second repo in frontmatter, got:\n%s", prd)
	}

	decPath := filepath.Join(root, ".nightgauge", "knowledge", "cross-repo", "auth-flow", "decisions.md")
	dec, _ := os.ReadFile(decPath)
	if !strings.HasPrefix(string(dec), "---\n") {
		t.Errorf("decisions.md missing frontmatter, got:\n%s", dec)
	}
}

func TestCreate_NoRepos_NoFrontmatter(t *testing.T) {
	root := makeWorkspace(t)

	_, err := workspace.Create(workspace.CreateInput{
		WorkspaceRoot: root,
		Category:      "product",
		Slug:          "no-repos",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prdPath := filepath.Join(root, ".nightgauge", "knowledge", "product", "no-repos", "PRD.md")
	data, _ := os.ReadFile(prdPath)
	if strings.HasPrefix(string(data), "---") {
		t.Errorf("PRD.md should have no frontmatter when repos is empty, got:\n%s", string(data))
	}
}

func TestGenerateSlug(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"My Feature", "my-feature"},
		{"hello-world", "hello-world"},
		{"Hello  World!", "hello-world"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"ALLCAPS", "allcaps"},
		{"slug_with_underscores", "slug-with-underscores"},
		{strings.Repeat("a", 60), strings.Repeat("a", 50)},
	}
	for _, tc := range tests {
		got := workspace.GenerateSlug(tc.raw)
		if got != tc.want {
			t.Errorf("GenerateSlug(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDetectWorkspaceRoot_Found(t *testing.T) {
	root := makeWorkspace(t)
	// Detect from a subdirectory inside the workspace
	subdir := filepath.Join(root, "packages", "my-pkg")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := workspace.DetectWorkspaceRoot(subdir)
	if err != nil {
		t.Fatalf("DetectWorkspaceRoot: %v", err)
	}
	if got != root {
		t.Errorf("DetectWorkspaceRoot = %q, want %q", got, root)
	}
}

func TestDetectWorkspaceRoot_NotFound(t *testing.T) {
	dir := t.TempDir() // no workspace marker
	_, err := workspace.DetectWorkspaceRoot(dir)
	if err == nil {
		t.Error("expected error when not in a workspace")
	}
	if !strings.Contains(err.Error(), "not inside a workspace") {
		t.Errorf("error = %q, want 'not inside a workspace'", err.Error())
	}
}

func TestIsValidCategory(t *testing.T) {
	if !workspace.IsValidCategory("product") {
		t.Error("product should be valid")
	}
	if !workspace.IsValidCategory("cross-repo") {
		t.Error("cross-repo should be valid")
	}
	if !workspace.IsValidCategory("architecture") {
		t.Error("architecture should be valid")
	}
	if workspace.IsValidCategory("features") {
		t.Error("features should not be valid")
	}
	if workspace.IsValidCategory("") {
		t.Error("empty string should not be valid")
	}
}
