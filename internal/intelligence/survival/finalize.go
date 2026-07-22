package survival

import "time"

// DefaultWindowDays is the post-merge observation window (spike #4134 §1.2). A
// record stays pending until the window elapses; a revert/breakage observed at
// any point finalizes it negative immediately.
const DefaultWindowDays = 7

// Observation carries the deterministic detection results for one pending record
// at sweep time. It is produced by a Detector (live GitHub queries) and consumed
// by DecideVerdict (pure logic), keeping the state machine fully testable.
type Observation struct {
	// RevertFound is true when a `This reverts commit <mergeSHA>` commit exists
	// on the base branch. RevertSHA names that commit (for evidence).
	RevertFound bool
	RevertSHA   string

	// Broke is true when an ancestry-correlated main-CI failure is attributed to
	// the merge: a failing run on a DESCENDANT of the merge commit where the same
	// check was GREEN at the merge commit. Detail is a short evidence string.
	Broke       bool
	BrokeDetail string
}

// IsDue reports whether a pending record's observation window has elapsed and it
// is therefore worth spending detection API calls on. Records that are not yet
// due (or whose MergedAt is unparseable) are skipped by the sweep — except that
// a not-yet-due record is still finalized negative if detection already ran and
// found a revert/breakage (handled in DecideVerdict, which is the single source
// of truth). IsDue is purely the "should I bother observing?" gate.
func IsDue(r Record, now time.Time, windowDays int) bool {
	if r.Verdict.IsTerminal() {
		return false
	}
	merged, ok := r.mergedTime()
	if !ok {
		return false
	}
	if windowDays <= 0 {
		windowDays = DefaultWindowDays
	}
	return !now.Before(merged.Add(window(windowDays)))
}

// DecideVerdict is the pure survival state machine. Given a record, the current
// time, the window, and the detection Observation, it returns the (possibly
// updated) record and whether it transitioned to a terminal verdict.
//
// Precedence (spike #4134 §1.2–§1.4):
//  1. revert observed            → reverted  (negative, proven; acts immediately)
//  2. ancestry-CI failure        → broke     (negative, proven; acts immediately)
//  3. no negative evidence:
//     a. aged past 2×window       → unobserved (terminal, NO signal — never survived)
//     b. window elapsed (≤2×win)  → survived   (terminal, weak-positive)
//     c. window not yet elapsed   → stay pending
//
// Negative evidence is acted on regardless of window timing because a revert or
// an ancestry-correlated regression is proven ground truth. The asymmetric
// timing for the positive path is the bias-safety guard: code only seen clean
// long after the fact (low-traffic) ages out to unobserved, never survived.
func DecideVerdict(r Record, now time.Time, windowDays int, obs Observation) (Record, bool) {
	if r.Verdict.IsTerminal() {
		return r, false // idempotent: never re-finalize
	}
	if windowDays <= 0 {
		windowDays = DefaultWindowDays
	}

	// 1 & 2 — proven negatives finalize immediately.
	if obs.RevertFound {
		return r.finalize(Reverted, EvidenceRevertCommit, now), true
	}
	if obs.Broke {
		return r.finalize(Broke, EvidenceAncestryCI, now), true
	}

	// 3 — positive/no-signal path is gated on the window.
	merged, ok := r.mergedTime()
	if !ok {
		return r, false // cannot determine window → stay pending
	}
	w := window(windowDays)
	switch {
	case !now.Before(merged.Add(2 * w)):
		return r.finalize(Unobserved, EvidenceWindowUnobserved, now), true
	case !now.Before(merged.Add(w)):
		return r.finalize(Survived, EvidenceWindowClean, now), true
	default:
		return r, false // not yet due
	}
}

// finalize returns a copy of the record stamped with a terminal verdict,
// evidence, and observation time. RevertSHA/BrokeDetail are folded into evidence
// where present so the audit trail is self-describing.
func (r Record) finalize(v Verdict, evidence string, now time.Time) Record {
	r.Verdict = v
	r.Evidence = evidence
	r.ObservedAt = now.UTC().Format(time.RFC3339)
	return r
}

func window(days int) time.Duration { return time.Duration(days) * 24 * time.Hour }
