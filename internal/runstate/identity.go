package runstate

import "regexp"

// Run-identity shape — ADR-017 Decision 1 (see docs/decisions/017-runtime-identity-keying.md).
//
// There is exactly ONE definition of the run-identity shape in the Go tree and
// it lives here, next to NewRunID which produces it. Decision 1 requires the
// wire validation (`run_id_invalid`), the snapshot filename composer, and the
// snapshot discovery regex to be the SAME expression rather than three copies:
// because the identity is always locally minted, there must be no id shape
// that can pass validation and fail discovery. Tests pin the composer and the
// discovery regex to each other against this constant.
//
// The value is NEVER decoded for a correctness rule (Decision 1): UUIDv7 is
// chosen so ids sort by mint time in logs and directory listings, not as a
// covert protocol. The one value in ADR-017 that IS decoded is Decision 9's
// pause-restore claim token, which is a separate UUIDv7 minted at claim time.

// IdentityPattern is the canonical lowercase UUIDv7 shape, UNANCHORED, so it
// can be embedded as a capture group in the filename patterns below. Version
// nibble 7, variant nibble in [89ab], lowercase hex only.
const IdentityPattern = `[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

// IdentityRegexp is IdentityPattern anchored — the single validator for "is
// this string a run identity?". Used at the IPC wire boundary (step 2/4) and
// by every disk reader that turns a string into a filename component.
var IdentityRegexp = regexp.MustCompile(`^` + IdentityPattern + `$`)

// IsIdentity reports whether s is a canonical lowercase UUIDv7 run identity.
// This is the check that must run BEFORE a value becomes a map key, a filename
// component, or a trace path: the identity is interpolated into
// runtime-{issue}-{runId}.json for Persist and os.Remove on a socket ADR-015
// documents as unauthenticated, so a value containing "/" or ".." would be an
// arbitrary-path write.
func IsIdentity(s string) bool {
	return IdentityRegexp.MatchString(s)
}

// ResumingArtifactRegexp matches the pause-restore claim artifact
// `resuming-{issue}-{runId}.{claimToken}.json` (ADR-017 Decision 9). BOTH
// trailing components are the same shared identity constant, because the claim
// token is itself a UUIDv7 minted at claim time; the reconciler parses the
// claim time out of the second one (C17 — never the file's mtime, which
// rename(2) does not update).
//
// Captures: 1 = issue number, 2 = run identity, 3 = claim token.
//
// INERT until ADR-017 step 8 creates the first such file. It is defined here,
// with the identity it shares, so the artifact name and the canonical snapshot
// name cannot drift apart — a claim artifact that the release path fails to
// recognise is F34's shape.
var ResumingArtifactRegexp = regexp.MustCompile(
	`^resuming-(\d+)-(` + IdentityPattern + `)\.(` + IdentityPattern + `)\.json$`)

// ParseResumingArtifactName returns the issue number, run identity and claim
// token encoded in a pause-restore claim artifact filename. ok is false for
// anything that is not a claim artifact — including a file whose token does not
// match the identity shape, which Decision 9 requires be left alone rather than
// guessed at.
func ParseResumingArtifactName(name string) (issue string, runID string, claimToken string, ok bool) {
	m := ResumingArtifactRegexp.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}
