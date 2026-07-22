package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/spf13/cobra"
)

// exitRecordsCmd is the top-level "exit-records" command. It exposes
// deterministic readers over the per-stage diagnostic JSONL files written
// during pipeline execution (#3605).
//
// The daily files live at `.nightgauge/pipeline/exit-records/<UTC-day>.jsonl`.
// Each line is one StageExitRecord — see internal/diagnostics for the schema.
func exitRecordsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exit-records",
		Short: "Inspect stage-exit diagnostic records (#3605)",
		Long: `Stage-exit diagnostic records (#3605) — one JSONL line per stage exit
in .nightgauge/pipeline/exit-records/<UTC-day>.jsonl.

Use the 'tail' subcommand to inspect the most recent records (default 20),
optionally filtered to a single issue number. Daily files are ordered
lexicographically and recursively scanned newest-first so you don't have to
guess which day's file holds the failure you're investigating.`,
	}
	cmd.AddCommand(exitRecordsTailCmd())
	return cmd
}

// exitRecordsTailCmd implements `nightgauge exit-records tail`.
//
// Walks the daily files newest-first, collects the last N records (optionally
// filtered to one issue), and prints either JSON (machine-readable) or a
// human-friendly table.
func exitRecordsTailCmd() *cobra.Command {
	var (
		issue      int
		limit      int
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "tail",
		Short:        "Show the most recent stage-exit records",
		SilenceUsage: true,
		Example: `  nightgauge exit-records tail
  nightgauge exit-records tail --limit 50
  nightgauge exit-records tail --issue 3591 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := workdir
			if root == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("getcwd: %w", err)
				}
				root = cwd
			}
			if limit <= 0 {
				limit = 20
			}

			records, err := tailExitRecords(root, issue, limit)
			if err != nil {
				return err
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(records)
			}
			printExitRecordsHuman(records)
			return nil
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Filter to a single issue number (0 = all)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum records to return (most recent first)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON instead of a table")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// tailExitRecords walks every daily file under root/.nightgauge/pipeline/
// exit-records/, returning up to `limit` records, most recent first, with an
// optional issue-number filter.
//
// Strategy: list files lex-descending (== chronologically newest-first because
// the filename is YYYY-MM-DD), read each file fully into memory, then stop
// once we've collected `limit` records. This is cheap for a daily file —
// each line is ≤ a few KB and we never read more days than necessary.
func tailExitRecords(root string, issueFilter, limit int) ([]diagnostics.StageExitRecord, error) {
	dir := diagnostics.ExitRecordsDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read exit-records dir: %w", err)
	}

	// Collect daily filenames lex-descending so the newest day is processed first.
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		files = append(files, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	out := make([]diagnostics.StageExitRecord, 0, limit)
	for _, name := range files {
		path := filepath.Join(dir, name)
		dayRecords, err := readDailyFile(path, issueFilter)
		if err != nil {
			return nil, err
		}
		// Walk this day's records in reverse insertion order — the newest
		// is the last line, so iterate end-to-start.
		for i := len(dayRecords) - 1; i >= 0; i-- {
			out = append(out, dayRecords[i])
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// readDailyFile parses one daily JSONL file applying the optional issue
// filter. Returns records in file order (oldest-first).
func readDailyFile(path string, issueFilter int) ([]diagnostics.StageExitRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []diagnostics.StageExitRecord
	scanner := bufio.NewScanner(f)
	// Daily files can grow large; raise the scanner buffer to 1 MB/line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec diagnostics.StageExitRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// Skip malformed lines rather than aborting the whole tail.
			// A corrupted line in last week's file shouldn't hide today's
			// healthy data.
			fmt.Fprintf(os.Stderr, "warning: skipping malformed line in %s: %v\n", path, err)
			continue
		}
		if issueFilter > 0 && rec.Issue != issueFilter {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return out, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// printExitRecordsHuman renders the tail as a 1-line-per-record table on
// stdout. Width is intentionally compact so an operator can scan a full
// terminal at a glance.
func printExitRecordsHuman(records []diagnostics.StageExitRecord) {
	if len(records) == 0 {
		fmt.Println("No stage-exit records found.")
		return
	}
	for _, rec := range records {
		ts := rec.Timestamp
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			ts = t.Local().Format("2006-01-02 15:04:05")
		}
		status := "ok"
		if !rec.Success {
			status = "FAIL"
			if rec.TerminalKind != "" {
				status = "FAIL/" + rec.TerminalKind
			}
		}
		signal := ""
		if rec.Signal != "" {
			signal = " sig=" + rec.Signal
			if rec.SignalSource != "" {
				signal += "(" + rec.SignalSource + ")"
			}
		}
		repo := rec.Repo
		if repo == "" {
			repo = "?"
		}
		fmt.Printf("%s  %s#%d  %-20s  %-14s  elapsed=%s  idle=%s%s\n",
			ts,
			repo, rec.Issue,
			rec.Stage,
			status,
			fmtMs(rec.ElapsedMs),
			fmtMs(rec.IdleMsAtExit),
			signal,
		)
		if rec.LastBashCommand != "" {
			fmt.Printf("    bash: %s\n", rec.LastBashCommand)
		}
	}
	fmt.Printf("\n%d record(s).\n", len(records))
}

// fmtMs renders a millisecond duration in a compact human form so the tail
// output stays scannable. 0 → "0", < 1s → "Nms", < 60s → "N.Ns", else "Nm Ns".
func fmtMs(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000.0
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	mins := int(sec) / 60
	rem := int(sec) % 60
	return fmt.Sprintf("%dm%ds", mins, rem)
}
