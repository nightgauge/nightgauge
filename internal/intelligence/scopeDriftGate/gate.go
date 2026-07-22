// Package scopeDriftGate implements the scope-drift preflight gate.
// For type:docs and type:chore issues, it verifies that modified files
// fall within a configured allowlist for that issue type. Out-of-scope
// modifications indicate scope drift — typically caused by stale worktrees
// reverting recently-merged work alongside legitimate scoped changes.
//
// See Issue #3040.
package scopeDriftGate

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EnforcementMode controls how the gate reacts when drift is detected.
const (
	EnforcementWarn   = "warn"
	EnforcementStrict = "strict"
)

// IssueType values understood by the gate.
const (
	IssueTypeDocs  = "docs"
	IssueTypeChore = "chore"
)

// GateConfig holds configuration for the scope-drift gate.
type GateConfig struct {
	// Enabled is the master toggle. When false, Evaluate always returns Allowed=true.
	Enabled bool
	// EnforcementMode is "warn" (log only) or "strict" (block PR). Default: "warn".
	EnforcementMode string
	// AllowlistDocs is the glob pattern list applied to type:docs issues.
	AllowlistDocs []string
	// AllowlistChore is the glob pattern list applied to type:chore issues.
	// When empty, AllowlistDocs is used as a fallback.
	AllowlistChore []string
	// BypassLabel is the label name that, when present on the issue, bypasses
	// the gate entirely (Allowed=true, Bypassed=true). Default: "scope:cross-cutting".
	BypassLabel string
}

// DefaultGateConfig returns a GateConfig with safe defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		Enabled:         true,
		EnforcementMode: EnforcementWarn,
		AllowlistDocs: []string{
			"docs/**",
			"*.md",
			".github/**",
			"README*",
		},
		AllowlistChore: []string{
			"docs/**",
			"*.md",
			".github/**",
			"README*",
		},
		BypassLabel: "scope:cross-cutting",
	}
}

// GateResult is the outcome of a scope-drift evaluation.
type GateResult struct {
	// Allowed is true when the changes pass the gate (or are bypassed/warning).
	Allowed bool
	// Bypassed is true when the bypass label triggered the pass.
	Bypassed bool
	// Reason describes why the result is what it is (always populated for human output).
	Reason string
	// DriftedFiles lists files that fell outside the allowlist.
	DriftedFiles []string
	// AllowedFiles lists files that matched the allowlist.
	AllowedFiles []string
	// IssueType is the resolved issue type ("docs" | "chore" | "" for unknown).
	IssueType string
	// EnforcementMode is the mode that produced this result.
	EnforcementMode string
	// SuggestedAction describes what the user should do when drift is detected.
	SuggestedAction string
	// HeuristicsApplied lists which heuristics fired during evaluation.
	HeuristicsApplied []string
}

// GateEvaluator evaluates changed files against the scope-drift allowlist.
type GateEvaluator struct {
	cfg GateConfig
}

// NewGateEvaluator creates a new GateEvaluator with the provided config.
func NewGateEvaluator(cfg GateConfig) *GateEvaluator {
	return &GateEvaluator{cfg: cfg}
}

// Evaluate checks changedFiles against the configured allowlist for issueType.
//
//   - issueType: "docs" or "chore" (other values return Allowed=true; gate is no-op).
//   - labels: full label slice from the GitHub issue (used to detect the bypass label).
//   - changedFiles: union of created+modified file paths. Deleted files MUST NOT
//     be passed in — deletions are always allowed.
func (g *GateEvaluator) Evaluate(issueType string, labels []string, changedFiles []string) *GateResult {
	mode := g.cfg.EnforcementMode
	if mode != EnforcementStrict {
		mode = EnforcementWarn
	}
	result := &GateResult{
		Allowed:           true,
		IssueType:         issueType,
		EnforcementMode:   mode,
		HeuristicsApplied: []string{},
		AllowedFiles:      []string{},
		DriftedFiles:      []string{},
	}

	if !g.cfg.Enabled {
		result.Reason = "gate disabled in config"
		return result
	}

	if issueType != IssueTypeDocs && issueType != IssueTypeChore {
		result.Reason = fmt.Sprintf("issue type %q is out of scope for this gate", issueType)
		return result
	}

	if g.cfg.BypassLabel != "" && hasLabel(labels, g.cfg.BypassLabel) {
		result.Bypassed = true
		result.Reason = fmt.Sprintf("bypass label %q present on issue", g.cfg.BypassLabel)
		result.HeuristicsApplied = append(result.HeuristicsApplied, "bypass-label")
		return result
	}

	if len(changedFiles) == 0 {
		result.Reason = "no changed files to evaluate"
		return result
	}

	patterns := g.allowlistFor(issueType)
	for _, path := range changedFiles {
		if matchesAllowlist(path, patterns) {
			result.AllowedFiles = append(result.AllowedFiles, path)
		} else {
			result.DriftedFiles = append(result.DriftedFiles, path)
		}
	}

	if len(result.DriftedFiles) == 0 {
		result.Reason = fmt.Sprintf("all %d changed file(s) match the type:%s allowlist", len(result.AllowedFiles), issueType)
		return result
	}

	result.HeuristicsApplied = append(result.HeuristicsApplied, "allowlist-mismatch")
	result.SuggestedAction = fmt.Sprintf(
		"Review the drifted files. If they are intentional, add %q label to the issue or extend pipeline.scope_drift_gate.allowlist_%s in .nightgauge/config.yaml.",
		g.cfg.BypassLabel, issueType,
	)

	if mode == EnforcementStrict {
		result.Allowed = false
		result.Reason = fmt.Sprintf(
			"%d file(s) outside the type:%s allowlist (strict mode blocks PR)",
			len(result.DriftedFiles), issueType,
		)
		return result
	}

	// Warn mode: drift detected but PR is not blocked.
	result.Reason = fmt.Sprintf(
		"%d file(s) outside the type:%s allowlist (warn mode — PR allowed)",
		len(result.DriftedFiles), issueType,
	)
	return result
}

