package main

import "testing"

// TestModelAccuracyAlternate_DoesNotPointAtANull guards the one line the tool
// offers when it cannot tune.
//
// It named modelAccuracy unconditionally. On a corpus where the SIZE pair is
// unmeasurable the MODEL pair usually is too — this repo's real corpus had zero
// of both — so the fallback advice pointed the operator at a second null (#994).
func TestModelAccuracyAlternate_DoesNotPointAtANull(t *testing.T) {
	got := modelAccuracyAlternate(nil)
	if got == "modelAccuracy (reported above; not a tuning target)" {
		t.Error("with a nil modelAccuracy the tool still points the operator at it")
	}
	if got == "" {
		t.Error("no alternate text at all")
	}

	v := 0.75
	if got := modelAccuracyAlternate(&v); got == "" {
		t.Error("a measurable modelAccuracy should still be offered")
	}
}
