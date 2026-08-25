package main

import (
	"context"
	"sort"
	"testing"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// TestDoctorCheckOrder_CoversEveryEmittedCheck pins the render list against
// what RunDoctor actually produces (#912).
//
// The failure this catches is silent by construction: a new arm added to
// RunDoctor lands in result.Checks and in --json, its own unit tests pass, and
// the human `nightgauge doctor` output — the surface an operator actually
// reads — never mentions it. That reads as "the product does not check for
// that", which is exactly the invisibility the stranded-branch arm was added
// to end. Adding the arm and forgetting this list would have reproduced the
// bug one layer up.
//
// Driven off a real RunDoctor call rather than a hand-listed set, so a future
// arm is covered without anyone remembering to extend the test.
func TestDoctorCheckOrder_CoversEveryEmittedCheck(t *testing.T) {
	// nil client/config is enough: every environment-independent arm still
	// writes its row, which is all this test reads.
	result := doctor.RunDoctor(context.Background(), nil, nil, nil)

	rendered := make(map[string]bool, len(doctorCheckOrder))
	for _, key := range doctorCheckOrder {
		rendered[key] = true
	}

	var missing []string
	for key := range result.Checks {
		if !rendered[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("doctor emits %v but the human output never renders them — add them to doctorCheckOrder", missing)
	}
}
