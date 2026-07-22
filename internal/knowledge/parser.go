// Package knowledge implements workspace-level knowledge file parsing and validation.
// Knowledge files use optional YAML frontmatter delimited by --- sentinels to declare
// which repositories a knowledge entry applies to. See docs/KNOWLEDGE_BASE.md for the
// full schema.
package knowledge

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FrontmatterBlock holds parsed YAML frontmatter from a knowledge file.
// When Repos is nil or empty, the entry applies to all repositories in the workspace.
type FrontmatterBlock struct {
	// Repos lists the repository names this knowledge entry applies to.
	// Nil or empty means workspace-wide (applies to all repos).
	Repos []string `yaml:"repos"`

	// Tags holds optional topic tags for discovery.
	Tags []string `yaml:"tags"`

	// Related holds related issue/PR references, e.g. ["#2090", "#2091"].
	Related []string `yaml:"related"`

	// Status is the lifecycle status of this knowledge entry: draft, stable, or superseded.
	// An unrecognized value is treated as a warning, not a hard error, for forward compatibility.
	Status string `yaml:"status"`

	// SupersededBy holds the issue/PR reference that supersedes this entry (when Status=superseded).
	SupersededBy string `yaml:"superseded_by"`

	// Raw holds the full parsed frontmatter as a map for forward-compatibility
	// with future frontmatter fields.
	Raw map[string]interface{}
}

// WorkspaceRepository is a minimal interface for repo name validation.
// The full struct lives in the TypeScript layer; Go uses this subset.
type WorkspaceRepository struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	Role string `yaml:"role"`
}

// WorkspaceConfig holds the relevant subset of workspace config for repo validation.
type WorkspaceConfig struct {
	Repositories []WorkspaceRepository `yaml:"repositories"`
}

// ValidationError is returned when one or more repo names in frontmatter are
// not declared in the workspace configuration.
type ValidationError struct {
	UnknownRepos []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("unknown repository names in frontmatter repos field: %s", strings.Join(e.UnknownRepos, ", "))
}

// ParseFrontmatter extracts and parses YAML frontmatter from markdown content.
// Frontmatter must be delimited by --- on its own line at the start of the file.
// Returns nil with no error when no frontmatter is present.
// Returns an error when frontmatter is present but the YAML is malformed.
func ParseFrontmatter(content string) (*FrontmatterBlock, error) {
	content = strings.TrimLeft(content, "\r\n")

	// Frontmatter must start with --- at the very beginning of the file.
	if !strings.HasPrefix(content, "---") {
		return nil, nil
	}

	// Find the closing --- sentinel. Skip the opening --- line.
	rest := content[3:]
	// Allow optional carriage return
	rest = strings.TrimLeft(rest, "\r\n")

	// The closing --- must appear as a line by itself.
	// We search for \n--- or ---\n to find the terminator.
	closeIdx := findClosingSentinel(rest)
	if closeIdx < 0 {
		return nil, fmt.Errorf("frontmatter: missing closing '---' sentinel")
	}

	yamlContent := rest[:closeIdx]

	// Empty frontmatter block (--- \n ---) is valid — no repos declared.
	if strings.TrimSpace(yamlContent) == "" {
		return &FrontmatterBlock{}, nil
	}

	// Parse YAML into raw map for forward compatibility.
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		return nil, fmt.Errorf("frontmatter: malformed YAML: %w", err)
	}

	block := &FrontmatterBlock{Raw: raw}

	// Extract repos field specifically.
	if reposRaw, ok := raw["repos"]; ok && reposRaw != nil {
		switch v := reposRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: repos[%d] must be a string, got %T", i, item)
				}
				block.Repos = append(block.Repos, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: repos must be a list of strings, got %T", reposRaw)
		}
	}

	// Extract tags field.
	if tagsRaw, ok := raw["tags"]; ok && tagsRaw != nil {
		switch v := tagsRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: tags[%d] must be a string, got %T", i, item)
				}
				block.Tags = append(block.Tags, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: tags must be a list of strings, got %T", tagsRaw)
		}
	}

	// Extract related field (issue/PR references like "#2090").
	if relatedRaw, ok := raw["related"]; ok && relatedRaw != nil {
		switch v := relatedRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: related[%d] must be a string, got %T", i, item)
				}
				block.Related = append(block.Related, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: related must be a list of strings, got %T", relatedRaw)
		}
	}

	// Extract status field. Unknown values are accepted for forward compatibility.
	if statusRaw, ok := raw["status"]; ok && statusRaw != nil {
		s, ok := statusRaw.(string)
		if !ok {
			return nil, fmt.Errorf("frontmatter: status must be a string, got %T", statusRaw)
		}
		block.Status = s
	}

	// Extract superseded_by field.
	if sbRaw, ok := raw["superseded_by"]; ok && sbRaw != nil {
		s, ok := sbRaw.(string)
		if !ok {
			return nil, fmt.Errorf("frontmatter: superseded_by must be a string, got %T", sbRaw)
		}
		block.SupersededBy = s
	}

	return block, nil
}

// findClosingSentinel locates the position of the closing --- sentinel in the
// YAML body (the text after the opening --- line has been consumed).
// Returns the index of the start of the --- line, or -1 if not found.
func findClosingSentinel(body string) int {
	lines := strings.Split(body, "\n")
	pos := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			return pos
		}
		pos += len(line) + 1 // +1 for the \n
	}
	return -1
}

// ValidateRepos checks that every repo name in repoNames is declared in the
// workspace configuration. Returns a *ValidationError listing unknown names,
// or nil when all names are valid (or when repoNames is empty).
func ValidateRepos(repoNames []string, workspaceConfig *WorkspaceConfig) error {
	if len(repoNames) == 0 {
		return nil
	}
	if workspaceConfig == nil {
		return fmt.Errorf("workspace config is required for repo validation")
	}

	known := make(map[string]struct{}, len(workspaceConfig.Repositories))
	for _, r := range workspaceConfig.Repositories {
		known[r.Name] = struct{}{}
	}

	var unknown []string
	for _, name := range repoNames {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}

	if len(unknown) > 0 {
		return &ValidationError{UnknownRepos: unknown}
	}
	return nil
}
