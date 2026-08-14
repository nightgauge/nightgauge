package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// History repair (#141).
//
// The writers were fixed so a run produces exactly one record, but history
// already on disk carries the damage: one run accumulated hundreds of records
// because two finalizers keyed the same run differently, and a large share of
// those duplicates are skeletons that overwrote nothing but inflate every
// count-based reading of the store (sample sizes, calibration cells, cost
// trends). This repairs that corpus in place.
//
// The repair is a REPORT by default and only rewrites under an explicit opt-in,
// because collapsing records is destructive and the operator should see the
// shape of the damage before agreeing to it.

// RepairGroup is one run's worth of records found in a history directory,
// after grouping by run identity.
type RepairGroup struct {
	// Key is the identity the records were grouped under: "run:<uuid>" when a
	// run_id is present, else the repo/issue/instant fallback.
	Key string `json:"key"`
	// Repo is the repository the kept record names, empty when the record
	// carries no repo (the cross-contamination shape — such a record cannot be
	// attributed to a repository at all).
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	RunID       string `json:"run_id,omitempty"`
	StartedAt   string `json:"started_at"`
	// Records is how many records this one run occupies today.
	Records int `json:"records"`
	// Dropped is how many of them the repair would discard (Records - 1).
	Dropped int `json:"dropped"`
	// KeptStages / KeptStagesWithTokens describe the survivor, so a reviewer
	// can confirm the repair keeps the richest record rather than a skeleton.
	KeptStages           int `json:"kept_stages"`
	KeptStagesWithTokens int `json:"kept_stages_with_tokens"`
}

// RepairFile is the per-daily-file summary of a repair.
type RepairFile struct {
	Path    string `json:"path"`
	Lines   int    `json:"lines"`
	Kept    int    `json:"kept"`
	Dropped int    `json:"dropped"`
}

// RepairReport is the full outcome of a history repair — the same structure
// whether the run was a dry run or an applied rewrite.
type RepairReport struct {
	Dir string `json:"dir"`
	// Applied distinguishes a report of what WOULD happen from one describing
	// what did.
	Applied bool         `json:"applied"`
	Files   []RepairFile `json:"files"`
	// LinesScanned counts every JSONL line read, including non-run records.
	LinesScanned int `json:"lines_scanned"`
	// RunRecords is how many run records were found, DistinctRuns how many
	// actual runs they represent. The ratio is the corruption factor.
	RunRecords   int `json:"run_records"`
	DistinctRuns int `json:"distinct_runs"`
	Duplicates   int `json:"duplicates"`
	// NonRunRecords pass through untouched — outcome records, and any line the
	// repair could not parse (preserved verbatim rather than discarded).
	NonRunRecords int `json:"non_run_records"`
	// Unattributed counts run records carrying no repo. These are the
	// cross-contamination shape: they may belong to a different repository than
	// the directory they sit in, and nothing on the record can say which. The
	// repair does NOT relocate them — it only reports the count, because
	// guessing a destination would compound the original error.
	Unattributed int `json:"unattributed"`
	// ForeignRepos lists repos named by records in this directory other than
	// the dominant one, with counts. Non-empty means this directory absorbed
	// another repository's runs.
	ForeignRepos map[string]int `json:"foreign_repos,omitempty"`
	// Groups is sorted by Dropped descending — worst offenders first.
	Groups []RepairGroup `json:"groups"`
}

// repairLine is one JSONL line held as both raw bytes (so a rewrite preserves
// every field, including ones this binary does not model) and a decoded record
// used only for identity and richness.
type repairLine struct {
	raw    []byte
	rec    V2RunRecord
	isRun  bool
	seq    int // original position, for stable ordering
	fileIx int
}

// repairKey is the identity a repair groups records under: run_id when present,
// else repo + issue + the started_at instant bucketed to the second (the same
// tolerance runRecordKey applies, which is what makes the two writers' differing
// timestamp formats agree).
func repairKey(rec V2RunRecord) string {
	if rec.RunID != "" {
		return "run:" + rec.RunID
	}
	return fmt.Sprintf("%s|%s", rec.Repo, fallbackRunKey(rec.IssueNumber, rec.StartedAt))
}

// stagesWithTokenData counts stages carrying per-stage token data. This is the
// primary richness signal for the repair: the skeletons written by the
// redundant finalizers have stage entries but no per-stage tokens, so counting
// stages alone would sometimes keep a skeleton over the authoritative record.
func stagesWithTokenData(rec V2RunRecord) int {
	n := 0
	for _, u := range rec.Tokens.PerStage {
		if u.Input > 0 || u.Output > 0 || u.CacheRead > 0 || u.CostUSD > 0 {
			n++
		}
	}
	return n
}

// richerThan reports whether a should be kept over b. Ordered by: per-stage
// token coverage, then stage count, then recorded cost, then run identity
// (a record with a run_id beats one without), then the later recorded_at so
// the comparison is total and the repair is deterministic.
func richerThan(a, b V2RunRecord) bool {
	if at, bt := stagesWithTokenData(a), stagesWithTokenData(b); at != bt {
		return at > bt
	}
	if as, bs := len(a.Stages), len(b.Stages); as != bs {
		return as > bs
	}
	if a.Tokens.EstimatedCostUSD != b.Tokens.EstimatedCostUSD {
		return a.Tokens.EstimatedCostUSD > b.Tokens.EstimatedCostUSD
	}
	if (a.RunID != "") != (b.RunID != "") {
		return a.RunID != ""
	}
	if (a.Repo != "") != (b.Repo != "") {
		return a.Repo != ""
	}
	return a.RecordedAt > b.RecordedAt
}