// allowlistFor returns the allowlist patterns for the given issue type.
// AllowlistChore falls back to AllowlistDocs when not set.
func (g *GateEvaluator) allowlistFor(issueType string) []string {
	switch issueType {
	case IssueTypeDocs:
		return g.cfg.AllowlistDocs
	case IssueTypeChore:
		if len(g.cfg.AllowlistChore) > 0 {
			return g.cfg.AllowlistChore
		}
		return g.cfg.AllowlistDocs
	}
	return nil
}

// hasLabel reports whether the target label is present in labels.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// matchesAllowlist reports whether path matches any of the supplied patterns.
// Supported syntax (.gitignore-style, anchored to path root):
//   - exact match (e.g. "Makefile" matches only "Makefile", not "src/Makefile")
//   - single-segment globs (e.g. "*.md" matches "README.md" but not "docs/README.md")
//   - "prefix/**" matches "prefix" exactly or anything under "prefix/"
//   - "**/suffix" matches any path whose final segment(s) match suffix
//   - "a/**/b" combines the two: prefix anchor + suffix anchor on segment boundary
//
// Patterns are always anchored to the path root. There is no implicit basename
// fallback — to allow a file at any depth, use "**/<name>" or include a parent
// "<dir>/**" pattern.
func matchesAllowlist(path string, patterns []string) bool {
	for _, p := range patterns {
		if matchesPattern(path, p) {
			return true
		}
	}
	return false
}

// MatchPath reports whether path matches any of the gitignore-style patterns
// ("dir/**", "**/suffix", "a/**/b", segment-anchored globs). Exported so sibling
// intelligence packages (e.g. changeClassifier, #4124) reuse this single matcher
// implementation instead of duplicating the glob logic.
func MatchPath(path string, patterns []string) bool {
	return matchesAllowlist(path, patterns)
}

// matchesPattern is the single-pattern matcher. Exposed at package level for
// table-driven testing.
func matchesPattern(path, pattern string) bool {
	if pattern == "" {
		return false
	}
	if pattern == path {
		return true
	}

	// No "**" — single-segment match. filepath.Match enforces that "*" does
	// not cross "/" boundaries, so this is naturally root-anchored and
	// rejects multi-segment paths.
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}

	// "**" handling. Split the pattern on the "**" tokens and reduce the
	// path against each literal/glob piece in order.
	parts := strings.Split(pattern, "**")

	// Trim leading "/" off middle/trailing pieces and trailing "/" off
	// leading/middle pieces so "docs/**" splits to ["docs/", ""] which we
	// normalize to ["docs", ""]. The empty entries flag a "match anything"
	// section.
	for i := range parts {
		parts[i] = strings.Trim(parts[i], "/")
	}

	rest := path
	for i, part := range parts {
		first := i == 0
		last := i == len(parts)-1

		if part == "" {
			// "**" segment with no surrounding literal — matches anything,
			// including zero segments. Keep `rest` unchanged.
			continue
		}

		if first {
			// Leading literal must be a path-segment-aligned prefix.
			if !hasSegmentPrefix(rest, part) {
				return false
			}
			rest = strings.TrimPrefix(rest, part)
			rest = strings.TrimPrefix(rest, "/")
			continue
		}

		if last {
			// Trailing literal/glob must match the entire remainder of rest,
			// either as the full rest or anchored at a segment boundary.
			if matchesSuffix(rest, part) {
				return true
			}
			return false
		}

		// Middle literal: must appear at a segment boundary somewhere in rest.
		idx := indexOfSegment(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
		rest = strings.TrimPrefix(rest, "/")
	}

	// All parts consumed. If pattern ended with "**" (last part empty) we
	// already accepted any remainder. Otherwise, rest should have been
	// fully consumed by the trailing-literal branch above.
	return true
}

// matchesSuffix reports whether rest matches the trailing pattern piece,
// either as the entire string (for non-glob literals) or via filepath.Match
// applied to one segment-anchored substring.
func matchesSuffix(rest, suffix string) bool {
	// Direct match on the entire remainder.
	if ok, _ := filepath.Match(suffix, rest); ok {
		return true
	}
	if rest == suffix {
		return true
	}
	// Try matching against any segment-anchored substring of rest.
	for i := 0; i < len(rest); i++ {
		if i > 0 && rest[i-1] != '/' {
			continue
		}
		if ok, _ := filepath.Match(suffix, rest[i:]); ok {
			return true
		}
	}
	return false
}

// hasSegmentPrefix reports whether path starts with prefix on a path-segment
// boundary (i.e. prefix is followed by end-of-string or "/").
func hasSegmentPrefix(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}

// indexOfSegment returns the index where literal occurs aligned to a path
// segment boundary, or -1.
func indexOfSegment(path, literal string) int {
	search := path
	offset := 0
	for {
		i := strings.Index(search, literal)
		if i < 0 {
			return -1
		}
		if i == 0 || search[i-1] == '/' {
			return offset + i
		}
		offset += i + 1
		search = search[i+1:]
	}
}
