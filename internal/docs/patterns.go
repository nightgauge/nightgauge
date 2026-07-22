// Package docs provides deterministic markdown documentation operations.
// PatternDetectResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge docs detect-patterns
// --json` output; any breaking change requires incrementing the V field.
//
// The detect-patterns verb replaces the inline bash grep loop in
// docs-write Phase 1.5 Step 1.5.1 (audit row B35). It is non-fatal by design:
// unreadable files produce warnings; only hard input errors (invalid glob
// syntax) return a non-nil error.
package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PatternDetectOptions controls a single detect-patterns run.
type PatternDetectOptions struct {
	// FilesGlob is a glob pattern passed to filepath.Glob. Required.
	FilesGlob string
	// JSON controls whether the caller wants JSON output (for CLI flag wiring).
	JSON bool
}

// Pattern records a single matched pattern slug and the files that matched it.
type Pattern struct {
	Slug  string   `json:"slug"`
	Files []string `json:"files"`
}

// PatternDetectResult is the stable JSON output schema for
// `nightgauge docs detect-patterns`. Schema version 1 — do not rename or
// remove fields after first merge.
type PatternDetectResult struct {
	V        int       `json:"v"`        // schema version, always 1
	Patterns []Pattern `json:"patterns"` // slugs with ≥1 matching file
	Warnings []string  `json:"warnings"` // non-fatal warnings (unreadable files, etc.)
}

// patternTable returns the closed set of 7 pattern slugs and their keywords.
// Keyword matching is line-based substring search using regexp.MatchString so
// keywords may include simple regexp meta-characters (e.g. `class.*Service`).
func patternTable() map[string][]string {
	return map[string][]string{
		"event-system":      {"EventEmitter", `on\(`, `\.emit\(`, "_onDid", "vscode.EventEmitter"},
		"auth-security":     {"authenticate", "authorize", "middleware", "guard", "validateToken"},
		"service-pattern":   {`class.*Service`, `class.*Manager`, `class.*Provider`},
		"repo-storage":      {`class.*Repository`, `class.*Store`, `db\.query`, `prisma\.`},
		"config-system":     {"config", "settings", "schema", "zod", "Config"},
		"pipeline-workflow": {"stage", "orchestrat", "pipeline", "PipelineOrchestrator"},
		"ipc-transport":     {"stdio", "ipc", "socket", `\bexec\b`, "spawn"},
	}
}

// slugOrder defines a stable output order for the closed-set slugs so JSON
// output is deterministic across runs.
var slugOrder = []string{
	"event-system",
	"auth-security",
	"service-pattern",
	"repo-storage",
	"config-system",
	"pipeline-workflow",
	"ipc-transport",
}

// DetectPatterns expands the glob in opts.FilesGlob, reads each matched file,
// and returns which pattern slugs have at least one keyword match. Unreadable
// files are added to Warnings and skipped — the function still exits 0.
// An invalid glob expression (one that filepath.Glob rejects) is the only
// condition that returns a non-nil error.
func DetectPatterns(opts PatternDetectOptions) (*PatternDetectResult, error) {
	if opts.FilesGlob == "" {
		return nil, fmt.Errorf("detect-patterns: --files is required")
	}

	matches, err := filepath.Glob(opts.FilesGlob)
	if err != nil {
		// filepath.Glob returns ErrBadPattern only for malformed syntax.
		return nil, fmt.Errorf("detect-patterns: invalid glob %q: %w", opts.FilesGlob, err)
	}

	table := patternTable()
	// slug → list of matched file paths
	slugMatches := make(map[string][]string, len(table))

	result := &PatternDetectResult{
		V:        1,
		Patterns: []Pattern{},
		Warnings: []string{},
	}

	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			// Skip directories silently; stat failures are non-fatal.
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: stat: %v", path, err))
			}
			continue
		}

		content, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: read: %v", path, err))
			continue
		}

		text := string(content)
		for slug, keywords := range table {
			if _, already := slugMatches[slug]; already {
				// Already matched this slug from a previous file — just check
				// if this file also matches so we can append it.
			}
			for _, kw := range keywords {
				matched, err := regexp.MatchString(kw, text)
				if err != nil {
					// Keyword regexp is part of the closed set and should
					// never be malformed; log a warning and skip rather than
					// crashing.
					result.Warnings = append(result.Warnings, fmt.Sprintf("keyword regexp %q: %v", kw, err))
					continue
				}
				if matched {
					slugMatches[slug] = append(slugMatches[slug], filepath.ToSlash(path))
					break
				}
			}
		}
	}

	// Emit results in stable slug order, only for slugs with ≥1 match.
	for _, slug := range slugOrder {
		files, ok := slugMatches[slug]
		if !ok || len(files) == 0 {
			continue
		}
		result.Patterns = append(result.Patterns, Pattern{
			Slug:  slug,
			Files: deduplicateStrings(files),
		})
	}

	return result, nil
}

// deduplicateStrings returns a copy of ss with duplicate entries removed,
// preserving first-occurrence order.
func deduplicateStrings(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// PrintDetectPatternsHuman renders detect-patterns result in human-readable form.
func PrintDetectPatternsHuman(r *PatternDetectResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "nightgauge docs detect-patterns — schema v%d\n", r.V)
	fmt.Fprintf(&sb, "patterns matched: %d\n\n", len(r.Patterns))
	if len(r.Patterns) == 0 {
		sb.WriteString("  (no patterns matched)\n")
	}
	for _, p := range r.Patterns {
		fmt.Fprintf(&sb, "  %s: %d file(s)\n", p.Slug, len(p.Files))
		for _, f := range p.Files {
			fmt.Fprintf(&sb, "    %s\n", f)
		}
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&sb, "  ! %s\n", w)
	}
	return sb.String()
}