// RepairHistory de-duplicates the run records in a workspace's history
// directory. With apply=false it reports what it would do and touches nothing.
// With apply=true it rewrites each daily file to hold one record per run and
// rebuilds index.json from the result.
//
// Non-run records and unparseable lines are always preserved verbatim.
func RepairHistory(workspaceRoot string, apply bool) (*RepairReport, error) {
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	report := &RepairReport{Dir: dir, Applied: apply, ForeignRepos: map[string]int{}}

	// Pass 1: read every line, decide the survivor for each run identity.
	// Grouping spans files because a run's duplicates can straddle midnight.
	perFile := make([][]repairLine, len(files))
	best := map[string]*repairLine{}
	repoCounts := map[string]int{}

	for fi, name := range files {
		path := filepath.Join(dir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", name, readErr)
		}
		for _, line := range splitLines(data) {
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			report.LinesScanned++
			rl := repairLine{raw: append([]byte(nil), line...), seq: report.LinesScanned, fileIx: fi}

			var rec V2RunRecord
			if json.Unmarshal(line, &rec) == nil && rec.RecordType == "run" {
				rl.isRun = true
				rl.rec = rec
				report.RunRecords++
				if rec.Repo == "" {
					report.Unattributed++
				} else {
					repoCounts[rec.Repo]++
				}
				k := repairKey(rec)
				if cur, ok := best[k]; !ok || richerThan(rec, cur.rec) {
					best[k] = &rl
				}
			} else {
				report.NonRunRecords++
			}
			perFile[fi] = append(perFile[fi], rl)
		}
	}

	report.DistinctRuns = len(best)
	report.Duplicates = report.RunRecords - report.DistinctRuns

	// Any repo other than the dominant one is a record this directory should
	// not be holding.
	dominant, dominantN := "", 0
	for repo, n := range repoCounts {
		if n > dominantN {
			dominant, dominantN = repo, n
		}
	}
	for repo, n := range repoCounts {
		if repo != dominant {
			report.ForeignRepos[repo] = n
		}
	}
	if len(report.ForeignRepos) == 0 {
		report.ForeignRepos = nil
	}

	// Group summaries, worst first.
	counts := map[string]int{}
	for _, lines := range perFile {
		for _, rl := range lines {
			if rl.isRun {
				counts[repairKey(rl.rec)]++
			}
		}
	}
	for k, win := range best {
		g := RepairGroup{
			Key:                  k,
			Repo:                 win.rec.Repo,
			IssueNumber:          win.rec.IssueNumber,
			RunID:                win.rec.RunID,
			StartedAt:            win.rec.StartedAt,
			Records:              counts[k],
			Dropped:              counts[k] - 1,
			KeptStages:           len(win.rec.Stages),
			KeptStagesWithTokens: stagesWithTokenData(win.rec),
		}
		report.Groups = append(report.Groups, g)
	}
	sort.Slice(report.Groups, func(i, j int) bool {
		if report.Groups[i].Dropped != report.Groups[j].Dropped {
			return report.Groups[i].Dropped > report.Groups[j].Dropped
		}
		return report.Groups[i].Key < report.Groups[j].Key
	})

	// Pass 2: per-file kept/dropped tallies, and the rewrite when applying.
	for fi, name := range files {
		path := filepath.Join(dir, name)
		rf := RepairFile{Path: path, Lines: len(perFile[fi])}
		var kept [][]byte
		for _, rl := range perFile[fi] {
			survives := !rl.isRun || best[repairKey(rl.rec)].seq == rl.seq
			if survives {
				rf.Kept++
				kept = append(kept, rl.raw)
			} else {
				rf.Dropped++
			}
		}
		report.Files = append(report.Files, rf)

		if !apply || rf.Dropped == 0 {
			continue
		}
		if writeErr := rewriteJSONL(path, kept); writeErr != nil {
			return nil, fmt.Errorf("rewrite %s: %w", name, writeErr)
		}
	}

	if apply {
		// The index is a projection of the JSONL, so it must be rebuilt from
		// the repaired files or it keeps pointing at discarded records.
		hw := &HistoryWriter{dir: dir}
		idx := V2Index{SchemaVersion: historyIndexSchemaVersion, Entries: hw.rebuildIndexEntriesFromJSONL()}
		idx.TotalRuns = len(idx.Entries)
		idx.UpdatedAt = time.Now().Format(time.RFC3339)
		if err := writeIndexAtomic(filepath.Join(dir, "index.json"), idx); err != nil {
			return nil, fmt.Errorf("rebuild index: %w", err)
		}
		// The process-wide idempotency ledger for this directory was seeded from
		// the pre-repair index; leaving it in place would make a writer in this
		// process reject records the repair just re-established the shape of.
		dirCoordinatorsMu.Lock()
		delete(dirCoordinators, dir)
		dirCoordinatorsMu.Unlock()
	}

	return report, nil
}

// rewriteJSONL replaces a daily file with the kept lines, via a temp file and
// rename so an interrupted repair cannot leave a half-written history.
func rewriteJSONL(path string, kept [][]byte) error {
	tmp := path + ".repair.tmp"
	var buf []byte
	for _, line := range kept {
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
