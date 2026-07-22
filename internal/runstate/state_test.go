package runstate

import (
	"strings"
	"testing"
)

func TestIsSchemaCompatible(t *testing.T) {
	cases := []struct {
		file, expected string
		want           bool
	}{
		{"1.0", "1.0", true},
		{"1.1", "1.0", false}, // future minor — refuse
		{"1.0", "1.1", true},  // older minor — accept
		{"2.0", "1.0", false}, // major mismatch
		{"abc", "1.0", false},
	}
	for _, c := range cases {
		got := IsSchemaCompatible(c.file, c.expected)
		if got != c.want {
			t.Errorf("IsSchemaCompatible(%q, %q) = %v; want %v", c.file, c.expected, got, c.want)
		}
	}
}

func TestNewRunIDIsUUIDv7Shape(t *testing.T) {
	id, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("len = %d; want 36", len(id))
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 hyphenated parts, got %d (%q)", len(parts), id)
	}
	if parts[2][0] != '7' {
		t.Errorf("version nibble = %c; want 7 (got %q)", parts[2][0], id)
	}
	// Variant nibble: parts[3][0] must be one of 8,9,a,b
	v := parts[3][0]
	if v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Errorf("variant nibble = %c; want 8/9/a/b (got %q)", v, id)
	}
}

func TestValidate_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*RunState)
		want string
	}{
		{"missing schema_version", func(rs *RunState) { rs.SchemaVersion = "" }, "schema_version"},
		{"bad schema major", func(rs *RunState) { rs.SchemaVersion = "2.0" }, "compatible"},
		{"bad state", func(rs *RunState) { rs.State = "bogus" }, "invalid state"},
		{"missing run_id", func(rs *RunState) { rs.RunID = "" }, "run_id"},
		{"bad attempt", func(rs *RunState) { rs.AttemptNumber = 0 }, "attempt_number"},
		{"missing branch", func(rs *RunState) { rs.Branch = "" }, "branch"},
		{"empty attempts", func(rs *RunState) { rs.Attempts = nil }, "attempts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := newValidFixture()
			c.mut(rs)
			err := rs.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v; want substring %q", err, c.want)
			}
		})
	}
}

func newValidFixture() *RunState {
	stage := StageIssuePickup
	pid := 1
	host := "h"
	return &RunState{
		SchemaVersion:   SchemaVersion,
		IssueNumber:     1,
		State:           StateRunning,
		RunID:           "00000000-0000-7000-8000-000000000000",
		AttemptNumber:   1,
		CompletedStages: []Stage{},
		ResumeFromStage: &stage,
		Branch:          "feat/test",
		CreatedAt:       "2026-05-06T00:00:00Z",
		UpdatedAt:       "2026-05-06T00:00:00Z",
		Attempts: []Attempt{{
			RunID:         "00000000-0000-7000-8000-000000000000",
			AttemptNumber: 1,
			StartedAt:     "2026-05-06T00:00:00Z",
			PID:           &pid,
			HostID:        &host,
			LastStage:     &stage,
		}},
	}
}
