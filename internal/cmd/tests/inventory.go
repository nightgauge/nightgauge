package tests

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RunInventory walks opts.Workdir, classifies every file matching the source
// extension allowlist as either source or test, derives the test→source
// mapping by stripping the basename suffix per SKILL.md Phase 1.3, and
// emits the list of source files that have no matching test per Phase 1.4.
//
// Paths in the result are workdir-relative with POSIX-style separators, so
// downstream consumers (e.g. `risk-score --files`) can pipe the list back in
// directly. A test file that maps to a source basename outside the
// inventory (e.g. tests for a deleted file) is included in TestToSourceMapping
// with the inferred source path even when that source no longer exists —
// callers can detect that case by intersecting with SourceFiles.
func RunInventory(ctx context.Context, opts InventoryOptions) (*InventoryResult, error) {
	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	workdir = abs

	result := &InventoryResult{
		V:                   SchemaVersion,
		Workdir:             workdir,
		SourceFiles:         []string{},
		TestFiles:           []string{},
		TestToSourceMapping: map[string]string{},
		UntestedFiles:       []string{},
		Warnings:            []string{},
	}

	// First pass: collect source and test file lists.
	sourceBases := map[string][]string{} // basename → list of rel paths (collision-aware)

	walkErr := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk %s: %v", rel, err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == workdir {
				return nil
			}
			if _, skip := excludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		base := d.Name()
		ext := strings.ToLower(filepath.Ext(base))
		if !contains(sourceExts, ext) {
			return nil
		}
		rel := relOrAbs(workdir, path)
		if isTestFile(base) {
			result.TestFiles = append(result.TestFiles, rel)
		} else {
			result.SourceFiles = append(result.SourceFiles, rel)
			sourceBases[base] = append(sourceBases[base], rel)
		}
		return nil
	})

	if walkErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("walk aborted: %v", walkErr))
	}

	sort.Strings(result.SourceFiles)
	sort.Strings(result.TestFiles)

	// Second pass: derive test→source mapping. For each test, compute the
	// basename a source counterpart would have, then look it up in
	// sourceBases. When a basename collides (e.g. `user.go` exists in two
	// directories), prefer the nearest source by directory match — walk
	// up the test's directory and look for a source under the same path.
	// This matches the SKILL.md prose ("strip the test/spec suffix") while
	// being explicit about the collision behavior.
	testedSources := map[string]struct{}{}
	for _, t := range result.TestFiles {
		baseT := filepath.Base(t)
		stemBase, ok := sourceStem(baseT)
		if !ok {
			continue
		}
		candidates, exists := sourceBases[stemBase]
		var mapped string
		switch {
		case !exists:
			// Test for a missing/deleted source — record the inferred
			// rel path under the test's directory so the mapping is
			// still informative. This matches the SKILL.md prose
			// "likely_source_file".
			testDir := filepath.ToSlash(filepath.Dir(t))
			if testDir == "." || testDir == "" {
				mapped = stemBase
			} else {
				mapped = testDir + "/" + stemBase
			}
		case len(candidates) == 1:
			mapped = candidates[0]
		default:
			// Collision: prefer the source under the same directory as
			// the test; otherwise fall back to the lexicographically
			// first candidate (stable across runs).
			testDir := filepath.ToSlash(filepath.Dir(t))
			mapped = candidates[0]
			for _, c := range candidates {
				if filepath.ToSlash(filepath.Dir(c)) == testDir {
					mapped = c
					break
				}
			}
		}
		result.TestToSourceMapping[t] = mapped
		testedSources[mapped] = struct{}{}
	}

	// Third pass: untested = source files NOT in testedSources AND with no
	// test-pattern basename match anywhere in the inventory. The basename
	// match is the stricter SKILL.md Phase 1.4 rule (a test in a different
	// directory still counts as covering the source). The mapping pass
	// already covered the same-name case; this pass catches tests that
	// happen to share a basename across directories.
	knownTestBases := map[string]struct{}{}
	for _, t := range result.TestFiles {
		baseT := filepath.Base(t)
		if stem, ok := sourceStem(baseT); ok {
			knownTestBases[stem] = struct{}{}
		}
	}

	for _, src := range result.SourceFiles {
		if _, covered := testedSources[src]; covered {
			continue
		}
		if _, basenameCovered := knownTestBases[filepath.Base(src)]; basenameCovered {
			continue
		}
		result.UntestedFiles = append(result.UntestedFiles, src)
	}
	sort.Strings(result.UntestedFiles)

	result.Counts = InventoryCounts{
		SourceFiles:   len(result.SourceFiles),
		TestFiles:     len(result.TestFiles),
		UntestedFiles: len(result.UntestedFiles),
	}

	return result, nil
}
