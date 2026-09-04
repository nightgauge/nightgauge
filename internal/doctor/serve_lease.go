package doctor

// The `serve_lease` arm (#1349).
//
// The lease makes "one scheduler per workspace" enforceable, which turns a
// wedged daemon into a new failure mode: it still holds the lock, so nothing
// else can start, and the symptom is a workspace where every `serve` and
// `autonomous run` is refused for a reason the operator has no way to see. The
// refusal message names the holder, but only for whoever tried to start
// something — an operator whose queue simply stopped moving never sees it.
//
// This arm reports the same fact from the other direction: what holds the
// lease for THIS workspace, and whether it still looks alive.
//
// Deliberately distinct from `orphaned_processes` (#341), which reads the same
// claim directory. That arm asks a machine-wide question — which processes are
// unaccounted for — and expires a claim after 24 hours, because its cost of
// being wrong is naming a healthy daemon as an orphan. This one asks a
// workspace-scoped question with a much tighter clock (two missed heartbeats),
// because its cost of being wrong is an operator waiting on a daemon that is
// never coming back.

import (
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// checkServeLease reports the holder of this workspace's scheduler lease.
//
// A FREE lease is a clean result, not an absence of information: it is the
// answer to "why is nothing running?" for a workspace whose operator expected
// a daemon. A HELD lease with a fresh heartbeat is equally clean. Only a
// wedged holder is a finding, because only a wedged holder blocks work while
// looking, from the outside, exactly like a healthy one.
func checkServeLease(workspaceRoot string, now time.Time) (CheckItem, string) {
	if workspaceRoot == "" {
		return CheckItem{OK: true, Detail: "serve lease not checked (no workspace root)"}, ""
	}

	holder, held := runstate.InspectServeLease(workspaceRoot)
	if !held {
		return CheckItem{OK: true, Detail: "free — no daemon is serving this workspace"}, ""
	}
	if !holder.Known {
		// The lock is authoritative about the lease being held; the sidecar is
		// the only source for who holds it. An unreadable record downgrades
		// what we can say, and must not be reported as a malfunction of the
		// daemon it failed to describe.
		return CheckItem{
			OK:     true,
			Detail: "held by another process (its claim record could not be read)",
		}, ""
	}

	detail := fmt.Sprintf("held by pid %d since %s",
		holder.PID, holder.StartedAt.Format(time.RFC3339))
	if !holder.Stale {
		return CheckItem{OK: true, Detail: detail}, ""
	}

	msg := fmt.Sprintf(
		"serve-lease-wedged: pid %d holds this workspace's scheduler lease but has not "+
			"heartbeat since %s (%s ago, past the %s limit). It is still running, so the lease "+
			"cannot be reclaimed and every `nightgauge serve` and `nightgauge autonomous run` "+
			"here is refused — which presents as a workspace where nothing starts. Stop pid %d; "+
			"the lease is released the moment it exits, however it exits",
		holder.PID,
		holder.LastHeartbeatAt.Format(time.RFC3339),
		now.Sub(holder.LastHeartbeatAt).Round(time.Minute),
		runstate.ServeLeaseStaleAfter,
		holder.PID)
	return CheckItem{OK: false, Detail: detail + " — WEDGED", Error: msg}, msg
}
