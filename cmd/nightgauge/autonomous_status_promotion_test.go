package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TestFormatIdleFedVsStarved (#288, AC #5) asserts `nightgauge autonomous
// status` distinguishes "no work" from "work exists but nothing is Ready"
// using the LastPromotionEligible diagnostic populated by the promotion
// scan fix.
func TestFormatIdleFedVsStarved(t *testing.T) {
	tests := []struct {
		name  string
		state orchestrator.AutonomousState
		want  string
	}{
		{
			name:  "no promotable work reads as genuine idle",
			state: orchestrator.AutonomousState{LastPromotionEligible: 0},
			want:  "no work",
		},
		{
			name:  "promotable work reads as a fault, not idleness",
			state: orchestrator.AutonomousState{LastPromotionEligible: 4},
			want:  "4 Backlog issue(s) are gate-eligible but not yet Ready — this is a fault, not idleness",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatIdleFedVsStarved(tt.state)
			if !strings.Contains(got, tt.want) {
				t.Errorf("formatIdleFedVsStarved(%+v) = %q, want it to contain %q", tt.state, got, tt.want)
			}
		})
	}
}
