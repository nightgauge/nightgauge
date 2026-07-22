package config

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrAlreadyMigrated is returned by MigrateFile when the file is already at schema_version 2.
var ErrAlreadyMigrated = errors.New("config is already at schema_version 2")

// MigrateResult describes the outcome of an on-disk migration attempt.
type MigrateResult struct {
	Path       string
	OldVersion string // empty string for v1 (absent schema_version)
	NewVersion string // "2"
	Changed    bool   // false when the file was already v2 (idempotent)
	Diff       string // unified diff when Changed=true; empty otherwise
}

// MigrateFile reads path, applies v1→v2 migration using the yaml.v3 Node API
// to preserve comments, blank lines, and key ordering, then writes the result.
// When dryRun=true, writes nothing but still populates MigrateResult.Diff.
// Returns ErrAlreadyMigrated when schema_version is already "2".
func MigrateFile(path string, dryRun bool) (*MigrateResult, error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(orig, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("unexpected YAML structure in %s: expected document node", path)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected YAML mapping at root of %s", path)
	}

	// Find schema_version in root mapping.
	// Content is pairs: [key0, val0, key1, val1, ...]
	schemaVersionIdx := -1
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "schema_version" {
			schemaVersionIdx = i
			break
		}
	}

	result := &MigrateResult{
		Path:       path,
		NewVersion: "2",
	}

	if schemaVersionIdx >= 0 {
		result.OldVersion = root.Content[schemaVersionIdx+1].Value
		if result.OldVersion == "2" {
			return result, ErrAlreadyMigrated
		}
	}

	// Insert schema_version: "2" at position 0 of the root mapping.
	if schemaVersionIdx < 0 {
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: "schema_version",
			Tag:   "!!str",
		}
		valNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: "2",
			Tag:   "!!str",
			Style: yaml.DoubleQuotedStyle,
		}
		root.Content = append([]*yaml.Node{keyNode, valNode}, root.Content...)
	} else {
		root.Content[schemaVersionIdx+1].Value = "2"
		root.Content[schemaVersionIdx+1].Style = yaml.DoubleQuotedStyle
	}

	// Find forges in root mapping (indices may have shifted after prepending schema_version).
	forgesIdx := -1
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "forges" {
			forgesIdx = i
			break
		}
	}

	if forgesIdx < 0 {
		// Append a new forges: mapping with a default github entry.
		forgesKeyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: "forges",
			Tag:   "!!str",
		}
		forgesValNode := buildDefaultGitHubForgeMapping()
		root.Content = append(root.Content, forgesKeyNode, forgesValNode)
	} else {
		// forges exists; insert github entry at the front if not already present.
		forgesVal := root.Content[forgesIdx+1]
		hasGitHub := false
		for i := 0; i < len(forgesVal.Content)-1; i += 2 {
			if forgesVal.Content[i].Value == "github" {
				hasGitHub = true
				break
			}
		}
		if !hasGitHub {
			githubKeyNode := &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: "github",
				Tag:   "!!str",
			}
			githubValNode := buildGitHubForgeEntryNode()
			forgesVal.Content = append([]*yaml.Node{githubKeyNode, githubValNode}, forgesVal.Content...)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close encoder for %s: %w", path, err)
	}
	newBytes := buf.Bytes()

	origNorm := normalizeTrailingNewline(orig)
	newNorm := normalizeTrailingNewline(newBytes)

	result.Changed = !bytes.Equal(origNorm, newNorm)
	if result.Changed {
		redactedOrig, redactOrigErr := RedactYAMLBytes(origNorm)
		if redactOrigErr != nil {
			return nil, fmt.Errorf("redact original migration preview: %w", redactOrigErr)
		}
		redactedNew, redactNewErr := RedactYAMLBytes(newNorm)
		if redactNewErr != nil {
			return nil, fmt.Errorf("redact migrated preview: %w", redactNewErr)
		}
		result.Diff = diffLines(string(redactedOrig), string(redactedNew))
		log.Printf("migrated config %q from v1 to v2 (schema_version added, forges.github inserted)", path)
	}

	// Post-migration validation with YAML line/column annotation.
	var doc2 yaml.Node
	if err2 := yaml.Unmarshal(newNorm, &doc2); err2 == nil {
		cfg, parseErr := parseYAML(newNorm)
		if parseErr == nil {
			var repos map[string]*RepositoryConfig
			if cfg.Autonomous != nil {
				repos = cfg.Autonomous.Repositories
			}
			if valErr := ValidateForgeConfig(cfg.Forges, repos); valErr != nil {
				return nil, enrichValidationError(valErr, &doc2, path)
			}
		}
	}

	if !dryRun && result.Changed {
		if err := os.WriteFile(path, newNorm, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}

	return result, nil
}

