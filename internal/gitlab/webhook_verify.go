package gitlab

import (
	"crypto/subtle"
	"time"
)

// DefaultReplayWindow is the maximum age of a GitLab event payload timestamp
// before the receiver treats the event as stale. 5 minutes absorbs clock skew
// while still constraining replay attacks.
const DefaultReplayWindow = 5 * time.Minute

// VerifyToken performs constant-time comparison of the X-Gitlab-Token header
// value against the expected shared secret. Returns false for empty presented
// or expected strings (an empty token must never authorize a request).
func VerifyToken(presented, expected string) bool {
	p := []byte(presented)
	e := []byte(expected)
	if len(p) == 0 || len(e) == 0 {
		return false
	}
	if len(p) != len(e) {
		return false
	}
	return subtle.ConstantTimeCompare(p, e) == 1
}

// IsStale returns true when occurredAt is older than maxAge measured against
// the current clock, or when the timestamp drifts more than maxAge into the
// future (prevents an attacker from forwarding a future-dated event past the
// replay window).
func IsStale(occurredAt time.Time, maxAge time.Duration) bool {
	if occurredAt.IsZero() {
		return true
	}
	delta := time.Since(occurredAt)
	if delta < 0 {
		delta = -delta
	}
	return delta > maxAge
}
