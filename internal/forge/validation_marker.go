package forge

import (
	"fmt"
	"strings"
)

// Post-merge validation marker (#2574). The post-merge hook comments it on a
// merged issue once the merge commit verifies green on the base branch — the
// hook's equivalent of `scripts/post-merge-check.sh` exit 0. A closed program
// issue releases its dependents only when it carries the marker, so closure
// alone ("the PR merged") is not taken as "main was green afterwards".
//
// The marker counts only in a comment whose author may vouch for the issue
// (trust.Gate.TrustedComments): anyone can paste the text, and a pasted marker
// must not release dependents.
const (
	// ValidationMarkerPrefix opens the marker. Detection matches on it.
	ValidationMarkerPrefix = "<!-- nightgauge:validated "
)

// ValidationMarker renders the marker line (without trailing newline):
//
//	<!-- nightgauge:validated sha=<merge-sha> -->
func ValidationMarker(sha string) string {
	return fmt.Sprintf("%ssha=%s -->", ValidationMarkerPrefix, markerValue(sha))
}

// ValidationMarkerComment is the whole comment the post-merge hook posts: the
// marker and one sentence a reader can act on.
func ValidationMarkerComment(baseRef, sha string) string {
	base := strings.TrimSpace(baseRef)
	if base == "" {
		base = "the base branch"
	}
	return fmt.Sprintf("%s\nPost-merge validation: `%s` is green at merge commit `%s`.",
		ValidationMarker(sha), base, strings.TrimSpace(sha))
}

// HasValidationMarker reports whether body carries a validation marker.
func HasValidationMarker(body string) bool {
	return strings.Contains(body, ValidationMarkerPrefix)
}
