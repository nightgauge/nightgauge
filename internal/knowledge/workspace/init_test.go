package workspace_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/workspace"
)

// Full set of workspace-root-relative paths that InitTree must materialize on
// a fresh workspace.
var expectedSeedPaths = []string{
	"architecture/README.md",
	"architecture/ecosystem-topology.md",
	"architecture/go-ts-parity.md",
	"cross-repo/README.md",
	"cross-repo/auth-flow.md",
	"cross-repo/platform-api-contract.md",
	"cross-repo/shared-types.md",
	"product/README.md",
	"product/multi-surface.md",
	"product/product-positioning.md",
}

func TestInitTree_FreshWorkspace(t *testing.T) {
	root := makeWorkspace(t)

	result, err := workspace.InitTree(workspace.InitTreeInput{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("InitTree: %v", err)
	}

	expectedKnowledge := filepath.Join(root, ".nightgauge", "knowledge")
	if result.KnowledgePath != expectedKnowledge {
		t.Errorf("KnowledgePath = %q, want %q", result.KnowledgePath, expectedKnowledge)
	}
	if !filepath.IsAbs(result.KnowledgePath) {
		t.Errorf("KnowledgePath should be absolute, got %q", result.KnowledgePath)
	}
	if !strings.HasSuffix(result.KnowledgePath, filepath.Join(".nightgauge", "knowledge")) {
		t.Errorf("KnowledgePath should end in .nightgauge/knowledge, got %q", result.KnowledgePath)
	}

	if result.Skipped {
		t.Error("Skipped = true on fresh workspace, want false")
	}

	wantCats := []string{"product", "cross-repo", "architecture"}
	if len(result.CategoriesCreated) != len(wantCats) {
		t.Errorf("CategoriesCreated = %v, want %v", result.CategoriesCreated, wantCats)
	}

	sort.Strings(result.FilesCreated)
	if !equalStringSlices(result.FilesCreated, expectedSeedPaths) {
		t.Errorf("FilesCreated = %v\nwant %v", result.FilesCreated, expectedSeedPaths)
	}

	// Verify every seed exists on disk under the workspace root.
	for _, rel := range expectedSeedPaths {
		abs := filepath.Join(expectedKnowledge, rel)
		info, err := os.Stat(abs)
		if err != nil {
			t.Errorf("seed %s not found: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("seed %s is empty", rel)
		}
	}
}

func TestInitTree_Idempotency(t *testing.T) {
	root := makeWorkspace(t)
	input := workspace.InitTreeInput{WorkspaceRoot: root}

	if _, err := workspace.InitTree(input); err != nil {
		t.Fatalf("first InitTree: %v", err)
	}

	// Overwrite one seed with a sentinel that must survive the second call.
	sentinelRel := "product/product-positioning.md"
	sentinelPath := filepath.Join(root, ".nightgauge", "knowledge", sentinelRel)
	sentinel := "sentinel — must not be overwritten\n"
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	result2, err := workspace.InitTree(input)
	if err != nil {
		t.Fatalf("second InitTree: %v", err)
	}

	if !result2.Skipped {
		t.Errorf("Skipped = false on second InitTree, want true; FilesCreated=%v", result2.FilesCreated)
	}
	if len(result2.FilesCreated) != 0 {
		t.Errorf("FilesCreated = %v on second call, want empty", result2.FilesCreated)
	}

	data, _ := os.ReadFile(sentinelPath)
	if string(data) != sentinel {
		t.Errorf("seed was overwritten: got %q, want %q", string(data), sentinel)
	}
}

func TestInitTree_PreservesCustomFile(t *testing.T) {
	root := makeWorkspace(t)

	// User drops a custom file BEFORE running InitTree.
	customPath := filepath.Join(root, ".nightgauge", "knowledge", "product", "custom.md")
	if err := os.MkdirAll(filepath.Dir(customPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	custom := "# Custom — user-authored\n"
	if err := os.WriteFile(customPath, []byte(custom), 0644); err != nil {
		t.Fatalf("write custom: %v", err)
	}

	if _, err := workspace.InitTree(workspace.InitTreeInput{WorkspaceRoot: root}); err != nil {
		t.Fatalf("InitTree: %v", err)
	}

	data, _ := os.ReadFile(customPath)
	if string(data) != custom {
		t.Errorf("custom.md was modified; got %q", string(data))
	}
}

func TestInitTree_PartialState(t *testing.T) {
	root := makeWorkspace(t)

	// Pre-create only the product/ subset; architecture/ and cross-repo/ are missing.
	productDir := filepath.Join(root, ".nightgauge", "knowledge", "product")
	if err := os.MkdirAll(productDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"README.md", "product-positioning.md", "multi-surface.md"} {
		path := filepath.Join(productDir, name)
		if err := os.WriteFile(path, []byte("existing\n"), 0644); err != nil {
			t.Fatalf("write existing: %v", err)
		}
	}

	result, err := workspace.InitTree(workspace.InitTreeInput{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("InitTree: %v", err)
	}

	if result.Skipped {
		t.Error("Skipped = true, want false (architecture/ and cross-repo/ were missing)")
	}

	// Every filesCreated path must be under architecture/ or cross-repo/ —
	// none under product/.
	for _, f := range result.FilesCreated {
		if strings.HasPrefix(f, "product/") {
			t.Errorf("FilesCreated includes product/ file %q but product/ was pre-populated", f)
		}
	}

	// And the three pre-existing product files still say "existing".
	for _, name := range []string{"README.md", "product-positioning.md", "multi-surface.md"} {
		data, _ := os.ReadFile(filepath.Join(productDir, name))
		if string(data) != "existing\n" {
			t.Errorf("pre-existing product/%s was overwritten", name)
		}
	}
}

func TestInitTree_EmptyWorkspaceRoot(t *testing.T) {
	_, err := workspace.InitTree(workspace.InitTreeInput{WorkspaceRoot: ""})
	if err == nil {
		t.Error("expected error with empty WorkspaceRoot")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
