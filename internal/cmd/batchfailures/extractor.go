package batchfailures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// canonicalStages mirrors the ALL_STAGES list from retro Phase 2.1 — the
// canonical set used to derive the failed_stages diff against
// completedStages on each batch-state issueResults entry.
var canonicalStages = []string{
	"pipeline-start",
	"issue-pickup",
	"feature-planning",
	"feature-dev",
	"feature-validate",
	"pr-create",
	"pr-merge",
	"pipeline-finish",
}

// Options controls a single Extract run.
type Options struct {
	// Workdir is the project root. Defaults to the current working
	// directory if empty.
	Workdir string
	// Issue keeps only failure rows with this issue number. 0 = all.
	Issue int
	// Since is a YYYY-MM-DD lower bound applied to history JSONL filenames.
	// Empty string = unbounded. Ignored for batch-state and context-files
	// sources (which carry no per-row date that maps to a daily file).
	Since string
	// AllFailures, when true, disables the Since filter for history
	// failures. Mirrors the ALL_FAILURES toggle from retro Phase 2.2.
	AllFailures bool
}

// Extract reads pipeline state files under workdir and returns the
// consolidated Result. Missing files are treated as zero-row inputs (matches
// the existing Python parsers' behavior); only structural errors fail.
func Extract(opts Options) (Result, error) {
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
			Issue:       opts.Issue,
			Since:       opts.Since,
			AllFailures: opts.AllFailures,
			Workdir:     workdir,
		},
		BatchFailures:   []BatchFailure{},
		HistoryFailures: []HistoryFailure{},
		ContextFailures: []ContextFileFailure{},
		Warnings:        []string{},
	}

	if err := extractBatchState(workdir, opts, &result); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("batch-state: %v", err))
	}

	if err := extractHistory(workdir, opts, &result); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("history: %v", err))
	}

	if err := extractContextFiles(workdir, opts, &result); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("context-files: %v", err))
	}

	return result, nil
}

// extractBatchState reads .nightgauge/pipeline/batch-state.json (when
// present) and appends failure rows to result.BatchFailures. Missing file is
// not an error.
func extractBatchState(workdir string, opts Options, result *Result) error {
	path := filepath.Join(workdir, ".nightgauge", "pipeline", "batch-state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	var batch struct {
		Status       string `json:"status"`
		StartedAt    string `json:"started_at"`
		UpdatedAt    string `json:"updated_at"`
		IssueResults []struct {
			IssueNumber     int            `json:"issueNumber"`
			Title           string         `json:"title"`
			Status          string         `json:"status"`
			CompletedStages []string       `json:"completedStages"`
			DurationMs      int64          `json:"durationMs"`
			TokenUsage      map[string]any `json:"tokenUsage"`
		} `json:"issueResults"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	result.Batch = &BatchSummary{
		BatchStatus:    batch.Status,
		BatchStartedAt: batch.StartedAt,
		BatchUpdatedAt: batch.UpdatedAt,
		TotalIssues:    len(batch.IssueResults),
	}

	for _, item := range batch.IssueResults {
		if opts.Issue != 0 && item.IssueNumber != opts.Issue {
			continue
		}

		completed := item.CompletedStages
		if completed == nil {
			completed = []string{}
		}
		failedStages := diffStages(canonicalStages, completed)

		if item.Status == "completed" && len(failedStages) == 0 {
			continue
		}

		usage := item.TokenUsage
		if usage == nil {
			usage = map[string]any{}
		}

		result.BatchFailures = append(result.BatchFailures, BatchFailure{
			IssueNumber:     item.IssueNumber,
			Title:           item.Title,
			Status:          fallbackStatus(item.Status),
			CompletedStages: completed,
			FailedStages:    failedStages,
			DurationMs:      item.DurationMs,
			TokenUsage:      usage,
			Source:          SourceBatchState,
		})
	}

	return nil
}

// extractHistory walks .nightgauge/pipeline/history/*.jsonl and appends
// failure rows to result.HistoryFailures. The Since filter pre-filters by
// filename stem (YYYY-MM-DD); per-line filters apply Issue and outcome
// matching. Malformed JSON lines are counted in result.SkippedRecords (matches
// the Python skipped_records semantics).
func extractHistory(workdir string, opts Options, result *Result) error {
	historyDir := filepath.Join(workdir, ".nightgauge", "pipeline", "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", historyDir, err)
	}

	var jsonlFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		jsonlFiles = append(jsonlFiles, name)
	}
	sort.Strings(jsonlFiles)

	for _, name := range jsonlFiles {
		stem := strings.TrimSuffix(name, ".jsonl")
		if !opts.AllFailures && opts.Since != "" && stem < opts.Since {
			continue
		}
		path := filepath.Join(historyDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("history file %s: %v", name, err))
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var rec struct {
				RecordType      string                    `json:"record_type"`
				IssueNumber     int                       `json:"issue_number"`
				Title           string                    `json:"title"`
				Outcome         string                    `json:"outcome"`
				StartedAt       string                    `json:"started_at"`
				TotalDurationMs int64                     `json:"total_duration_ms"`
				Stages          map[string]map[string]any `json:"stages"`
				Tokens          struct {
					EstimatedCostUSD float64 `json:"estimated_cost_usd"`
				} `json:"tokens"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				result.SkippedRecords++
				continue
			}
			if rec.RecordType != "" && rec.RecordType != "run" {
				continue
			}
			if opts.Issue != 0 && rec.IssueNumber != opts.Issue {
				continue
			}

			stageFailures := map[string]string{}
			for stageName, detail := range rec.Stages {
				statusVal, _ := detail["status"].(string)
				if isFailureStatus(statusVal) {
					stageFailures[stageName] = statusVal
				}
			}

			if rec.Outcome == "complete" && len(stageFailures) == 0 {
				continue
			}

			outcome := rec.Outcome
			if outcome == "" {
				outcome = "unknown"
			}

			result.HistoryFailures = append(result.HistoryFailures, HistoryFailure{
				IssueNumber:      rec.IssueNumber,
				Title:            rec.Title,
				Outcome:          outcome,
				StartedAt:        rec.StartedAt,
				TotalDurationMs:  rec.TotalDurationMs,
				StageFailures:    stageFailures,
				EstimatedCostUSD: rec.Tokens.EstimatedCostUSD,
				Source:           SourceHistory,
			})
		}
	}

	return nil
}

