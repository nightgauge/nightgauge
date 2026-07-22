// Package docs provides deterministic markdown documentation operations.
// FreshnessResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge docs check-freshness
// --json` output; any breaking change requires incrementing the V field.
//
// The check-freshness verb replaces the bash + git log prose in
// update-docs Phase 4.8 (audit row B36).
package docs

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FreshnessResult is the stable JSON output schema for
// `nightgauge docs check-freshness`. Schema version 1 — do not rename
// or remove fields after first merge.
type FreshnessResult struct {
	V                        int            `json:"v"`                           // schema version, always 1
	Root                     string         `json:"root"`                        // absolute path scanned
	FilesScanned             int            `json:"files_scanned"`               // count of markdown files inspected
	FilesWithUpdatedMetadata int            `json:"files_with_updated_metadata"` // count with an "Updated:" line
	StaleFindings            []StaleFinding `json:"stale_findings"`              // one entry per stale file
	StaleCount               int            `json:"stale_count"`                 // len(StaleFindings)
	Warnings                 []string       `json:"warnings"`                    // non-fatal scan warnings
}

// StaleFinding records a single file whose "Updated:" metadata lags behind
// the most recent git commit that touched that file.
type StaleFinding struct {
	File           string `json:"file"`            // path relative to Root
	Line           int    `json:"line"`            // 1-based line number of the "Updated:" metadata
	DocumentedDate string `json:"documented_date"` // date string extracted from the file (YYYY-MM-DD)
	GitDate        string `json:"git_date"`        // date of most recent git commit touching the file (YYYY-MM-DD)
	DaysStale      int    `json:"days_stale"`      // (git_date - documented_date) in whole days
}

// CheckFreshnessOptions controls a single check-freshness run.
type CheckFreshnessOptions struct {
	// Root is the directory tree to scan. When empty, the caller's CWD is used.
	Root string
	// GitRunner allows tests to substitute a mock git executor. When nil the
	// real git binary is used. Returns (dateString, warning); warning is
	// non-empty when the runner encountered a non-fatal problem.
	GitRunner func(file string) (string, string)
}

// updatedRe matches "Updated: YYYY-MM-DD" in several common prose patterns:
//   - Updated: 2026-01-15
//   - **Updated**: 2026-01-15
//   - | Updated | 2026-01-15 |
var updatedRe = regexp.MustCompile(`(?i)\bupdated\b[^0-9\r\n]*(\d{4}-\d{2}-\d{2})`)

// dateLayout is the only date format parsed/emitted by this command.
const dateLayout = "2006-01-02"

// freshnessSkipDirs mirrors the skip list used across other docs commands.
var freshnessSkipDirs = map[string]struct{}{
	"node_modules":     {},
	".git":             {},
	"dist":             {},
	"build":            {},
	".nightgauge": {},
}

// CheckFreshness detects markdown files whose "Updated:" metadata lags behind
// the most recent git commit that touched each file.
func CheckFreshness(_ context.Context, opts CheckFreshnessOptions) (*FreshnessResult, error) {
	root := opts.Root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}

	gitRunner := opts.GitRunner
	if gitRunner == nil {
		gitRunner = defaultGitRunner(abs)
	}

	result := &FreshnessResult{
		V:    1,
		Root: abs,
	}

	err = filepath.Walk(abs, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk error at %s: %v", path, werr))
			return nil
		}
		if info.IsDir() {
			if _, skip := freshnessSkipDirs[info.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		result.FilesScanned++
		rel, _ := filepath.Rel(abs, path)

		docDate, lineNum, warn := extractUpdatedDate(path, rel)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
		if docDate.IsZero() {
			return nil
		}

		result.FilesWithUpdatedMetadata++

		gitDateStr, gitWarn := gitRunner(path)
		if gitWarn != "" {
			result.Warnings = append(result.Warnings, gitWarn)
			return nil
		}
		if gitDateStr == "" {
			// Untracked file — cannot determine git date.
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: not tracked by git — skipping freshness check", rel))
			return nil
		}

		gitDate, err := time.Parse(dateLayout, strings.TrimSpace(gitDateStr))
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: could not parse git date %q: %v", rel, gitDateStr, err))
			return nil
		}

		if gitDate.After(docDate) {
			days := int(math.Round(gitDate.Sub(docDate).Hours() / 24))
			result.StaleFindings = append(result.StaleFindings, StaleFinding{
				File:           rel,
				Line:           lineNum,
				DocumentedDate: docDate.Format(dateLayout),
				GitDate:        gitDate.Format(dateLayout),
				DaysStale:      days,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", abs, err)
	}

	result.StaleCount = len(result.StaleFindings)
	return result, nil
}

// extractUpdatedDate scans a markdown file for the first "Updated: YYYY-MM-DD"
// line and returns the parsed date, the 1-based line number, and an optional
// warning. Code fences are skipped. Returns a zero Time when no match is found.
func extractUpdatedDate(absPath, relPath string) (time.Time, int, string) {
	f, err := os.Open(absPath)
	if err != nil {
		return time.Time{}, 0, fmt.Sprintf("%s: could not read: %v", relPath, err)
	}
	defer f.Close()

	inFence := false
	lineNum := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if fenceOpenRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := updatedRe.FindStringSubmatch(line); len(m) == 2 {
			d, err := time.Parse(dateLayout, m[1])
			if err != nil {
				return time.Time{}, lineNum, fmt.Sprintf("%s:%d: malformed date %q — skipping", relPath, lineNum, m[1])
			}
			return d, lineNum, ""
		}
	}
	return time.Time{}, 0, ""
}

// defaultGitRunner returns a GitRunner that invokes the real git binary. The
// returned function calls `git log -1 --format=%cs -- <file>` and returns the
// ISO date string of the most recent commit touching that file, or an empty
// string when the file has no git history.
func defaultGitRunner(repoRoot string) func(file string) (string, string) {
	return func(file string) (string, string) {
		cmd := exec.Command("git", "log", "-1", "--format=%cs", "--", file)
		cmd.Dir = repoRoot
		out, err := cmd.Output()
		if err != nil {
			// Exit 128 means git could not find the repo or file — treat as warning,
			// not hard error.
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 128 {
				rel, _ := filepath.Rel(repoRoot, file)
				return "", fmt.Sprintf("%s: git unavailable or not a git repo — skipping", rel)
			}
			return "", "" // other errors: silently skip
		}
		return strings.TrimSpace(string(out)), ""
	}
}
