// Package queuewait records the time a pr-merge stage spends waiting on the
// merge queue after its PR was enqueued, so the stage's wall-clock budget can
// leave that time out (#2770).
//
// A budgeted pr-merge stage runs `nightgauge pr merge --issue N`, which, on a
// base branch with a merge queue, enqueues the PR and polls it until the queue
// merges it. That wait is the queue's CI run on the merge-group commit, not
// the stage's work, and counting it killed a stage 66 s before its PR merged
// (#2687). The writer opens an interval when the PR reaches the queue and
// closes it when the merge call returns; the wall-clock enforcers subtract the
// recorded time from elapsed time before deciding the budget is spent.
//
// Every interval carries its own writer's identity, a pid and that process's
// start time. An interval left open by a writer that died, or whose pid now
// belongs to another process, adds nothing: excluding time on behalf of a
// process that is no longer waiting would let a stuck stage outlive its
// budget. The total is clamped to the window asked about, so excluded time
// never exceeds elapsed time.
package queuewait

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/procstart"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// maxIntervals bounds the record. Retries of one issue append intervals; only
// the recent ones can fall inside a live stage's window.
const maxIntervals = 32

// Writer identifies the process that opened an interval.
type Writer struct {
	PID int `json:"pid"`
	// Start is the process start time in Unix microseconds, 0 when the
	// platform cannot report it (liveness alone is then checked).
	Start int64 `json:"pid_start"`
}

// Interval is one span of queue wait. End is nil while the wait is open.
type Interval struct {
	Writer
	Begin time.Time  `json:"begin"`
	End   *time.Time `json:"end,omitempty"`
}

// Record is the queue-wait record of one issue.
type Record struct {
	Issue     int        `json:"issue"`
	Intervals []Interval `json:"intervals"`
}

// Probe reports whether the process with pid is alive and, if so, its start
// time in the units of Writer.Start (0 when unknown).
type Probe func(pid int) (start int64, alive bool)

// FileName is the record's name in the pipeline state directory.
func FileName(issue int) string { return fmt.Sprintf("queue-wait-%d.json", issue) }

// Self is the calling process as a Writer.
func Self() Writer {
	pid := os.Getpid()
	start, _ := procstart.Start(pid)
	return Writer{PID: pid, Start: start}
}

// SystemProbe is the Probe over the running system.
func SystemProbe(pid int) (int64, bool) {
	if !runstate.ProcessAlive(pid) {
		return 0, false
	}
	start, err := procstart.Start(pid)
	if err != nil {
		return 0, true
	}
	return start, true
}

// Load reads the record of issue for the repository root is in. A missing
// record is an empty one.
func Load(root string, issue int) (Record, error) {
	path, err := layoutPath(root, issue)
	if err != nil {
		return Record{Issue: issue}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{Issue: issue}, nil
	}
	if err != nil {
		return Record{Issue: issue}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{Issue: issue}, fmt.Errorf("decode %s: %w", path, err)
	}
	rec.Issue = issue
	return rec, nil
}

func save(root string, rec Record) error {
	if n := len(rec.Intervals); n > maxIntervals {
		rec.Intervals = rec.Intervals[n-maxIntervals:]
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	_, err = layout.WriteClassFile(root, layout.ClassPipeline, FileName(rec.Issue), bytes.NewReader(append(data, '\n')))
	return err
}

// Begin opens an interval for w at now. It is a no-op while w already has an
// open interval.
func Begin(root string, issue int, w Writer, now time.Time) error {
	rec, err := Load(root, issue)
	if err != nil {
		return err
	}
	for _, iv := range rec.Intervals {
		if iv.End == nil && iv.Writer == w {
			return nil
		}
	}
	rec.Intervals = append(rec.Intervals, Interval{Writer: w, Begin: now.UTC()})
	return save(root, rec)
}

// End closes w's open interval at now. Another writer's interval is never
// touched, and a writer with no open interval changes nothing.
func End(root string, issue int, w Writer, now time.Time) error {
	rec, err := Load(root, issue)
	if err != nil {
		return err
	}
	for i := len(rec.Intervals) - 1; i >= 0; i-- {
		iv := &rec.Intervals[i]
		if iv.End != nil || iv.Writer != w {
			continue
		}
		end := now.UTC()
		if end.Before(iv.Begin) {
			end = iv.Begin
		}
		iv.End = &end
		return save(root, rec)
	}
	return nil
}

// Excluded is the queue wait the record holds inside [since, now]. A closed
// interval counts as recorded. An open interval counts up to now only while
// its writer is alive and its start time still matches (a recycled pid is a
// different process); otherwise it adds nothing. Overlapping intervals count
// once, and the total never exceeds now - since.
func (r Record) Excluded(since, now time.Time, probe Probe) time.Duration {
	window := now.Sub(since)
	if window <= 0 {
		return 0
	}
	type span struct{ b, e time.Time }
	spans := make([]span, 0, len(r.Intervals))
	for _, iv := range r.Intervals {
		var end time.Time
		if iv.End != nil {
			end = *iv.End
		} else {
			start, alive := probe(iv.PID)
			if !alive || (iv.Start != 0 && start != iv.Start) {
				continue
			}
			end = now
		}
		b, e := iv.Begin, end
		if b.Before(since) {
			b = since
		}
		if e.After(now) {
			e = now
		}
		if e.After(b) {
			spans = append(spans, span{b, e})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].b.Before(spans[j].b) })
	var total time.Duration
	var cur *span
	for i := range spans {
		s := spans[i]
		if cur != nil && !s.b.After(cur.e) {
			if s.e.After(cur.e) {
				cur.e = s.e
			}
			continue
		}
		if cur != nil {
			total += cur.e.Sub(cur.b)
		}
		cur = &s
	}
	if cur != nil {
		total += cur.e.Sub(cur.b)
	}
	if total > window {
		total = window
	}
	return total
}

// ExcludedFor is the queue wait recorded for issue inside [since, now], read
// with the system probe. A record that cannot be read excludes nothing, which
// is the budget as it was before the record existed.
func ExcludedFor(root string, issue int, since, now time.Time) time.Duration {
	if issue <= 0 {
		return 0
	}
	rec, err := Load(root, issue)
	if err != nil {
		return 0
	}
	return rec.Excluded(since, now, SystemProbe)
}

func layoutPath(root string, issue int) (string, error) {
	return layout.ClassFilePath(root, layout.ClassPipeline, FileName(issue))
}

// Track is the writer side for one merge call of this process: begin opens
// its interval (call it when the PR reaches the queue) and end closes it
// (call it when the merge call returns; a no-op when begin never succeeded).
// A record that cannot be written is reported through warn, and the merge
// goes on: the budget then counts the wait, as it did before the record.
func Track(root string, issue int, warn func(error)) (begin, end func()) {
	self := Self()
	opened := false
	begin = func() {
		if err := Begin(root, issue, self, time.Now()); err != nil {
			warn(fmt.Errorf("record merge-queue wait for issue #%d: %w", issue, err))
			return
		}
		opened = true
	}
	end = func() {
		if !opened {
			return
		}
		if err := End(root, issue, self, time.Now()); err != nil {
			warn(fmt.Errorf("close merge-queue wait for issue #%d: %w", issue, err))
		}
	}
	return begin, end
}
