package main

import (
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// A refusal is only useful if it says what to do next. "The serve lease is
// held" names a mechanism the operator has never heard of; the advice has to
// name the holder and both ways out, or the guard just converts one confusing
// failure (two schedulers) into another (a command that will not start).
func TestLeaseAdviceNamesBothWaysOut(t *testing.T) {
	for name, advice := range map[string]string{
		"autonomous": autonomousLeaseAdvice(),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(advice, "--attach") {
				t.Errorf("%s advice does not mention --attach:\n%s", name, advice)
			}
			if !strings.Contains(advice, "autonomous status") {
				t.Errorf("%s advice does not say how to see the holder:\n%s", name, advice)
			}
			if !strings.Contains(advice, "stop that process") {
				t.Errorf("%s advice does not cover the wedged-holder case:\n%s", name, advice)
			}
		})
	}
}

// The VS Code extension starts a daemon on activation, so an operator running
// `autonomous run` in a terminal hits this with an editor open behind them and
// no reason to connect the two.
func TestAutonomousAdviceNamesTheUsualHolder(t *testing.T) {
	if !strings.Contains(autonomousLeaseAdvice(), "VS Code") {
		t.Errorf("the autonomous advice does not name the usual holder:\n%s", autonomousLeaseAdvice())
	}
}

func TestDescribeSchedulerLeaseFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := describeSchedulerLease(t.TempDir())
	if !strings.Contains(got, "free") {
		t.Errorf("describeSchedulerLease = %q, want it to state the lease is free — an operator "+
			"whose queue stopped needs to tell 'something else is running it' from 'nothing is'", got)
	}
}

func TestDescribeSchedulerLeaseHeldAndWedged(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	now := time.Now()

	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)

	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID: 9090, StartedAt: now.Add(-time.Hour), LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	if got := describeSchedulerLease(root); !strings.Contains(got, "9090") {
		t.Errorf("describeSchedulerLease = %q, want the holding PID", got)
	}

	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID:             9090,
		StartedAt:       now.Add(-time.Hour),
		LastHeartbeatAt: now.Add(-runstate.ServeLeaseStaleAfter - time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	got := describeSchedulerLease(root)
	if !strings.Contains(got, "WEDGED") {
		t.Errorf("describeSchedulerLease = %q, want the wedged state called out", got)
	}
}

// A holder that exits between the refusal and the report leaves nothing to
// describe. Printing an empty line there leaves an operator staring at a blank
// where the explanation should be.
func TestSchedulerLeaseHolderLineHandlesAReleasedLease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := schedulerLeaseHolderLine(t.TempDir())
	if strings.TrimSpace(got) == "" {
		t.Fatal("schedulerLeaseHolderLine returned an empty string for a free lease")
	}
	if !strings.Contains(got, "released") {
		t.Errorf("schedulerLeaseHolderLine = %q, want it to explain the lease is no longer held", got)
	}
}

// `autonomous run` must expose --attach, or the advice above points at a flag
// that does not exist.
func TestAttachFlagExistsOnAutonomousRun(t *testing.T) {
	if f := autonomousRunCmd().Flags().Lookup("attach"); f == nil {
		t.Error("`nightgauge autonomous run` has no --attach flag, but the refusal advice names it")
	}
}

// `serve` must NOT refuse over a held lease.
//
// It is the stdio IPC server the extension talks to, one per extension host, so
// two VS Code windows on one workspace folder legitimately run two. Failing the
// second would take that window's entire Nightgauge integration down in order to
// prevent a duplicate scheduler — trading a real outage for a hypothetical one.
// A flag here would invite exactly that, so there is none.
func TestServeHasNoAttachFlagBecauseItNeverRefuses(t *testing.T) {
	if f := serveCmd().Flags().Lookup("attach"); f != nil {
		t.Error("`nightgauge serve` grew an --attach flag; a second window's IPC " +
			"server must start unconditionally and simply not attach a scheduler")
	}
}
