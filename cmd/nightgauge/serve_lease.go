package main

// The operator-facing half of the scheduler lease (#1349).
//
// A refusal is only useful if it tells the operator what to do next. "The
// serve lease is held" names a mechanism they have never heard of and leaves
// them guessing; naming the holding process, its workspace, and the two ways
// out (attach, or stop the holder) turns the same refusal into an instruction.
//
// The message is built here rather than inside internal/runstate because the
// advice is CLI-shaped — it names flags and commands — and a runstate package
// that knows about cobra flags would be the wrong dependency.

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// schedulerLeaseHolderLine describes the current holder for a report.
//
// Returns a definite statement about not knowing when the lease is free by the
// time we look. That happens legitimately — the holder exited between the
// refusal and this call — and printing an empty line there would leave an
// operator staring at a blank where the explanation should be.
func schedulerLeaseHolderLine(workspaceRoot string) string {
	holder, held := runstate.InspectServeLease(workspaceRoot)
	if !held {
		return "the lease was released while this command was reporting on it"
	}
	return holder.Describe()
}

// autonomousLeaseAdvice is what to do about a refused `autonomous run`.
//
// It names `serve` explicitly because that is overwhelmingly the holder: the
// VS Code extension starts a daemon on activation, so an operator running
// `autonomous run` in a terminal hits this with an editor window open behind
// them and no reason to connect the two.
func autonomousLeaseAdvice() string {
	return leaseAdvice("nightgauge autonomous run --attach") +
		"\nThe holder is usually the daemon the VS Code extension starts on activation."
}

func leaseAdvice(attachCmd string) string {
	return strings.Join([]string{
		"Two schedulers against one workspace dispatch the same board: each one's",
		"concurrency ceiling is per-process, so neither can see the other's slots and",
		"both start the same issues.",
		"",
		"  " + attachCmd + "   — succeed quietly; the running scheduler keeps the work",
		"  nightgauge autonomous status      — see who holds the lease",
		"",
		"If the holder is wedged, stop that process; the lease is released the moment",
		"it exits, however it exits.",
	}, "\n")
}

// `nightgauge serve` has no equivalent, deliberately. It is also the stdio IPC
// server the extension talks to — one per extension host — so two VS Code
// windows on one workspace folder legitimately run two of them. Failing the
// second would take that window's whole integration down to prevent a duplicate
// scheduler; instead it serves IPC without attaching one, and logs why.

// describeSchedulerLease renders the lease section of `autonomous status`.
//
// Reported unconditionally, including when the lease is FREE. "No scheduler
// holds this workspace" is the answer to a real question — an operator whose
// queue is not moving needs to tell "something else is running it" apart from
// "nothing is running it", and those two states were previously
// indistinguishable from this command's output.
func describeSchedulerLease(workspaceRoot string) string {
	holder, held := runstate.InspectServeLease(workspaceRoot)
	if !held {
		return "Scheduler lease: free (no daemon is serving this workspace)"
	}
	if !holder.Known {
		return "Scheduler lease: held by another process (its claim record could not be read)"
	}
	line := fmt.Sprintf("Scheduler lease: held by pid %d (%s)", holder.PID, holder.WorkspaceRoot)
	if holder.Stale {
		line += fmt.Sprintf("\n  WEDGED: no heartbeat since %s — stop that process rather than waiting for it",
			holder.LastHeartbeatAt.Format("2006-01-02 15:04:05 MST"))
	}
	return line
}
