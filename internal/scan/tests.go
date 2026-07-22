// Test/source-ratio scan verb. Replicates the Glob-based test/source counting
// pass from skills/nightgauge-health-check/SKILL.md Phase 2.2 (and the
// equivalent in refactor-rewrite Phase 2.1) — audit row B5. Pure path
// classification: no file-content reads, no regex.
package scan

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TestsScanResult is the stable JSON output schema for
// `nightgauge scan tests`. Schema version 1 — do not rename or remove
// fields after first merge.
type TestsScanResult struct {
	V                 int      `json:"v"`                    // schema version, always 1
	Workdir           string   `json:"workdir"`              // absolute path that was scanned
	SourceFiles       int      `json:"source_files"`         // count of source files (excluding tests)
	TestFiles         int      `json:"test_files"`           // count of test files
	TestToSourceRatio float64  `json:"test_to_source_ratio"` // test_files / source_files; 0 when source_files=0
	Warnings          []string `json:"warnings"`             // non-fatal scan warnings
}

// TestsOptions controls a single tests-scan run.
type TestsOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// testsSourceExts mirrors debtSourceExts. Lowercase including the leading dot.
var testsSourceExts = stringSet(
	".ts", ".tsx", ".js", ".jsx",
	".py", ".go", ".rs", ".java", ".kt",
)

// testsExcludedDirs are pruned at walk time — same set as the other scan
// verbs for consistency.
var testsExcludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
}

// RunTestsScan walks the workdir counting source files vs test files. Test
// classification is by basename, mirroring the SKILL.md Glob patterns:
// *.test.*, *.spec.*, *_test.*, test_*. A file is counted as "source" if it
// matches the source-extension allowlist AND is NOT classified as a test —
// matching the SKILL.md "filter out files matching test patterns" step.
func RunTestsScan(ctx context.Context, opts TestsOptions) (*TestsScanResult, error) {
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

	result := &TestsScanResult{
		V:        1,
		Workdir:  workdir,
		Warnings: []string{},
	}

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
			if _, skip := testsExcludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		base := d.Name()
		ext := strings.ToLower(filepath.Ext(base))
		if !contains(testsSourceExts, ext) {
			return nil
		}
		if isTestFile(base) {
			result.TestFiles++
		} else {
			result.SourceFiles++
		}
		return nil
	})

	if walkErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("walk aborted: %v", walkErr))
	}

	if result.SourceFiles > 0 {
		result.TestToSourceRatio = float64(result.TestFiles) / float64(result.SourceFiles)
	}

	return result, nil
}

// isTestFile classifies a basename against the four SKILL.md test-name
// patterns: *.test.*, *.spec.*, *_test.*, test_*. The implementation is a
// set of basename predicates rather than a regex, matching the Glob behavior
// the SKILL.md sources documented.
func isTestFile(base string) bool {
	if strings.HasPrefix(base, "test_") {
		return true
	}
	// *.test.* and *.spec.* — must contain ".test." or ".spec." in the name.
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	// *_test.* — strip extension, check the stem ends in "_test".
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, "_test") {
		return true
	}
	return false
}
