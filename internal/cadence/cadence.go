// Package cadence answers the one question no other detector in this product
// asks: did a thing that is supposed to run on a schedule actually run?
//
// Every existing detector answers "did this run FAIL?". A workflow with zero
// runs has no failed run to report, and a stopped daemon logs no error, so the
// entire class of "it stopped" is invisible — including to `doctor`, whose arms
// are all residue detectors (leaked worktrees, stale stashes, orphaned
// processes: things that exist and should not).
//
// That gap is not academic. Four automations were found dark by hand on
// 2026-08-27, one of them the autonomous loop itself — stopped 22 days, while
// every surface reported the workspace healthy. Unattended operation is this
// product's value proposition, which makes "nothing notices when the unattended
// thing stops" the most expensive silence it can have.
package cadence

import (
	"fmt"
	"time"
)

// Status is the verdict for one automation.
type Status string

const (
	// StatusFresh — evidence exists and is inside the staleness threshold.
	StatusFresh Status = "fresh"
	// StatusStale — it ran once and then stopped.
	StatusStale Status = "stale"
	// StatusNeverRan — no evidence has EVER existed.
	//
	// Deliberately distinct from StatusStale. They have different causes and
	// different fixes: "never ran" is usually a schedule that was never valid
	// (a cron on a branch GitHub does not schedule from, a workflow whose
	// trigger never matched), while "ran and stopped" is usually a process that
	// died or a credential that expired. Collapsing both into one "stale"
	// verdict is a worse signal than none, because it sends the operator to
	// look at the wrong half.
	StatusNeverRan Status = "never_ran"
	// StatusUnknown — the probe could not determine freshness. NEVER reported
	// as healthy: an unreachable API is not evidence that a cron fired.
	StatusUnknown Status = "unknown"
)

// EvidenceKind names where an automation's freshness evidence lives, so the
// registry stays declarative and the probes stay in the layer that has the
// clients to run them.
type EvidenceKind string

const (
	// EvidenceAutonomousState — the daemon's own state file (lastScanAt).
	EvidenceAutonomousState EvidenceKind = "autonomous_state"
	// EvidenceWorkflowRun — the newest run of a named GitHub Actions workflow.
	EvidenceWorkflowRun EvidenceKind = "workflow_run"
)

// Automation is one scheduled thing whose silence should be noticed.
type Automation struct {
	// ID is the stable identifier findings are keyed on. Assert on this, never
	// on a substring of a message.
	ID string
	// Description is what the operator loses while this is dark.
	Description string
	// Interval is how often it is EXPECTED to produce evidence.
	Interval time.Duration
	// Kind selects the probe.
	Kind EvidenceKind
	// Repo is "owner/name" for EvidenceWorkflowRun. Empty means the repo the
	// check is running in.
	Repo string
	// Workflow is the workflow filename for EvidenceWorkflowRun.
	Workflow string
	// TriggerEvent is the GitHub event whose runs count as evidence the
	// SCHEDULE fired ("schedule" for a cron). Empty means any run counts.
	//
	// This is not a detail. A workflow whose cron is broken but which someone
	// dispatched by hand has runs, so a check counting any run reads it as
	// healthy — the exact blindness this package exists to remove.
	// `org-security-audit.yml` is that shape today: every run to date is
	// workflow_dispatch or pull_request, and its weekly cron has never fired.
	TriggerEvent string
	// Remedy is the operator's next action, named concretely.
	Remedy string
}

// Evidence is a probe's answer about one automation.
type Evidence struct {
	// Newest is the most recent time this automation produced evidence.
	Newest time.Time
	// EverRan distinguishes "no evidence yet" from "evidence, but old". A probe
	// that cannot tell must set Err rather than guessing.
	EverRan bool
	// Err means the probe could not determine freshness — reported as
	// StatusUnknown, never as fresh.
	Err error
}

// DefaultStaleMultiple is how many intervals may elapse before an automation is
// reported stale.
//
// Three, deliberately. At 1x a single skipped cron alarms, and an alarm that
// fires on normal jitter is muted long before it catches a real stop — which is
// the failure mode this whole package exists to avoid reproducing. At 3x a
// daily job is reported after three days and a continuous loop within minutes,
// while the 22-day stall that motivated this would have been caught on day one.
const DefaultStaleMultiple = 3

// Verdict is one automation's evaluated freshness.
type Verdict struct {
	Automation Automation
	Status     Status
	Newest     time.Time
	Age        time.Duration
	Detail     string
}

// Stale reports whether this verdict warrants an operator finding. Unknown
// counts: an unverifiable automation is not a healthy one.
func (v Verdict) Stale() bool {
	return v.Status == StatusStale || v.Status == StatusNeverRan || v.Status == StatusUnknown
}

// Evaluate turns one probe result into a verdict.
//
// multiple <= 0 falls back to DefaultStaleMultiple, so a caller that forgets to
// configure it gets the conservative default rather than a threshold of zero
// that reports everything stale.
func Evaluate(a Automation, e Evidence, now time.Time, multiple float64) Verdict {
	if multiple <= 0 {
		multiple = DefaultStaleMultiple
	}
	v := Verdict{Automation: a, Newest: e.Newest}

	switch {
	case e.Err != nil:
		v.Status = StatusUnknown
		v.Detail = fmt.Sprintf("could not determine freshness: %v", e.Err)
	case !e.EverRan:
		v.Status = StatusNeverRan
		v.Detail = "has never produced evidence of a run"
	default:
		v.Age = now.Sub(e.Newest)
		threshold := time.Duration(float64(a.Interval) * multiple)
		if v.Age > threshold {
			v.Status = StatusStale
			v.Detail = fmt.Sprintf("last ran %s ago; expected every %s",
				roundAge(v.Age), roundAge(a.Interval))
		} else {
			v.Status = StatusFresh
			v.Detail = fmt.Sprintf("last ran %s ago", roundAge(v.Age))
		}
	}
	return v
}

// roundAge renders a duration at a granularity an operator reads, not at
// nanosecond precision.
func roundAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