// extractContextFiles scans .nightgauge/pipeline/issue-*.json files and
// appends a row when the matching pr-{N}.json is absent (implying the run
// did not reach PR creation). Mirrors retro Phase 2.4.
func extractContextFiles(workdir string, opts Options, result *Result) error {
	pipelineDir := filepath.Join(workdir, ".nightgauge", "pipeline")
	entries, err := os.ReadDir(pipelineDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", pipelineDir, err)
	}

	issueNums := map[int]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "issue-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "issue-"), ".json")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		issueNums[n] = struct{}{}
	}

	sortedNums := make([]int, 0, len(issueNums))
	for n := range issueNums {
		sortedNums = append(sortedNums, n)
	}
	sort.Ints(sortedNums)

	for _, n := range sortedNums {
		if opts.Issue != 0 && n != opts.Issue {
			continue
		}
		hasPR := fileExists(filepath.Join(pipelineDir, fmt.Sprintf("pr-%d.json", n)))
		if hasPR {
			continue
		}
		hasDev := fileExists(filepath.Join(pipelineDir, fmt.Sprintf("dev-%d.json", n)))
		result.ContextFailures = append(result.ContextFailures, ContextFileFailure{
			IssueNumber:     n,
			HasDevContext:   hasDev,
			Source:          SourceContextFiles,
			InferredFailure: "no pr context found — pipeline likely did not complete",
		})
	}

	return nil
}

// diffStages returns elements of canonical that are not present in completed.
// Order follows canonical to keep JSON output deterministic.
func diffStages(canonical, completed []string) []string {
	completedSet := make(map[string]struct{}, len(completed))
	for _, s := range completed {
		completedSet[s] = struct{}{}
	}
	out := make([]string, 0, len(canonical))
	for _, s := range canonical {
		if _, ok := completedSet[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// isFailureStatus reports whether a stage status string indicates failure.
// Mirrors the retro Phase 2.2 set: failed | error | timeout | cancelled.
func isFailureStatus(s string) bool {
	switch s {
	case "failed", "error", "timeout", "cancelled":
		return true
	default:
		return false
	}
}

func fallbackStatus(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
