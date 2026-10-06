package state

import "fmt"

// Who reads a run on the hosted service (#2400). A run is team-visible unless
// the member who started it chose private; a private run is readable there by
// its creator alone. The hosted service enforces the rule; the core only
// carries the choice with the run, on every record it uploads.
//
// The local value is VisibilityPrivate or empty. Empty means team: the wire
// omits the field for a team run, which the service reads as `team`, so a
// run the autonomous scheduler starts never names a visibility at all.
const (
	VisibilityTeam    = "team"
	VisibilityPrivate = "private"
)

// ParseVisibility validates a requested visibility at a system boundary (an
// IPC request, a CLI flag). It returns VisibilityPrivate for "private" and
// "" for "team" or "", and an error for anything else.
func ParseVisibility(v string) (string, error) {
	switch v {
	case "", VisibilityTeam:
		return "", nil
	case VisibilityPrivate:
		return VisibilityPrivate, nil
	default:
		return "", fmt.Errorf("invalid visibility %q: want %q or %q", v, VisibilityTeam, VisibilityPrivate)
	}
}

// RaiseVisibility returns the visibility a run has once next is applied to
// current: private once either is private, never lowered. It mirrors the
// hosted service's rule that a private record raises a run and nothing
// lowers it.
func RaiseVisibility(current, next string) string {
	if current == VisibilityPrivate || next == VisibilityPrivate {
		return VisibilityPrivate
	}
	return ""
}
