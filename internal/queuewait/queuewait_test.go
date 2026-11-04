package queuewait

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/pidtest"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

var t0 = time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func ptr(t time.Time) *time.Time { return &t }

// probeOf answers alive with the given start time for the listed pids, dead
// for every other pid.
func probeOf(live map[int]int64) Probe {
	return func(pid int) (int64, bool) {
		s, ok := live[pid]
		return s, ok
	}
}

func TestExcluded_ClosedIntervalCountsInsideWindow(t *testing.T) {
	rec := Record{Intervals: []Interval{{Writer: Writer{PID: 1, Start: 10}, Begin: at(100), End: ptr(at(400))}}}
	if got := rec.Excluded(at(0), at(500), probeOf(nil)); got != 300*time.Second {
		t.Fatalf("excluded = %s, want 5m0s", got)
	}
}

// TestExcluded_DeadWritersOpenIntervalAddsNothing is AC 2's first side: the
// writer was killed with its interval open, so nothing is excluded for it.
func TestExcluded_DeadWritersOpenIntervalAddsNothing(t *testing.T) {
	rec := Record{Intervals: []Interval{{Writer: Writer{PID: 42, Start: 7}, Begin: at(100)}}}
	if got := rec.Excluded(at(0), at(500), probeOf(nil)); got != 0 {
		t.Fatalf("excluded = %s for a dead writer's open interval, want 0", got)
	}
}

func TestExcluded_LiveWritersOpenIntervalCountsToNow(t *testing.T) {
	rec := Record{Intervals: []Interval{{Writer: Writer{PID: 42, Start: 7}, Begin: at(100)}}}
	if got := rec.Excluded(at(0), at(500), probeOf(map[int]int64{42: 7})); got != 400*time.Second {
		t.Fatalf("excluded = %s, want 6m40s", got)
	}
}

// TestExcluded_RecycledPidAddsNothing: the pid is alive, but it is another
// process (its start time differs from the writer's).
func TestExcluded_RecycledPidAddsNothing(t *testing.T) {
	rec := Record{Intervals: []Interval{{Writer: Writer{PID: 42, Start: 7}, Begin: at(100)}}}
	if got := rec.Excluded(at(0), at(500), probeOf(map[int]int64{42: 99})); got != 0 {
		t.Fatalf("excluded = %s for a recycled pid, want 0", got)
	}
}

// TestExcluded_NewWriterDoesNotReviveDeadWritersInterval is the reviewed
// defect: one record-level pid let a new writer revive a killed writer's
// open interval, excluding 500 s against 400 s elapsed.
func TestExcluded_NewWriterDoesNotReviveDeadWritersInterval(t *testing.T) {
	rec := Record{Intervals: []Interval{
		{Writer: Writer{PID: 1, Start: 1}, Begin: at(0)},   // killed writer
		{Writer: Writer{PID: 2, Start: 2}, Begin: at(300)}, // live writer
	}}
	got := rec.Excluded(at(0), at(400), probeOf(map[int]int64{2: 2}))
	if got != 100*time.Second {
		t.Fatalf("excluded = %s, want only the live writer's 1m40s", got)
	}
}

// TestExcluded_NeverExceedsElapsed is AC 2's second side: overlapping
// intervals count once, time before the window is clipped, and the total is
// bounded by the window.
func TestExcluded_NeverExceedsElapsed(t *testing.T) {
	rec := Record{Intervals: []Interval{
		{Writer: Writer{PID: 1}, Begin: at(-1000), End: ptr(at(250))}, // starts before the window
		{Writer: Writer{PID: 2}, Begin: at(100), End: ptr(at(300))},   // overlaps the first
		{Writer: Writer{PID: 3}, Begin: at(200), End: ptr(at(900))},   // runs past now
	}}
	since, now := at(0), at(400)
	got := rec.Excluded(since, now, probeOf(nil))
	if got != 400*time.Second {
		t.Fatalf("excluded = %s, want the whole 6m40s window", got)
	}
	if got > now.Sub(since) {
		t.Fatalf("excluded %s exceeds elapsed %s", got, now.Sub(since))
	}
	if got := rec.Excluded(at(500), at(400), probeOf(nil)); got != 0 {
		t.Fatalf("excluded = %s for an empty window, want 0", got)
	}
}

