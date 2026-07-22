// Package workspace implements workspace-level knowledge directory scaffolding.
// It creates <workspace-root>/.nightgauge/knowledge/<category>/<slug>/
// with PRD.md and decisions.md template files.
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidCategories is the exhaustive list of allowed workspace knowledge categories.
var ValidCategories = []string{"product", "cross-repo", "architecture"}

// CreateInput holds validated inputs for workspace knowledge creation.
type CreateInput struct {
	WorkspaceRoot string   // absolute path to workspace root
	Category      string   // "product" or "cross-repo"
	Slug          string   // normalized slug
	Repos         []string // optional repo names for frontmatter
}

// CreateResult is returned by Create().
type CreateResult struct {
	KnowledgePath string   `json:"knowledge_path"`
	PRDPath       string   `json:"prd_path"`
	DecisionsPath string   `json:"decisions_path"`
	Skipped       bool     `json:"skipped"`       // true when dir already existed
	FilesCreated  []string `json:"files_created"` // paths of newly written files
}

// MarshalJSON implements json.Marshaler so boolean false is always emitted.
func (r CreateResult) MarshalJSON() ([]byte, error) {
	type alias CreateResult
	return json.Marshal(alias(r))
}

// DetectWorkspaceRoot walks up from dir looking for .vscode/nightgauge-workspace.yaml.
// Returns an error if not found after reaching the filesystem root.
func DetectWorkspaceRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	cur := abs
	for {
		marker := filepath.Join(cur, ".vscode", "nightgauge-workspace.yaml")
		if _, err := os.Stat(marker); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("not inside a workspace: .vscode/nightgauge-workspace.yaml not found")
		}
		cur = parent
	}
}

// GenerateSlug normalizes a raw slug: lowercase, replace non-alnum with dash,
// collapse dashes, trim leading/trailing dashes, truncate to 50 chars.
// Exported so CLI layer can display the normalized value to the user.
func GenerateSlug(raw string) string {
	slug := strings.ToLower(raw)
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, slug)

	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")

	if len(slug) > 50 {
		slug = slug[:50]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// IsValidCategory reports whether category is in ValidCategories.
func IsValidCategory(category string) bool {
	for _, v := range ValidCategories {
		if v == category {
			return true
		}
	}
	return false
}

// Create scaffolds the workspace knowledge directory. Idempotent: if the directory
// already exists, it returns the existing paths with Skipped=true and an empty
// FilesCreated list.
func Create(input CreateInput) (CreateResult, error) {
	knowledgePath := filepath.Join(
		input.WorkspaceRoot,
		".nightgauge", "knowledge",
		input.Category,
		input.Slug,
	)

	result := CreateResult{
		KnowledgePath: knowledgePath,
		PRDPath:       filepath.Join(knowledgePath, "PRD.md"),
		DecisionsPath: filepath.Join(knowledgePath, "decisions.md"),
		FilesCreated:  []string{},
	}

	if _, err := os.Stat(knowledgePath); err == nil {
		result.Skipped = true
		return result, nil
	}

	if err := os.MkdirAll(knowledgePath, 0755); err != nil {
		return CreateResult{}, fmt.Errorf("create knowledge directory: %w", err)
	}

	prdContent := generatePRD(input.Repos, input.Slug)
	if err := os.WriteFile(result.PRDPath, []byte(prdContent), 0644); err != nil {
		return CreateResult{}, fmt.Errorf("write PRD.md: %w", err)
	}
	result.FilesCreated = append(result.FilesCreated, "PRD.md")

	decisionsContent := generateDecisions(input.Repos, input.Slug)
	if err := os.WriteFile(result.DecisionsPath, []byte(decisionsContent), 0644); err != nil {
		return CreateResult{}, fmt.Errorf("write decisions.md: %w", err)
	}
	result.FilesCreated = append(result.FilesCreated, "decisions.md")

	return result, nil
}

func generatePRD(repos []string, slug string) string {
	var sb strings.Builder

	if len(repos) > 0 {
		sb.WriteString("---\nrepos:\n")
		for _, r := range repos {
			fmt.Fprintf(&sb, "  - %s\n", r)
		}
		sb.WriteString("---\n\n")
	}

	fmt.Fprintf(&sb, "# PRD: %s\n\n", slug)
	sb.WriteString("## Summary\n\n")
	sb.WriteString("<!-- TODO: Describe the product feature or roadmap item -->\n\n")
	sb.WriteString("## Goals\n\n")
	sb.WriteString("<!-- TODO: List measurable goals -->\n\n")
	sb.WriteString("## Acceptance Criteria\n\n")
	sb.WriteString("<!-- TODO: List acceptance criteria -->\n\n")
	sb.WriteString("## Technical Notes\n\n")
	sb.WriteString("<!-- TODO: Add technical context -->\n")

	return sb.String()
}

func generateDecisions(repos []string, slug string) string {
	var sb strings.Builder

	if len(repos) > 0 {
		sb.WriteString("---\nrepos:\n")
		for _, r := range repos {
			fmt.Fprintf(&sb, "  - %s\n", r)
		}
		sb.WriteString("---\n\n")
	}

	fmt.Fprintf(&sb, "# Decisions: %s\n\n", slug)
	sb.WriteString("## Architecture Decisions\n\n")
	sb.WriteString("| Decision | Options Considered | Selected | Rationale |\n")
	sb.WriteString("| -------- | ------------------ | -------- | --------- |\n")

	return sb.String()
}
