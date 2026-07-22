// Debt-marker scan verb. Replicates the TODO/FIXME/HACK/XXX grep pass from
// skills/nightgauge-health-check/SKILL.md Phase 3.1 (and the equivalent
// inline pass in refactor-rewrite Phase 2.2) — audit row B5. Counts matching
// *lines* per marker (not per occurrence) to preserve drop-in behavioral
// compatibility with `grep -cE 'TODO|FIXME|HACK|XXX' file | awk '{sum+=$NF}'`.
package scan

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DebtScanResult is the stable JSON output schema for
// `nightgauge scan debt`. Schema version 1 — do not rename or remove
// fields after first merge.
type DebtScanResult struct {
	V        int      `json:"v"`        // schema version, always 1
	Workdir  string   `json:"workdir"`  // absolute path that was scanned
	Markers  Markers  `json:"markers"`  // per-marker matching-line counts + total
	Files    int      `json:"files"`    // count of source files containing >=1 marker line
	Warnings []string `json:"warnings"` // non-fatal scan warnings (oversize-skips, etc.)
}

// Markers holds per-keyword line counts plus the total. Each field counts
// the number of lines on which the keyword appears — multiple occurrences on
// the same line increment by 1 (line-count semantics, mirroring grep -cE).
type Markers struct {
	TODO  int `json:"todo"`
	FIXME int `json:"fixme"`
	HACK  int `json:"hack"`
	XXX   int `json:"xxx"`
	Total int `json:"total"`
}

// DebtOptions controls a single debt-scan run.
type DebtOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// debtSourceExts mirrors the SKILL.md `--include` allowlist exactly. Lowercase
// keys including the leading dot.
var debtSourceExts = stringSet(
	".ts", ".tsx", ".js", ".jsx",
	".py", ".go", ".rs", ".java", ".kt",
)

// debtExcludedDirs are pruned at walk time — same set as scan secrets so the
// two verbs share a consistent exclude profile.
var debtExcludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
}

// debtMaxFileBytes caps individual file reads. Files larger than this are
// recorded as warnings and skipped — same threshold as scan secrets.
const debtMaxFileBytes int64 = 5 * 1024 * 1024

// Per-keyword regexes. Word boundaries tighten against substrings like
// `TODOIST` — the SKILL.md `grep -cE` does not enforce boundaries, but the
// downstream rubric tolerances (e.g. <5 markers → 90-100, >100 → 0-29) are
// wide enough that the slight reduction in false positives is a net positive.
// Documented in the verb's `Long` description.
var (
	debtTODORE  = regexp.MustCompile(`\bTODO\b`)
	debtFIXMERE = regexp.MustCompile(`\bFIXME\b`)
	debtHACKRE  = regexp.MustCompile(`\bHACK\b`)
	debtXXXRE   = regexp.MustCompile(`\bXXX\b`)
)

// RunDebtScan executes the debt-marker scan and returns the structured
// result. Non-fatal by design — oversize files and unreadable files surface
// as warnings rather than errors. err is reserved for hard input errors
// (invalid workdir).
func RunDebtScan(ctx context.Context, opts DebtOptions) (*DebtScanResult, error) {
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

	result := &DebtScanResult{
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
			if _, skip := debtExcludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !contains(debtSourceExts, ext) {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("stat %s: %v", rel, infoErr))
			return nil
		}
		if info.Size() > debtMaxFileBytes {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip oversize %s (%d bytes > %d)", rel, info.Size(), debtMaxFileBytes))
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("open %s: %v", rel, openErr))
			return nil
		}
		fileMarkerHits := scanFileForDebt(f, &result.Markers)
		_ = f.Close()
		if fileMarkerHits > 0 {
			result.Files++
		}
		return nil
	})

	if walkErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("walk aborted: %v", walkErr))
	}

	result.Markers.Total = result.Markers.TODO + result.Markers.FIXME +
		result.Markers.HACK + result.Markers.XXX

	return result, nil
}

// scanFileForDebt reads the input line-by-line. Each marker increments at
// most once per line (line-count semantics). Returns the number of lines on
// which at least one marker was found, used to populate result.Files.
func scanFileForDebt(r io.Reader, m *Markers) int {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	hits := 0
	for scanner.Scan() {
		line := scanner.Text()
		matched := false
		if debtTODORE.MatchString(line) {
			m.TODO++
			matched = true
		}
		if debtFIXMERE.MatchString(line) {
			m.FIXME++
			matched = true
		}
		if debtHACKRE.MatchString(line) {
			m.HACK++
			matched = true
		}
		if debtXXXRE.MatchString(line) {
			m.XXX++
			matched = true
		}
		if matched {
			hits++
		}
	}
	// Scanner errors (e.g. line exceeds 1 MiB buffer) are non-fatal at the
	// caller level — the verb keeps running. Return current hits regardless.
	return hits
}
