package inbound

import (
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultReplayWindow is the maximum age of a request timestamp before
// the receiver rejects the request as stale. Mattermost outgoing webhooks
// fire near-instantly, so a 5-minute window absorbs clock skew while
// still constraining replay attacks.
const DefaultReplayWindow = 5 * time.Minute

// verifyToken returns true iff presented and expected are byte-equal,
// using crypto/subtle.ConstantTimeCompare so token verification does
// not leak timing on per-byte mismatches. ConstantTimeCompare only
// protects same-length inputs — a length-mismatch must be rejected
// before the constant-time compare is called.
func verifyToken(presented, expected []byte) bool {
	if len(presented) != len(expected) {
		return false
	}
	if len(presented) == 0 {
		// Empty-vs-empty is a configuration error, not a match. Reject so
		// an unset env var never inadvertently authorizes a request.
		return false
	}
	return subtle.ConstantTimeCompare(presented, expected) == 1
}

// parseTriggerTimestamp extracts the unix-millisecond timestamp from
// Mattermost's trigger_id format. The current Mattermost format is
// "<request_id>.<unix_ms>" — we split on the last dot and parse the
// suffix as an int64. Older or non-conforming trigger_ids return an
// error so the caller can fall back to X-Request-Timestamp or reject.
func parseTriggerTimestamp(triggerID string) (time.Time, error) {
	if triggerID == "" {
		return time.Time{}, fmt.Errorf("trigger_id: empty")
	}
	idx := strings.LastIndex(triggerID, ".")
	if idx < 0 || idx == len(triggerID)-1 {
		return time.Time{}, fmt.Errorf("trigger_id %q: missing timestamp suffix", triggerID)
	}
	suffix := triggerID[idx+1:]
	ms, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("trigger_id %q: invalid timestamp: %w", triggerID, err)
	}
	return time.UnixMilli(ms), nil
}

// isStale returns true when t is older than maxAge measured against
// the supplied "now" function. A future timestamp drifting by more
// than maxAge is also rejected — that prevents an attacker who can
// influence the trigger_id from skipping the replay window forward.
func isStale(t, now time.Time, maxAge time.Duration) bool {
	if t.IsZero() {
		return true
	}
	delta := now.Sub(t)
	if delta < 0 {
		delta = -delta
	}
	return delta > maxAge
}
