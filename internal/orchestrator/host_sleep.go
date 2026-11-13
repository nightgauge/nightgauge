package orchestrator

import (
	"fmt"
	"time"
)

// HostSleepThreshold is how much of a stage's wall-clock span must have been
// spent asleep before the stage's failure is booked as host_slept (#3136).
// Short naps (a lid close for a few seconds, a clock slew) stay with the kind
// the failure would have had anyway.
const HostSleepThreshold = 60 * time.Second

// HostSleptMarker opens the stage error the scheduler writes when it books a
// failure as host_slept. The canonical rule table matches it (rule
// `host-slept`), so the run record, the extension's halt decision and the
// failure comment all read the same kind from the same text.
const HostSleptMarker = "[host-slept]"

// HostSleptDuration reports how long the host was asleep between start and
// now, both taken with time.Now() so each carries a monotonic reading.
//
// Go's monotonic clock does not advance while the host sleeps: on darwin it is
// mach_absolute_time, on Linux CLOCK_MONOTONIC (golang/go#24595 records the
// decision not to switch to a sleep-inclusive clock). The wall clock does
// advance. So the wall delta minus the monotonic delta is the time spent
// asleep. Round(0) strips the monotonic reading, which makes Sub use the wall
// clock.
//
// A forward wall-clock step (a manual clock change) reads as sleep too. NTP
// slews rather than steps in normal operation, and the cost of a false reading
// is one retry. When either time lacks a monotonic reading both deltas are
// wall deltas and the result is 0. Never negative.
func HostSleptDuration(start, now time.Time) time.Duration {
	return sleptFrom(now.Round(0).Sub(start.Round(0)), now.Sub(start))
}

// sleptFrom is the wall span minus the awake span, never negative.
func sleptFrom(wall, awake time.Duration) time.Duration {
	if slept := wall - awake; slept > 0 {
		return slept
	}
	return 0
}

// measureHostSleep is the scheduler's sleep measurement. A variable so a test
// can stand in for a host that slept, which no unit test can make happen.
var measureHostSleep = HostSleptDuration

// ReclassifyForHostSleep returns TerminalKindHostSlept for a failure of a
// stage that lost more than HostSleepThreshold to host sleep, and kind
// unchanged otherwise (#3136, ADR-002).
//
// A gate timeout, a stall kill and a stage cap all count: each is a limit that
// expired on the wall clock while the host slept. Left alone are the kinds
// that are not the stage running out of time: no kind at all, an operator
// stop, a park, a human hold, a stop at a budget the operator set, and
// host_slept itself.
func ReclassifyForHostSleep(kind string, slept time.Duration) string {
	if slept <= HostSleepThreshold || !hostSleepReclassifiable(kind) {
		return kind
	}
	return TerminalKindHostSlept
}

// hostSleepReclassifiable reports whether a failure of this kind becomes
// host_slept when the host slept through the stage. An empty kind is
// reclassifiable: a failure the table cannot name is still a failure.
func hostSleepReclassifiable(kind string) bool {
	switch kind {
	case TerminalKindHostSlept, TerminalKindOperatorStop,
		TerminalKindBudgetCeiling, TerminalKindBudgetExceeded:
		return false
	}
	if TerminalKindParks(kind) || IsNonFailureTerminalKind(kind) || HoldForTerminalKind(kind) != HoldNone {
		return false
	}
	return true
}

// HostSleptStageError is the stage error recorded for a host_slept failure. It
// opens with HostSleptMarker, says how long the host slept and how to stop it
// happening again, and deliberately does not quote the original failure text:
// that text describes a limit that expired while the host slept, and quoting
// it would put a gate's "validation" wording on the card and the comment. The
// original text is logged by the caller.
func HostSleptStageError(stage string, wall, slept time.Duration) string {
	awake := wall - slept
	if awake < 0 {
		awake = 0
	}
	return fmt.Sprintf("%s the host slept for %s during %s (wall clock %s, awake %s). "+
		"This run was booked as infrastructure, not as a failure of this issue, and will retry. "+
		"%s", HostSleptMarker, formatSleep(slept), stage, formatSleep(wall), formatSleep(awake),
		HostSleptPrevention)
}

// HostSleptPrevention is the remedy every host_slept surface names.
const HostSleptPrevention = "To keep the host awake: the daemon holds a `caffeinate -i -s` assertion while " +
	"the autonomous scheduler runs (see `nightgauge autonomous status`), or disable sleep on AC power " +
	"with `sudo pmset -c sleep 0`."

// formatSleep renders a duration to the second ("15m27s").
func formatSleep(d time.Duration) string {
	return d.Round(time.Second).String()
}
