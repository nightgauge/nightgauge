package scanfailures

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FailurePatterns is the canonical 16-pattern set used to extract failure
// signals from pipeline session logs. Source of truth:
// skills/nightgauge-retro/SKILL.md Phase 2.3 (L417-L434).
//
// NOTE: This list is the *signal-extraction* set. Failure *classification*
// (bucketing into one of 7 categories) is owned by `failure classify`
// (audit row B44) and lives in scripts/retro/classifiers/failure_classifier.py.
// Keep the two responsibilities separate — the B29 motivation is precisely
// that this 16-pattern set was duplicated; a third copy here would worsen
// the drift signal.
var FailurePatterns = []string{
	`\[ERROR\]`,
	`\[FAIL\]`,
	`budget exceeded`,
	`token limit`,
	`timed out`,
	`exceeded.*timeout`,
	`ci.*fail`,
	`workflow.*fail`,
	`tests? failed`,
	`tsc.*error`,
	`build failed`,
	`context file.*missing`,
	`json.*parse.*error`,
	`model returned empty`,
	`unexpected.*output`,
	`stage.*cancelled`,
}

// failurePatternRE is the compiled, case-insensitive disjunction of
// FailurePatterns. Compiled once at package init for runtime efficiency.
var failurePatternRE = regexp.MustCompile(`(?i)(?:` + strings.Join(FailurePatterns, "|") + `)`)

// maxLineCapture matches the [:300] slice in retro Phase 2.3 — bounds each
// captured line to 300 bytes so unbounded log lines (e.g. base64 payloads)
// don't blow up output size.
const maxLineCapture = 300

// Options controls a single Scan run.
type Options struct {
	// Workdir is the project root. Defaults to the current working
	// directory if empty.
	Workdir string
	// Issue keeps only logs whose filename embeds this issue number
	// (`YYYY-MM-DD_NNN_session.log`). 0 = all.
	Issue int
	// Since is a YYYY-MM-DD lower bound applied to the date prefix in the
	// log filename. Empty string = unbounded.
	Since string
}

// Scan walks .nightgauge/logs/*_session.log under workdir and returns
// the consolidated Result. Missing logs directory is treated as zero matches
// (matches the existing Python behavior). Per-file IO errors are recorded as
// warnings; only structural errors (e.g., unreadable workdir) fail.
func Scan(opts Options) (Result, error) {
	workdir := opts.Workdir
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Result{}, fmt.Errorf("getwd: %w", err)
		}
		workdir = wd
	}

	result := Result{
		V: SchemaVersion,
		Filters: AppliedFilters{
			Issue:   opts.Issue,
			Since:   opts.Since,
			Workdir: workdir,
		},
		LogSignals: []LogFileSignals{},
		Warnings:   []string{},
	}

	logsDir := filepath.Join(workdir, ".nightgauge", "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read %s: %w", logsDir, err)
	}

	var logFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, "_session.log") {
			continue
		}
		logFiles = append(logFiles, name)
	}
	sort.Strings(logFiles)

	for _, name := range logFiles {
		date, issuePtr := parseLogFilename(name)
		if opts.Since != "" && date < opts.Since {
			continue
		}
		if opts.Issue != 0 {
			if issuePtr == nil || *issuePtr != opts.Issue {
				continue
			}
		}

		result.LogFilesScanned++
		matches, err := scanFile(filepath.Join(logsDir, name))
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("log %s: %v", name, err))
			continue
		}
		if len(matches) == 0 {
			continue
		}
		result.LogSignals = append(result.LogSignals, LogFileSignals{
			LogFile:        name,
			IssueNumber:    issuePtr,
			Date:           date,
			FailureSignals: matches,
		})
	}
	result.FilesWithSignals = len(result.LogSignals)

	return result, nil
}

// scanFile reads a single session log and returns matched lines, capped at
// MaxSignalsPerFile. Each line is truncated to maxLineCapture bytes.
func scanFile(path string) ([]SignalMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	matches := make([]SignalMatch, 0, 8)
	scanner := bufio.NewScanner(f)
	// Allow long lines (some session logs include base64 payloads). 1 MB
	// per line is enough for any realistic line.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !failurePatternRE.MatchString(line) {
			continue
		}
		text := strings.TrimSpace(line)
		if len(text) > maxLineCapture {
			text = text[:maxLineCapture]
		}
		matches = append(matches, SignalMatch{Line: lineNo, Text: text})
		if len(matches) >= MaxSignalsPerFile {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return matches, err
	}
	return matches, nil
}

// parseLogFilename extracts the date prefix and optional issue number from a
// session log filename. Mirrors the split-and-isdigit logic in retro Phase 2.3.
//
// Recognized forms:
//   - YYYY-MM-DD_session.log            → date=YYYY-MM-DD, issue=nil
//   - YYYY-MM-DD_NNN_session.log        → date=YYYY-MM-DD, issue=*NNN
//   - YYYY-MM-DD_NNN_<extra>_session.log → date=YYYY-MM-DD, issue=*NNN (matches Python parts[1] semantic)
func parseLogFilename(name string) (string, *int) {
	parts := strings.Split(name, "_")
	if len(parts) == 0 {
		return "", nil
	}
	date := parts[0]
	if len(parts) < 2 {
		return date, nil
	}
	if n, err := strconv.Atoi(parts[1]); err == nil {
		return date, &n
	}
	return date, nil
}
