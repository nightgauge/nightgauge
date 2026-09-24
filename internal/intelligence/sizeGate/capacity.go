package sizeGate

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// Capacity-aware sizing (#1655, ADR 023 Q9): a model's context window caps
// the largest issue size it may take. The table itself lives in
// internal/skillrender/budget.go, the file ADR 023 names for it, and every
// enforcement point reads it through MaxSizeForWindow.

// CapacityDecomposedMarker is the HTML-comment marker issue-create writes as
// the first line of every child it creates when the capacity gate forces a
// decomposition. It is what bounds decomposition to one level: a marked issue
// that is still over capacity is never decomposed again automatically.
const CapacityDecomposedMarker = "nightgauge:capacity-decomposed"

// Recovery values a capacity rejection carries.
const (
	// RecoveryDecompose: split the issue into sub-issues each within the cap.
	RecoveryDecompose = "decompose"
	// RecoveryHumanDecomposition: the issue is already a capacity-forced
	// child, so the one automatic level is spent and a person decides.
	RecoveryHumanDecomposition = "requires human decomposition"
)

// sizeRank orders the five size buckets; 0 means unrecognized.
var sizeRank = map[string]int{"XS": 1, "S": 2, "M": 3, "L": 4, "XL": 5}

// NormalizeSize returns size as one of XS, S, M, L, XL, accepting a bare
// bucket or a `size:` label in any case, or "" when it is none of them.
func NormalizeSize(size string) string {
	s := strings.ToUpper(strings.TrimSpace(size))
	s = strings.TrimPrefix(s, "SIZE:")
	if sizeRank[s] == 0 {
		return ""
	}
	return s
}

// SizeFromLabels returns the largest size:* label on an issue, or "".
func SizeFromLabels(labels []string) string {
	best := ""
	for _, l := range labels {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "size:") {
			continue
		}
		if s := NormalizeSize(l); s != "" && sizeRank[s] > sizeRank[best] {
			best = s
		}
	}
	return best
}

// MaxSizeForWindow returns the largest issue size a model with window tokens
// of context may take, read from the ADR 023 capacity table. ok is false for
// an unknown window (<= 0), for which no cap applies.
func MaxSizeForWindow(window int) (string, bool) {
	return skillrender.MaxIssueSizeForWindow(window)
}

// SizeExceeds reports whether size is larger than maxSize. An unrecognized
// value on either side exceeds nothing.
func SizeExceeds(size, maxSize string) bool {
	s, m := sizeRank[NormalizeSize(size)], sizeRank[NormalizeSize(maxSize)]
	return s > 0 && m > 0 && s > m
}

// IsCapacityDecomposedChild reports whether an issue body carries
// CapacityDecomposedMarker.
func IsCapacityDecomposedChild(body string) bool {
	return strings.Contains(body, CapacityDecomposedMarker)
}

// CapacityResult is the verdict of one capacity check.
type CapacityResult struct {
	// Applied is false when the window or the size is unknown: no cap
	// applied, and Note says which.
	Applied bool
	// Allowed is false only when Applied and the size exceeds the cap.
	Allowed bool
	Size    string
	Window  int
	MaxSize string
	// Reason names the size, the window and the cap on a rejection.
	Reason string
	// Recovery is RecoveryDecompose or RecoveryHumanDecomposition on a
	// rejection, "" otherwise.
	Recovery string
	// Note is the one line a caller logs for this check.
	Note string
}

// CheckCapacity judges size against the cap for window. decomposedChild is
// true when the issue is already a capacity-forced child
// (IsCapacityDecomposedChild): a rejection then recovers by human
// decomposition, never by a second automatic one. The check is a pure
// function of its inputs and calls nothing, so no evaluation of it can loop.
func CheckCapacity(size string, window int, decomposedChild bool) CapacityResult {
	res := CapacityResult{Allowed: true, Size: NormalizeSize(size), Window: window}
	maxSize, known := MaxSizeForWindow(window)
	if !known {
		res.Note = fmt.Sprintf("capacity: window unknown — no capacity cap applied (size %s)", displaySize(res.Size))
		return res
	}
	res.MaxSize = maxSize
	if res.Size == "" {
		res.Note = fmt.Sprintf("capacity: size unknown — no capacity cap applied (window %d, cap %s)", window, maxSize)
		return res
	}
	res.Applied = true
	if !SizeExceeds(res.Size, maxSize) {
		res.Note = fmt.Sprintf("capacity: size %s within the cap %s for a %d-token window", res.Size, maxSize, window)
		return res
	}
	res.Allowed = false
	res.Recovery = RecoveryDecompose
	if decomposedChild {
		res.Recovery = RecoveryHumanDecomposition
	}
	res.Reason = fmt.Sprintf("size %s exceeds the capacity cap %s for a %d-token context window (recovery: %s)",
		res.Size, maxSize, window, res.Recovery)
	res.Note = "capacity: " + res.Reason
	return res
}

func displaySize(size string) string {
	if size == "" {
		return "unknown"
	}
	return size
}

// CapacityCandidate is a fallback model and the context window it runs with
// (0 when unknown).
type CapacityCandidate struct {
	Model  string
	Window int
}

// FirstAdmittingFallback returns the first candidate, in the operator's
// order, whose window admits size. A candidate with an unknown window is
// skipped: nothing shows it can hold the issue.
func FirstAdmittingFallback(size string, candidates []CapacityCandidate) (CapacityCandidate, bool) {
	for _, c := range candidates {
		maxSize, known := MaxSizeForWindow(c.Window)
		if known && !SizeExceeds(size, maxSize) {
			return c, true
		}
	}
	return CapacityCandidate{}, false
}
