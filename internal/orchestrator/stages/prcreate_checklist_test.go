package stages

import (
	"encoding/json"
	"testing"
)

// TestManualChecklistOpen pins the checklist shapes pr-create's deterministic
// path accepts, matching the SDK schema's normalization, with status strings
// read strictly. The status-string case is the shape a local model wrote in a
// validate stage whose checklist was fully verified yet punted pr-create.
func TestManualChecklistOpen(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		open bool
	}{
		{"absent", ``, false},
		{"null", `null`, false},
		{"empty list", `[]`, false},
		{"canonical verified", `[{"item":"a","verified":true}]`, false},
		{"canonical open", `[{"item":"a","verified":true},{"item":"b","verified":false}]`, true},
		{"status passed with evidence", `[{"item":"units 5 km parsec","status":"passed","evidence":"exit 2"}]`, false},
		{"status failed", `[{"item":"a","status":"failed"}]`, true},
		{"status pending", `[{"item":"a","status":"pending"}]`, true},
		{"alt keys done", `[{"description":"a","done":true}]`, false},
		{"checked false", `[{"item":"a","checked":false}]`, true},
		{"record form all true", `{"check X":true,"check Y":true}`, false},
		{"record form one false", `{"check X":true,"check Y":false}`, true},
		{"plain strings", `["check X"]`, true},
		{"entry with no flag", `[{"item":"a"}]`, true},
		{"unreadable", `42`, true},
	}
	for _, c := range cases {
		if got := manualChecklistOpen(json.RawMessage(c.raw)); got != c.open {
			t.Errorf("%s: manualChecklistOpen(%s) = %v, want %v", c.name, c.raw, got, c.open)
		}
	}
}
