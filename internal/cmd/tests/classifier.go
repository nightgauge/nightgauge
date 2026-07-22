package tests

import (
	"path/filepath"
	"strings"
)

// The four basename predicates, source-extension allowlist, and excluded-dir
// set in this file are intentionally a local copy of the helpers in
// internal/scan/tests.go. They are kept in sync by ADR-001 (decisions.md for
// issue #3097): drift would surface immediately because the proof-consumer
// SKILL exercises both `scan tests` (the `internal/scan` copy) and
// `test inventory` (this copy) in the same workflow. If a third caller ever
// appears, promote these helpers to internal/util/testfiles.

// sourceExts is the lowercase, leading-dot allowlist of file extensions that
// count as source/test candidates. Mirrors testsSourceExts in
// internal/scan/tests.go.
var sourceExts = stringSet(
	".ts", ".tsx", ".js", ".jsx",
	".py", ".go", ".rs", ".java", ".kt",
)

// excludedDirs are pruned at walk time. Mirrors testsExcludedDirs in
// internal/scan/tests.go.
var excludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
}

// isTestFile classifies a basename against the four SKILL.md test-name
// patterns: *.test.*, *.spec.*, *_test.*, test_*. Mirrors isTestFile in
// internal/scan/tests.go.
func isTestFile(base string) bool {
	if strings.HasPrefix(base, "test_") {
		return true
	}
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, "_test") {
		return true
	}
	return false
}

// sourceStem reverses the four test-name patterns: it returns the basename
// of the source file a test would map to, or ("", false) if the input is not
// a recognized test basename. The mapping is the same one documented in
// SKILL.md Phase 1.3:
//
//	foo.test.ts  → foo.ts
//	bar.spec.js  → bar.js
//	baz_test.go  → baz.go
//	test_qux.py  → qux.py
func sourceStem(testBase string) (string, bool) {
	if strings.HasPrefix(testBase, "test_") {
		return strings.TrimPrefix(testBase, "test_"), true
	}
	ext := filepath.Ext(testBase)
	stem := strings.TrimSuffix(testBase, ext)

	if strings.HasSuffix(stem, "_test") {
		return strings.TrimSuffix(stem, "_test") + ext, true
	}
	if strings.HasSuffix(stem, ".test") {
		return strings.TrimSuffix(stem, ".test") + ext, true
	}
	if strings.HasSuffix(stem, ".spec") {
		return strings.TrimSuffix(stem, ".spec") + ext, true
	}
	return "", false
}

func stringSet(s ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[strings.ToLower(v)] = struct{}{}
	}
	return out
}

func contains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// relOrAbs returns path relative to workdir for warning + output messages,
// falling back to the absolute path if the rel computation fails.
func relOrAbs(workdir, path string) string {
	if rel, err := filepath.Rel(workdir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}
