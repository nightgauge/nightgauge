package health

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The health series lives inside CHECKOUT (.git/nightgauge-worktree/health/,
// ADR-024 § 7). Gate metrics history lives in CLONE logs instead
// (state.GateMetricsHistoryName), because pipeline runs record them from linked
// worktrees whose CHECKOUT is deleted on cleanup (#2535).
const trendsName = layout.CheckoutHealth + "/trends.jsonl"

// TrendsPath is the absolute path of the checkout's health series.
func TrendsPath(workspaceRoot string) (string, error) {
	return layout.CheckoutPath(workspaceRoot, trendsName)
}

// ReadTrends reads the last N entries from the checkout's health/trends.jsonl.
// When limit is 0 all entries are returned. If the file does not exist an empty
// slice is returned without error. A line of any schema other than
// TrendSchemaVersion is skipped with a warning: the series has one schema.
func ReadTrends(workspaceRoot string, limit int) ([]HealthTrendEntry, error) {
	filePath, err := TrendsPath(workspaceRoot)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []HealthTrendEntry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []HealthTrendEntry
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry HealthTrendEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipped malformed line %d in trends.jsonl: %v\n", lineNum, err)
			continue
		}
		if entry.SchemaVersion != TrendSchemaVersion {
			fmt.Fprintf(os.Stderr, "warning: skipped line %d in trends.jsonl: schema_version %q, want %q\n",
				lineNum, entry.SchemaVersion, TrendSchemaVersion)
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read trends: %w", err)
	}

	if limit > 0 && len(entries) > limit {
		return entries[len(entries)-limit:], nil
	}
	return entries, nil
}

// ReadGateMetrics reads all entries from the clone's cross-run gate-metrics
// history (logs/gate-metrics.jsonl), which every checkout and linked worktree
// of the clone appends to. If the file does not exist an empty slice is
// returned without error. Malformed lines are skipped and reported in a single
// summary warning on stderr.
func ReadGateMetrics(workspaceRoot string) ([]GateMetricsEntry, error) {
	return readGateMetrics(workspaceRoot, os.Stderr)
}

func readGateMetrics(workspaceRoot string, warn io.Writer) ([]GateMetricsEntry, error) {
	filePath, err := layout.ClassFilePath(workspaceRoot, layout.ClassLogs, state.GateMetricsHistoryName)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []GateMetricsEntry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []GateMetricsEntry
	scanner := bufio.NewScanner(f)
	malformed := 0

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry GateMetricsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			malformed++
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read gate metrics: %w", err)
	}
	if malformed > 0 {
		fmt.Fprintf(warn, "warning: skipped %d malformed line(s) in %s\n", malformed, filePath)
	}
	return entries, nil
}
