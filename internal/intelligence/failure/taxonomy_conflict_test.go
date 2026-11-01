package failure

import "testing"

// The escalation text carries PR numbers and file paths, so a marker must beat
// every substring ladder: this exact text read as a network timeout (#2841).
func TestClassify_MergeConflictMarkerBeatsSubstringLadders(t *testing.T) {
	text := "[merge-conflict] deterministic pr-merge refused: merge-queue-failed: merge conflict " +
		"with main: PR #429 is CONFLICTING; conflicting files: internal/timeout.go, CHANGELOG.md"
	got := NewClassifier().Classify("pr-merge", 1, text)
	if got.Category != CatDeterministic || got.Description != "merge conflict — requires manual resolution" {
		t.Fatalf("got %+v", got)
	}
}
