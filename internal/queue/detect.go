package queue

import "strings"

// IsEpic returns true when the labels slice contains the "type:epic" label.
// Case-insensitive to guard against label-name drift.
func IsEpic(labels []string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, "type:epic") {
			return true
		}
	}
	return false
}
