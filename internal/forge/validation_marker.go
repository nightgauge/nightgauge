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

	// LegacyValidationMarker is the marker the retired program-office bypass
	// script posted (#2575). Detection accepts it so items the bypass
	// validated keep releasing their dependents; nothing writes it. It has no
	// closing "-->" so a variant with a trailing payload matches too.
	LegacyValidationMarker = "<!-- opencode-program:validated"
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

// HasValidationMarker reports whether body carries a validation marker: the
// current one, or the legacy bypass marker. Callers apply the trust filter.
func HasValidationMarker(body string) bool {
	return strings.Contains(body, ValidationMarkerPrefix) ||
		strings.Contains(body, LegacyValidationMarker)
}

// ValidationMarkerSHA returns the sha= value of the first current-format
// marker in body (#2629). A legacy marker is present but names no SHA, so it
// returns ("", true); a body with no marker returns ("", false).
func ValidationMarkerSHA(body string) (string, bool) {
	if i := strings.Index(body, ValidationMarkerPrefix); i >= 0 {
		rest := strings.TrimPrefix(body[i+len(ValidationMarkerPrefix):], "sha=")
		if j := strings.Index(rest, "-->"); j >= 0 {
			rest = rest[:j]
		}
		return strings.TrimSpace(rest), true
	}
	return "", strings.Contains(body, LegacyValidationMarker)
}

// HasValidationMarkerFor reports whether body carries a current-format marker
// naming merge commit sha (#2629). A marker for another SHA, or a legacy
// marker, does not match.
func HasValidationMarkerFor(body, sha string) bool {
	got, ok := ValidationMarkerSHA(body)
	return ok && got != "" && strings.EqualFold(got, markerValue(sha))
}
