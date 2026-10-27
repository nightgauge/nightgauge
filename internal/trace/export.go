package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
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
	// IssueRuns, Merge and Survival anchor the run in its issue's full
	// decision chain (#1283). Each is always present and carries a status, so
	// a piece that could not be recovered is stated, never silently omitted.
	IssueRuns IssueRunsJoin `json:"issue_runs"`
	Merge     MergeJoin     `json:"merge"`
	Survival  SurvivalJoin  `json:"survival"`
}

// JoinStatus says whether an export join found its target.
type JoinStatus string

const (
	JoinJoined      JoinStatus = "joined"
	JoinUnavailable JoinStatus = "unavailable"
)

// IssueRunsJoin lists every traced run for the document's issue, oldest
// first, including this one.
type IssueRunsJoin struct {
	Status JoinStatus `json:"status"`
	Reason string     `json:"reason,omitempty"`
	RunIDs []string   `json:"run_ids"`
}

// MergeJoin is the merge this run produced, read from the survival record the
// post-merge hook wrote. MergedBy comes from the forge's PR history at merge
// time; when it was not captured MergedByStatus says so.
type MergeJoin struct {
	Status         JoinStatus `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	PRNumber       int        `json:"pr_number,omitempty"`
	MergeCommitSHA string     `json:"merge_commit_sha,omitempty"`
	MergedAt       string     `json:"merged_at,omitempty"`
	MergedBy       string     `json:"merged_by,omitempty"`
	MergedByStatus JoinStatus `json:"merged_by_status"`
	MergedByReason string     `json:"merged_by_reason,omitempty"`
}

// SurvivalJoin is the post-merge survival outcome of the merge, joined by its
// merge commit SHA. A pending verdict is a real, joined state.
type SurvivalJoin struct {
	Status           JoinStatus `json:"status"`
	Reason           string     `json:"reason,omitempty"`
	Verdict          string     `json:"verdict,omitempty"`
	Evidence         string     `json:"evidence,omitempty"`
	ObservedAt       string     `json:"observed_at,omitempty"`
	MainCheckVerdict string     `json:"main_check_verdict,omitempty"`
}

// mergeJoinGrace extends a run's window past its last trace event: the
// post-merge hook that stamps merged_at runs after the trace's final event.
const mergeJoinGrace = 30 * time.Minute

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

	doc.IssueRuns = joinIssueRuns(rootDir, doc.Issue)
	doc.Merge, doc.Survival = joinMerge(rootDir, doc)

	return doc, nil
}

// joinIssueRuns lists every traced run for issue.
func joinIssueRuns(rootDir string, issue int) IssueRunsJoin {
	if issue == 0 {
		return IssueRunsJoin{Status: JoinUnavailable, Reason: "trace carries no issue number", RunIDs: []string{}}
	}
	ids, err := FindAllRunIDsForIssue(rootDir, issue)
	if err != nil {
		return IssueRunsJoin{Status: JoinUnavailable, Reason: err.Error(), RunIDs: []string{}}
	}
	return IssueRunsJoin{Status: JoinJoined, RunIDs: ids}
}

// joinMerge finds the survival record for the merge this run produced and
// projects it onto the merge and survival joins. Offline: the record was
// written by the post-merge hook; nothing here calls the forge.
func joinMerge(rootDir string, doc *ExportDoc) (MergeJoin, SurvivalJoin) {
	unavailable := func(reason string) (MergeJoin, SurvivalJoin) {
		return MergeJoin{Status: JoinUnavailable, Reason: reason, MergedByStatus: JoinUnavailable, MergedByReason: reason},
			SurvivalJoin{Status: JoinUnavailable, Reason: reason}
	}
	if doc.Issue == 0 {
		return unavailable("trace carries no issue number")
	}
	records, err := survival.NewStore(rootDir).Load()
	if err != nil {
		return unavailable(fmt.Sprintf("survival store unreadable: %v", err))
	}
	rec, ok := matchSurvivalRecord(records, doc.Issue, doc.Repo, doc.Events)
	if !ok {
		return unavailable(fmt.Sprintf("no merge recorded for issue #%d during this run "+
			"(the run did not merge, merged outside the pipeline, or predates survival capture)", doc.Issue))
	}

	merge := MergeJoin{
		Status:         JoinJoined,
		PRNumber:       rec.PRNumber,
		MergeCommitSHA: rec.MergeCommitSHA,
		MergedAt:       rec.MergedAt,
		MergedBy:       rec.MergedBy,
		MergedByStatus: JoinJoined,
	}
	if rec.MergedBy == "" {
		merge.MergedByStatus = JoinUnavailable
		merge.MergedByReason = "merge actor not captured (survival record predates #1283 or the forge returned no mergedBy)"
	}
	surv := SurvivalJoin{
		Status:           JoinJoined,
		Verdict:          string(rec.Verdict),
		Evidence:         rec.Evidence,
		ObservedAt:       rec.ObservedAt,
		MainCheckVerdict: rec.MainCheckVerdict,
	}
	if rec.Verdict == survival.Pending {
		surv.Reason = "survival window still open; verdict not yet finalized"
	}
	return merge, surv
}

// matchSurvivalRecord picks the survival record for a run: same issue, same
// repo when both are known, merged_at inside [first event, last event +
// mergeJoinGrace]. The earliest merge in the window wins. events must be in
// trace order.
func matchSurvivalRecord(records []survival.Record, issue int, repo string, events []Event) (survival.Record, bool) {
	if len(events) == 0 {
		return survival.Record{}, false
	}
	start, err := time.Parse(time.RFC3339, events[0].Ts)
	if err != nil {
		return survival.Record{}, false
	}
	end, err := time.Parse(time.RFC3339, events[len(events)-1].Ts)
	if err != nil {
		return survival.Record{}, false
	}
	end = end.Add(mergeJoinGrace)

	var best survival.Record
	var bestAt time.Time
	found := false
	for _, r := range records {
		if r.IssueNumber != issue {
			continue
		}
		if repo != "" && r.Repo != "" && r.Repo != repo {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.MergedAt)
		if err != nil || at.Before(start) || at.After(end) {
			continue
		}
		if !found || at.Before(bestAt) {
			best, bestAt, found = r, at, true
		}
	}
	return best, found
}

// RunSummary is one row of an issue's run list (`trace list`).
type RunSummary struct {
	RunID          string `json:"run_id"`
	StartedAt      string `json:"started_at"`
	LastEventAt    string `json:"last_event_at"`
	OutcomeSuccess *bool  `json:"outcome_success"`
	Events         int    `json:"events"`
}

// ListIssueRuns summarizes every traced run for issue, oldest first.
func ListIssueRuns(rootDir string, issue int) ([]RunSummary, error) {
	ids, err := FindAllRunIDsForIssue(rootDir, issue)
	if err != nil {
		return nil, err
	}
	out := make([]RunSummary, 0, len(ids))
	for _, id := range ids {
		events, err := ReadRun(rootDir, id)
		if err != nil {
			return nil, err
		}
		sum := RunSummary{RunID: id, Events: len(events)}
		if len(events) > 0 {
			sum.StartedAt = events[0].Ts
			sum.LastEventAt = events[len(events)-1].Ts
		}
		for _, ev := range events {
			if ev.Kind != KindOutcome {
				continue
			}
			if p, ok := ev.Payload.(map[string]any); ok {
				if v, ok := p["success"].(bool); ok {
					sum.OutcomeSuccess = &v
				}
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

// readExitRecordsForRun scans every daily exit-records file and returns the
// records whose run_id matches, in timestamp order.
func readExitRecordsForRun(rootDir, runID string) ([]diagnostics.StageExitRecord, error) {
	dir, err := diagnostics.ExitRecordsDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
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
