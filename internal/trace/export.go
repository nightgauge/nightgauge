package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
)

// ExportDoc is the single ordered document `nightgauge trace export` emits:
// the run's trace events joined with the RunRecord spine and the stage-exit
// forensic records that share its run_id (ADR 013 §Export).
type ExportDoc struct {
	RunID string `json:"run_id"`
	Repo  string `json:"repo,omitempty"`
	Issue int    `json:"issue,omitempty"`
	// Events is the full trace, ordered by (ts, producer, seq).
	Events []Event `json:"events"`
	// RunRecord is the V3 run record whose run_id matches, when one exists.
	// Records written before run_id threading (#179) cannot be joined and
	// leave this null.
	RunRecord *state.V2RunRecord `json:"run_record,omitempty"`
	// ExitRecords are the stage-exit forensic records for this run, in
	// timestamp order.
	ExitRecords []diagnostics.StageExitRecord `json:"exit_records,omitempty"`
}

// exportHistoryLookback bounds how many daily history files the RunRecord
// join scans. Traces are exported for recent runs; 30 days is generous.
const exportHistoryLookback = 30

// Export builds the joined per-run document for runID. The trace file is the
// spine: exporting a run with no trace file returns an error (unlike ReadRun,
// which treats missing-as-empty, export exists to inspect a captured trace).
func Export(rootDir, runID string) (*ExportDoc, error) {
	events, err := ReadRun(rootDir, runID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("trace: no trace recorded for run %s", runID)
	}

	doc := &ExportDoc{
		RunID:  runID,
		Repo:   events[0].Repo,
		Issue:  events[0].Issue,
		Events: events,
	}

	// Join the V3 RunRecord by run_id. Best-effort: a missing or pre-#179
	// record leaves RunRecord null rather than failing the export.
	hw := state.NewHistoryWriter(rootDir)
	if records, readErr := hw.ReadRecentV2(0, exportHistoryLookback); readErr == nil {
		for i := len(records) - 1; i >= 0; i-- {
			if records[i].RunID == runID {
				rec := records[i]
				doc.RunRecord = &rec
				break
			}
		}
	}

	// Join stage-exit records by run_id across the daily files.
	exitRecords, err := readExitRecordsForRun(rootDir, runID)
	if err == nil {
		doc.ExitRecords = exitRecords
	}

	return doc, nil
}

// readExitRecordsForRun scans every daily exit-records file and returns the
// records whose run_id matches, in timestamp order.
func readExitRecordsForRun(rootDir, runID string) ([]diagnostics.StageExitRecord, error) {
	dir := diagnostics.ExitRecordsDir(rootDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("trace: read exit-records dir: %w", err)
	}

	var out []diagnostics.StageExitRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec diagnostics.StageExitRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}
			if rec.RunID == runID {
				out = append(out, rec)
			}
		}
		f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out, nil
}