// buildDefaultGitHubForgeMapping returns a MappingNode representing:
//
//	github:
//	  kind: github
//	  base_url: https://github.com
func buildDefaultGitHubForgeMapping() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "github", Tag: "!!str"},
			buildGitHubForgeEntryNode(),
		},
	}
}

// buildGitHubForgeEntryNode returns a MappingNode for the github forge entry.
func buildGitHubForgeEntryNode() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "kind", Tag: "!!str"},
			{Kind: yaml.ScalarNode, Value: "github", Tag: "!!str"},
			{Kind: yaml.ScalarNode, Value: "base_url", Tag: "!!str"},
			{Kind: yaml.ScalarNode, Value: "https://github.com", Tag: "!!str"},
		},
	}
}

// normalizeTrailingNewline ensures bytes end with exactly one newline.
func normalizeTrailingNewline(b []byte) []byte {
	b = bytes.TrimRight(b, "\n")
	return append(b, '\n')
}

// enrichValidationError annotates a validation error with YAML line numbers
// by looking up forge key positions in the migrated node tree.
func enrichValidationError(valErr error, doc *yaml.Node, path string) error {
	positions := forgeNodePositions(doc)
	errMsg := valErr.Error()
	for forgeID, line := range positions {
		if strings.Contains(errMsg, "forges."+forgeID) {
			return fmt.Errorf("%s:%d: post-migration validation failed: %w", path, line, valErr)
		}
	}
	return fmt.Errorf("%s: post-migration validation failed: %w", path, valErr)
}

// forgeNodePositions returns a map of forge key name → line number extracted
// from the yaml.v3 Node tree. Used for validation error annotation.
func forgeNodePositions(doc *yaml.Node) map[string]int {
	positions := make(map[string]int)
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return positions
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return positions
	}
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "forges" {
			forgesVal := root.Content[i+1]
			if forgesVal.Kind != yaml.MappingNode {
				break
			}
			for j := 0; j < len(forgesVal.Content)-1; j += 2 {
				keyNode := forgesVal.Content[j]
				positions[keyNode.Value] = keyNode.Line
			}
			break
		}
	}
	return positions
}

// diffLines produces a simple unified-style diff between two strings.
func diffLines(a, b string) string {
	aLines := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bLines := strings.Split(strings.TrimRight(b, "\n"), "\n")

	var buf strings.Builder
	buf.WriteString("--- original\n")
	buf.WriteString("+++ migrated\n")

	lcs := lcsLines(aLines, bLines)
	ai, bi := 0, 0
	for _, l := range lcs {
		for ai < len(aLines) && aLines[ai] != l {
			buf.WriteString("- ")
			buf.WriteString(aLines[ai])
			buf.WriteByte('\n')
			ai++
		}
		for bi < len(bLines) && bLines[bi] != l {
			buf.WriteString("+ ")
			buf.WriteString(bLines[bi])
			buf.WriteByte('\n')
			bi++
		}
		buf.WriteString("  ")
		buf.WriteString(l)
		buf.WriteByte('\n')
		ai++
		bi++
	}
	for ; ai < len(aLines); ai++ {
		buf.WriteString("- ")
		buf.WriteString(aLines[ai])
		buf.WriteByte('\n')
	}
	for ; bi < len(bLines); bi++ {
		buf.WriteString("+ ")
		buf.WriteString(bLines[bi])
		buf.WriteByte('\n')
	}
	return buf.String()
}

// lcsLines returns the Longest Common Subsequence of two string slices.
func lcsLines(a, b []string) []string {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	result := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			result = append(result, a[i-1])
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
