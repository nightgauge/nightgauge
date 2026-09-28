package config

import (
	"strings"
	"testing"
	"time"
)

// TestStageBudgetClampsUncappedCeilings is #2257: a stage no USD cap can bind
// cannot escape its budget with a very large positive value in place of the
// refused -1. The clamp is logged; a priced stage keeps its configured value.
func TestStageBudgetClampsUncappedCeilings(t *testing.T) {
	huge := map[string]StageBudget{StageBudgetDefaultKey: {
		MaxTurns:     2147483647,
		MaxWallClock: StageBudgetDuration(1000 * time.Hour),
		MaxTokens:    2147483647,
	}}
	for _, cost := range []StageCost{StageZeroCost, StageUnpriced} {
		got := ResolveStageBudget(huge, "feature-dev", cost)
		if got.MaxTurns != ZeroCostStageMaxTurnsCeiling ||
			got.MaxWallClock != ZeroCostStageMaxWallClockCeiling ||
			got.MaxTokens != ZeroCostStageMaxTokensCeiling {
			t.Errorf("cost %d: got %+v, want the hard maxima", cost, got)
		}
		if n := strings.Count(strings.Join(got.Warnings, "\n"), "clamped to"); n != 3 {
			t.Errorf("cost %d: %d clamp warnings, want 3: %q", cost, n, got.Warnings)
		}
	}
	priced := ResolveStageBudget(huge, "feature-dev", StagePriced)
	if priced.MaxTurns != 2147483647 || len(priced.Warnings) != 0 {
		t.Errorf("priced: got %+v, want the configured value unclamped", priced)
	}
	within := ResolveStageBudget(map[string]StageBudget{"feature-dev": {MaxTurns: 300}}, "feature-dev", StageZeroCost)
	if within.MaxTurns != 300 || len(within.Warnings) != 0 {
		t.Errorf("within the maximum: got %+v, want 300 and no warning", within)
	}
}
