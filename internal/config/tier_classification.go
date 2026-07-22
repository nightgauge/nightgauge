package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	yaml "gopkg.in/yaml.v3"
)

//go:embed testdata/tier_classification.yaml
var classificationYAML []byte

// TierAuditEntry is one row in the per-key tier audit report.
type TierAuditEntry struct {
	Key             string `json:"key"`
	EffectiveTier   string `json:"effectiveTier"`   // machine | project | local | default
	EffectiveSource string `json:"effectiveSource"` // absolute file path or "default"
	TargetTier      string `json:"targetTier"`      // team | machine | runtime | unknown
	Status          string `json:"status"`          // OK | DRIFT | UNCLASSIFIED
}

// tierClassification holds the canonical key→target-tier map loaded from the embedded YAML.
type tierClassification struct {
	Keys map[string]string `yaml:"keys"`
}

func loadTierClassification() (map[string]string, error) {
	var tc tierClassification
	if err := yaml.Unmarshal(classificationYAML, &tc); err != nil {
		return nil, fmt.Errorf("parse tier classification: %w", err)
	}
	return tc.Keys, nil
}

// BuildAuditReport walks all three tier YAML documents (machine, project, local)
// and returns one TierAuditEntry per leaf key, annotated with its effective
// source tier and drift status vs. the canonical classification.
//
// This deliberately does NOT call LoadMerged — the merged result loses per-tier
// source attribution. We re-read each tier file independently to preserve it.
// See ADR-002 in .nightgauge/knowledge/features/3644-config-show-tier-audit/decisions.md.
func BuildAuditReport(workspaceRoot string) ([]TierAuditEntry, error) {
	machineConfigPath, _ := machineConfigPathFn()
	projectConfigPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	localConfigPath := filepath.Join(workspaceRoot, ".nightgauge", "config.local.yaml")

	type tierSource struct {
		tier   string
		source string
	}

	// effective maps dotted-key → the last tier that wrote it (machine < project < local)
	effective := map[string]tierSource{}

	tiers := []struct {
		name string
		path string
	}{
		{"machine", machineConfigPath},
		{"project", projectConfigPath},
		{"local", localConfigPath},
	}

	for _, t := range tiers {
		data, err := os.ReadFile(t.path)
		if err != nil {
			continue // missing tier — skip
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			continue
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		absPath, _ := filepath.Abs(t.path)
		walkLeaves(root, "", func(path string) {
			effective[path] = tierSource{tier: t.name, source: absPath}
		})
	}

	classification, err := loadTierClassification()
	if err != nil {
		return nil, err
	}

	var entries []TierAuditEntry
	for key, src := range effective {
		targetTier := lookupTargetTier(classification, key)

		// Runtime-tier keys are excluded from the audit until #3313 Phase 3
		// wires runtime enforcement to the Go side.
		if targetTier == "runtime" {
			continue
		}

		entries = append(entries, TierAuditEntry{
			Key:             key,
			EffectiveTier:   src.tier,
			EffectiveSource: src.source,
			TargetTier:      targetTier,
			Status:          computeStatus(src.tier, targetTier),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	return entries, nil
}

// walkLeaves recursively visits every leaf (scalar or sequence) in the YAML
// mapping tree and calls fn with the dotted key path.
func walkLeaves(node *yaml.Node, prefix string, fn func(path string)) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			val := node.Content[i+1]
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			walkLeaves(val, path, fn)
		}
	case yaml.ScalarNode, yaml.SequenceNode:
		fn(prefix)
	}
}

// lookupTargetTier finds the documented target tier for the given key path.
// Handles the autonomous.repositories.* wildcard (any sub-key of repositories maps to "machine").
// Falls back to a top-level prefix match so nested keys under a classified parent inherit that tier.
func lookupTargetTier(classification map[string]string, key string) string {
	// Exact match first
	if tier, ok := classification[key]; ok {
		return tier
	}
	// Wildcard prefix match (e.g., autonomous.repositories.* matches any per-repo entry)
	for classKey, tier := range classification {
		if strings.HasSuffix(classKey, ".*") {
			prefix := strings.TrimSuffix(classKey, ".*")
			if strings.HasPrefix(key, prefix+".") {
				return tier
			}
		}
	}
	// Top-level prefix match: if "pipeline" → team, then "pipeline.anything" inherits team
	// unless a more specific rule (exact or wildcard) already matched above.
	parts := strings.SplitN(key, ".", 2)
	if len(parts) > 1 {
		if tier, ok := classification[parts[0]]; ok {
			return tier
		}
	}
	return "unknown"
}

// computeStatus determines OK / DRIFT / UNCLASSIFIED for the (effectiveTier, targetTier) pair.
//
// The file-to-tier mapping is:
//   - "project" effective tier → .nightgauge/config.yaml  (the team-committed file)
//   - "machine" effective tier → ~/.nightgauge/config.yaml
//   - "local"   effective tier → .nightgauge/config.local.yaml
//
// So a "team" target-tier key belongs in "project"; a "machine" target-tier key
// belongs in "machine". Any other placement is DRIFT.
func computeStatus(effectiveTier, targetTier string) string {
	if targetTier == "unknown" {
		return "UNCLASSIFIED"
	}
	// Resolve each target tier to its expected file tier.
	expectedEffective := map[string]string{
		"team":    "project", // team keys live in .nightgauge/config.yaml (the project file)
		"machine": "machine", // machine keys live in ~/.nightgauge/config.yaml
	}
	expected, ok := expectedEffective[targetTier]
	if !ok {
		// Unmapped target tiers (e.g. "local") — if effective matches, OK; otherwise DRIFT.
		if effectiveTier == targetTier {
			return "OK"
		}
		return "DRIFT"
	}
	if effectiveTier == expected {
		return "OK"
	}
	// "local" overriding a team key is allowed — local is the gitignored per-checkout override.
	if targetTier == "team" && effectiveTier == "local" {
		return "OK"
	}
	if targetTier == "machine" && (effectiveTier == "project" || effectiveTier == "local") {
		return "DRIFT — machine key in " + effectiveTier + " config"
	}
	if targetTier == "team" && effectiveTier == "machine" {
		return "DRIFT — team key in machine config"
	}
	return "DRIFT"
}

// RenderTierAudit builds the audit report and returns formatted output.
//
// filterDrift: when true, only DRIFT rows are included.
// asJSON: when true, emit a JSON array instead of a tabwriter table.
//
// Returns (hasDrift bool, output string, error).
func RenderTierAudit(workspaceRoot string, filterDrift bool, asJSON bool) (bool, string, error) {
	entries, err := BuildAuditReport(workspaceRoot)
	if err != nil {
		return false, "", err
	}

	hasDrift := false
	var filtered []TierAuditEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Status, "DRIFT") {
			hasDrift = true
		}
		if !filterDrift || strings.HasPrefix(e.Status, "DRIFT") {
			filtered = append(filtered, e)
		}
	}

	if asJSON {
		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return hasDrift, "", fmt.Errorf("encode tier audit json: %w", err)
		}
		return hasDrift, string(out) + "\n", nil
	}

	// Text table via tabwriter
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tEFFECTIVE TIER\tTARGET TIER\tSTATUS")
	fmt.Fprintln(tw, strings.Repeat("-", 80))
	for _, e := range filtered {
		effectiveCol := e.EffectiveTier
		if e.EffectiveSource != "" && e.EffectiveSource != "default" {
			effectiveCol = fmt.Sprintf("%s (%s)", e.EffectiveTier, e.EffectiveSource)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Key, effectiveCol, e.TargetTier, e.Status)
	}
	tw.Flush()
	return hasDrift, buf.String(), nil
}
