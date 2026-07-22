package workspace

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

//go:embed seeds/*.md
var seedFS embed.FS

// seedManifest maps each embedded seed file (relative to the seeds/ dir) to
// its target location under the workspace knowledge root. Keys are source
// basenames; values are workspace-root-relative destinations.
//
// Category README files are prefixed with "<category>-README.md" in the embed
// FS and land as "README.md" inside their category directory.
var seedManifest = map[string]string{
	"product-README.md":        "product/README.md",
	"cross-repo-README.md":     "cross-repo/README.md",
	"architecture-README.md":   "architecture/README.md",
	"product-positioning.md":   "product/product-positioning.md",
	"multi-surface.md":         "product/multi-surface.md",
	"platform-api-contract.md": "cross-repo/platform-api-contract.md",
	"shared-types.md":          "cross-repo/shared-types.md",
	"auth-flow.md":             "cross-repo/auth-flow.md",
	"ecosystem-topology.md":    "architecture/ecosystem-topology.md",
	"go-ts-parity.md":          "architecture/go-ts-parity.md",
}

// InitTreeInput holds validated inputs for workspace knowledge tree init.
type InitTreeInput struct {
	// WorkspaceRoot is the absolute path to the workspace root (the
	// directory holding .vscode/nightgauge-workspace.yaml).
	WorkspaceRoot string
}

// InitTreeResult is returned by InitTree().
type InitTreeResult struct {
	// KnowledgePath is the absolute path to the workspace knowledge root
	// (<WorkspaceRoot>/.nightgauge/knowledge).
	KnowledgePath string `json:"knowledge_path"`
	// CategoriesCreated lists category directories that existed or were
	// created by this call (always the full set on success).
	CategoriesCreated []string `json:"categories_created"`
	// FilesCreated lists workspace-root-relative paths of files newly
	// written by this call. Existing files are never overwritten.
	FilesCreated []string `json:"files_created"`
	// Skipped is true when no files were written because every seed
	// target already exists.
	Skipped bool `json:"skipped"`
}

// MarshalJSON ensures Skipped=false is always emitted.
func (r InitTreeResult) MarshalJSON() ([]byte, error) {
	type alias InitTreeResult
	return json.Marshal(alias(r))
}

// InitTree scaffolds the three-category workspace knowledge tree at
// <WorkspaceRoot>/.nightgauge/knowledge/ with embedded seed content.
//
// Idempotent: existing files are never overwritten. Partial state is fine —
// only missing files are written. Returns Skipped=true only when every seed
// target already existed.
func InitTree(input InitTreeInput) (InitTreeResult, error) {
	if input.WorkspaceRoot == "" {
		return InitTreeResult{}, fmt.Errorf("workspace root is required")
	}

	knowledgePath := filepath.Join(input.WorkspaceRoot, ".nightgauge", "knowledge")

	categories := []string{"product", "cross-repo", "architecture"}
	for _, cat := range categories {
		if err := os.MkdirAll(filepath.Join(knowledgePath, cat), 0755); err != nil {
			return InitTreeResult{}, fmt.Errorf("create category %q: %w", cat, err)
		}
	}

	// Deterministic iteration order so FilesCreated is stable across runs.
	sources := make([]string, 0, len(seedManifest))
	for src := range seedManifest {
		sources = append(sources, src)
	}
	sort.Strings(sources)

	filesCreated := make([]string, 0, len(sources))
	for _, src := range sources {
		target := seedManifest[src]
		absTarget := filepath.Join(knowledgePath, target)

		if _, err := os.Stat(absTarget); err == nil {
			continue
		}

		data, err := seedFS.ReadFile("seeds/" + src)
		if err != nil {
			return InitTreeResult{}, fmt.Errorf("read embedded seed %q: %w", src, err)
		}

		if err := os.MkdirAll(filepath.Dir(absTarget), 0755); err != nil {
			return InitTreeResult{}, fmt.Errorf("create parent dir for %q: %w", target, err)
		}
		if err := os.WriteFile(absTarget, data, 0644); err != nil {
			return InitTreeResult{}, fmt.Errorf("write %q: %w", target, err)
		}
		filesCreated = append(filesCreated, target)
	}

	return InitTreeResult{
		KnowledgePath:     knowledgePath,
		CategoriesCreated: categories,
		FilesCreated:      filesCreated,
		Skipped:           len(filesCreated) == 0,
	}, nil
}
