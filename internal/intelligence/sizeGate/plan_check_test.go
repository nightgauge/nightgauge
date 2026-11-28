package sizeGate

import (
	"reflect"
	"testing"
)

func TestEvaluatePlan(t *testing.T) {
	on := DefaultGateConfig()
	zero := DefaultGateConfig()
	zero.MaxPlannedFiles = 0
	off := DefaultGateConfig()
	off.PlanCheckEnabled = false

	cases := []struct {
		name    string
		cfg     GateConfig
		in      PlanInput
		allowed bool
		fired   []string
	}{
		{"over threshold", on, PlanInput{PlannedFiles: 26}, false, []string{HeuristicPlannedFiles}},
		{"at threshold", on, PlanInput{PlannedFiles: 25}, true, []string{}},
		{"M issue, L plan", on, PlanInput{PlannedFiles: 3, PlanSize: "L", IssueSize: "size:M"}, false,
			[]string{HeuristicPlanSizeMismatch}},
		{"S issue, XL plan", on, PlanInput{PlannedFiles: 3, PlanSize: " xl ", IssueSize: "S"}, false,
			[]string{HeuristicPlanSizeMismatch}},
		{"XS issue, L plan", on, PlanInput{PlanSize: "L", IssueSize: "XS"}, false,
			[]string{HeuristicPlanSizeMismatch}},
		{"L issue, L plan", on, PlanInput{PlanSize: "L", IssueSize: "L"}, true, []string{}},
		{"M issue, M plan", on, PlanInput{PlanSize: "M", IssueSize: "M"}, true, []string{}},
		{"unknown issue size", on, PlanInput{PlanSize: "XL", IssueSize: ""}, true, []string{}},
		{"unknown plan size", on, PlanInput{PlanSize: "huge", IssueSize: "S"}, true, []string{}},
		{"both arms", on, PlanInput{PlannedFiles: 40, PlanSize: "XL", IssueSize: "M"}, false,
			[]string{HeuristicPlannedFiles, HeuristicPlanSizeMismatch}},
		{"max 0 disables the count arm", zero, PlanInput{PlannedFiles: 500}, true, []string{}},
		{"gate disabled", off, PlanInput{PlannedFiles: 500, PlanSize: "XL", IssueSize: "XS"}, true, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePlan(tc.cfg, tc.in)
			if got.Allowed != tc.allowed {
				t.Errorf("Allowed = %v, want %v (reason %q)", got.Allowed, tc.allowed, got.Reason)
			}
			if !reflect.DeepEqual(got.Heuristics, tc.fired) {
				t.Errorf("Heuristics = %v, want %v", got.Heuristics, tc.fired)
			}
			if got.Allowed != (got.Reason == "") {
				t.Errorf("Reason %q inconsistent with Allowed=%v", got.Reason, got.Allowed)
			}
		})
	}
}
