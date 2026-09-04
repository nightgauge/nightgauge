package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// lifecycleEventLabels are the event types that describe a change to the
// bundle's CONTENT, and their display names.
//
// Reads and recalls are deliberately excluded: log.md is a change history, not
// a usage log. A bundle whose log is dominated by "someone searched" tells a
// reader nothing about how the knowledge got there.
var lifecycleEventLabels = map[EventType]string{
	EventScaffold: "Scaffolded",
	EventWrite:    "Written",
	EventGraduate: "Graduated",
	EventPrune:    "Pruned",
}

// logMarkdownMaxDays caps how far back the rendered log goes, so a
// long-running workspace does not grow an unbounded file that nobody reads.
const logMarkdownMaxDays = 90

// WriteLogMarkdown renders the Open Knowledge Format change history at path,
// derived from the knowledge telemetry event stream.
//
// `log.md` is one of the two files every OKF consumer reads first. It is a
// RENDERING of data that already lives in a queryable JSONL and is read by two
// other commands, so it is deliberately non-blocking: a missing or unreadable
// event stream returns an error the index generator ignores rather than
// failing the index the pipeline actually depends on.
//
// Format: newest date first, one `## YYYY-MM-DD` heading per day, one
// `* **<Event>**: [path](/bundle-path)` bullet per lifecycle event.
func WriteLogMarkdown(workspaceRoot, path string) error {
	events, err := readLifecycleEvents(Path(workspaceRoot))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderLogMarkdown(events)), 0o644)
}

// logEntry is one rendered bullet.
type logEntry struct {
	Date  string
	Label string
	Path  string
}

func readLifecycleEvents(eventsPath string) ([]logEntry, error) {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cutoff := time.Now().UTC().AddDate(0, 0, -logMarkdownMaxDays)

	var entries []logEntry
	seen := map[logEntry]bool{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		label, ok := lifecycleEventLabels[ev.Type]
		if !ok || ev.Path == "" {
			continue
		}
		ts, tsErr := time.Parse(time.RFC3339, ev.Timestamp)
		if tsErr != nil || ts.Before(cutoff) {
			continue
		}
		entry := logEntry{
			Date:  ts.UTC().Format("2006-01-02"),
			Label: label,
			Path:  bundlePath(ev.Path),
		}
		// One line per (day, event, path). A stage that rewrites the same
		// entry twice in a day is one change to a reader.
		if seen[entry] {
			continue
		}
		seen[entry] = true
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// bundlePath converts a recorded path into the bundle-absolute form an OKF
// consumer resolves: rooted at the knowledge root, not at the workspace.
func bundlePath(p string) string {
	p = filepath.ToSlash(p)
	const marker = ".nightgauge/knowledge/"
	if i := strings.Index(p, marker); i >= 0 {
		return "/" + p[i+len(marker):]
	}
	return "/" + strings.TrimPrefix(p, "/")
}

func renderLogMarkdown(entries []logEntry) string {
	byDate := map[string][]logEntry{}
	for _, e := range entries {
		byDate[e.Date] = append(byDate[e.Date], e)
	}

	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	// Newest first. ISO dates sort lexicographically, so reversing the
	// ascending sort is correct and needs no time parsing.
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))

	var sb strings.Builder
	sb.WriteString("---\ntype: log\ntitle: Change Log\nstatus: stable\ngenerated:\n  by: process:knowledge-index\n---\n\n")
	sb.WriteString("# Change Log\n\n")

	if len(dates) == 0 {
		sb.WriteString("No recorded changes.\n")
		return sb.String()
	}

	for _, d := range dates {
		day := byDate[d]
		sort.Slice(day, func(i, j int) bool {
			if day[i].Path != day[j].Path {
				return day[i].Path < day[j].Path
			}
			return day[i].Label < day[j].Label
		})
		fmt.Fprintf(&sb, "## %s\n\n", d)
		for _, e := range day {
			fmt.Fprintf(&sb, "* **%s**: [%s](%s)\n", e.Label, strings.TrimPrefix(e.Path, "/"), e.Path)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
