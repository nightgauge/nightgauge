package health

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadTrends reads the last N entries from .nightgauge/health/trends.jsonl.
// When limit is 0 all entries are returned. If the file does not exist an empty
// slice is returned without error.
func ReadTrends(workspaceRoot string, limit int) ([]HealthTrendEntry, error) {
	filePath := filepath.Join(workspaceRoot, ".nightgauge", "health", "trends.jsonl")
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

// ReadGateMetrics reads all entries from .nightgauge/health/gate-metrics.jsonl.
// If the file does not exist an empty slice is returned without error.
func ReadGateMetrics(workspaceRoot string) ([]GateMetricsEntry, error) {
	filePath := filepath.Join(workspaceRoot, ".nightgauge", "health", "gate-metrics.jsonl")
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
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry GateMetricsEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipped malformed line %d in gate-metrics.jsonl: %v\n", lineNum, err)
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read gate metrics: %w", err)
	}
	return entries, nil
}