func TestExcluded_DisjointIntervalsSum(t *testing.T) {
	rec := Record{Intervals: []Interval{
		{Writer: Writer{PID: 1}, Begin: at(10), End: ptr(at(20))},
		{Writer: Writer{PID: 1}, Begin: at(50), End: ptr(at(80))},
	}}
	if got := rec.Excluded(at(0), at(100), probeOf(nil)); got != 40*time.Second {
		t.Fatalf("excluded = %s, want 40s", got)
	}
}

func TestBeginEnd_RoundTripAndOwnIntervalOnly(t *testing.T) {
	root := layouttest.Repo(t)
	a, b := Writer{PID: 11, Start: 1}, Writer{PID: 12, Start: 2}
	if err := Begin(root, 7, a, at(10)); err != nil {
		t.Fatal(err)
	}
	if err := Begin(root, 7, a, at(20)); err != nil { // idempotent while open
		t.Fatal(err)
	}
	if err := Begin(root, 7, b, at(30)); err != nil {
		t.Fatal(err)
	}
	if err := End(root, 7, a, at(40)); err != nil {
		t.Fatal(err)
	}
	rec, err := Load(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Intervals) != 2 {
		t.Fatalf("intervals = %+v, want 2", rec.Intervals)
	}
	if ia := rec.Intervals[0]; ia.Writer != a || !ia.Begin.Equal(at(10)) || ia.End == nil || !ia.End.Equal(at(40)) {
		t.Errorf("a's interval = %+v, want [10,40]", ia)
	}
	if ib := rec.Intervals[1]; ib.Writer != b || ib.End != nil {
		t.Errorf("b's interval = %+v, want still open", ib)
	}
	if err := End(root, 8, a, at(50)); err != nil { // no record: no-op
		t.Fatal(err)
	}
	if rec, _ := Load(root, 8); len(rec.Intervals) != 0 {
		t.Errorf("End created intervals on an empty record: %+v", rec.Intervals)
	}
}

func TestExcludedFor_UnreadableRecordExcludesNothing(t *testing.T) {
	root := layouttest.Repo(t)
	if err := Begin(root, 9, Writer{PID: 1}, at(0)); err != nil {
		t.Fatal(err)
	}
	p, err := layoutPath(root, 9)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ExcludedFor(root, 9, at(0), at(100)); got != 0 {
		t.Fatalf("excluded = %s for an unreadable record, want 0", got)
	}
}

// TestSystemProbe_SelfMatchesAndReapedPidIsDead drives the real probe: this
// process is alive with the start time Self records, and a reaped child is
// dead.
func TestSystemProbe_SelfMatchesAndReapedPidIsDead(t *testing.T) {
	self := Self()
	start, alive := SystemProbe(self.PID)
	if !alive || start != self.Start {
		t.Fatalf("SystemProbe(self) = (%d, %v), want (%d, true)", start, alive, self.Start)
	}
	dead := pidtest.Reaped(t, runstate.ProcessAlive)
	if _, alive := SystemProbe(dead); alive {
		t.Fatalf("SystemProbe(reaped %d) = alive", dead)
	}
	// A child that is still running has a start time of its own.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if cs, alive := SystemProbe(cmd.Process.Pid); !alive || (self.Start != 0 && cs == self.Start) {
		t.Fatalf("SystemProbe(child) = (%d, %v), want alive with a distinct start time", cs, alive)
	}
}

// TestExcluded_ClipsTimeBeforeTheWindow: an interval that began before the
// stage was spawned (a prior attempt's wait) counts only from the spawn.
func TestExcluded_ClipsTimeBeforeTheWindow(t *testing.T) {
	rec := Record{Intervals: []Interval{{Writer: Writer{PID: 1}, Begin: at(-100), End: ptr(at(50))}}}
	if got := rec.Excluded(at(0), at(500), probeOf(nil)); got != 50*time.Second {
		t.Fatalf("excluded = %s, want only the 50s inside the window", got)
	}
}
