// Package changeClassifier deterministically classifies a set of changed files
// into a coarse change scope (docs-only / config-only / source / mixed) so the
// pipeline and CI can fast-track trivial changes.
//
// It is the shared, deterministic primitive consumed by the scheduler (skip
// stages for trivial changes, #4126), CI (skip heavy jobs, #4127), and gate
// relaxation (#4128). The glob matcher is reused from scopeDriftGate rather than
// re-implemented — the two must agree on what "docs" / "config" means.
//
// @see Issue #4124
package changeClassifier

import (
	"github.com/nightgauge/nightgauge/internal/intelligence/scopeDriftGate"
)

// Classification is the coarse scope of a change set.
type Classification string

const (
	// Empty means no files changed (nothing to classify).
	Empty Classification = "empty"
	// DocsOnly means every changed file is documentation.
	DocsOnly Classification = "docs_only"
	// ConfigOnly means every changed file is configuration.
	ConfigOnly Classification = "config_only"
	// Source means every changed file is source/other (the conservative class).
	Source Classification = "source"
	// Mixed means the change spans more than one of the above file kinds.
	Mixed Classification = "mixed"
)

// Trivial reports whether the classification is one the pipeline/CI may
// fast-track by default (docs-only or config-only). Source and Mixed are never
// trivial — they take the full path. Callers may still consult per-repo rules
// to widen or narrow this, but this is the safe default.
func (c Classification) Trivial() bool {
	return c == DocsOnly || c == ConfigOnly
}

// ClassPatterns holds the glob allowlists that define each non-source file
// kind. A file is "docs" if it matches Docs, else "config" if it matches
// Config, else "source". Patterns use the gitignore-style syntax understood by
// scopeDriftGate.MatchPath ("dir/**", "**/suffix", segment-anchored globs).
type ClassPatterns struct {
	Docs   []string
	Config []string
}

// DefaultClassPatterns returns the built-in classification globs. These mirror
// the docs/config defaults the fast-track config (routing.change_rules, #4125)
// ships, so a repo with no custom rules still classifies sensibly.
func DefaultClassPatterns() ClassPatterns {
	return ClassPatterns{
		Docs: []string{
			"docs/**",
			"**/*.md",
			"**/*.mdx",
			"README*",
			"CHANGELOG*",
			"LICENSE*",
		},
		Config: []string{
			".nightgauge/**",
			".github/**",
			"**/*.yaml",
			"**/*.yml",
			"*.json",
			"tsconfig*.json",
			".editorconfig",
			".gitignore",
			".npmrc",
		},
	}
}

// fileKind is the per-file bucket used to aggregate a Classification.
type fileKind int

const (
	kindDocs fileKind = iota
	kindConfig
	kindSource
)

// classifyFile buckets a single path. Docs takes precedence over config (a
// markdown file under .github/ is still docs), and anything unmatched is the
// conservative "source" kind.
func classifyFile(path string, p ClassPatterns) fileKind {
	if scopeDriftGate.MatchPath(path, p.Docs) {
		return kindDocs
	}
	if scopeDriftGate.MatchPath(path, p.Config) {
		return kindConfig
	}
	return kindSource
}

// Classify returns the coarse Classification for a set of changed file paths
// using the supplied patterns. It is pure and deterministic: same input → same
// output, no I/O, no LLM. Pass DefaultClassPatterns() unless a caller has
// repo-specific globs.
//
// Aggregation: an empty set is Empty; a set whose files are all one kind is the
// matching homogeneous class (DocsOnly / ConfigOnly / Source); any set spanning
// more than one kind is Mixed. Mixed and Source are never trivial, so a change
// that mixes docs with source is correctly NOT fast-tracked.
func Classify(changedFiles []string, p ClassPatterns) Classification {
	var docs, config, source bool
	n := 0
	for _, f := range changedFiles {
		if f == "" {
			continue
		}
		n++
		switch classifyFile(f, p) {
		case kindDocs:
			docs = true
		case kindConfig:
			config = true
		case kindSource:
			source = true
		}
	}
	if n == 0 {
		return Empty
	}

	kinds := 0
	if docs {
		kinds++
	}
	if config {
		kinds++
	}
	if source {
		kinds++
	}
	if kinds > 1 {
		return Mixed
	}
	switch {
	case docs:
		return DocsOnly
	case config:
		return ConfigOnly
	default:
		return Source
	}
}

// ClassifyDefault is a convenience wrapper using DefaultClassPatterns().
func ClassifyDefault(changedFiles []string) Classification {
	return Classify(changedFiles, DefaultClassPatterns())
}
