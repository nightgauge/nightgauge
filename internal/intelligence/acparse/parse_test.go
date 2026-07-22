package acparse

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStatus    string
		wantChecked   int
		wantUnchecked int
		wantTotal     int
	}{
		{
			name:       "empty body",
			body:       "",
			wantStatus: StatusNotApplicable,
		},
		{
			name:       "body without checkboxes",
			body:       "## Notes\nfree prose.\n",
			wantStatus: StatusNotApplicable,
		},
		{
			name:        "all checked",
			body:        "- [x] a\n- [x] b\n",
			wantStatus:  StatusPassed,
			wantChecked: 2,
			wantTotal:   2,
		},
		{
			name:          "any unchecked → failed",
			body:          "- [x] a\n- [ ] b\n",
			wantStatus:    StatusFailed,
			wantChecked:   1,
			wantUnchecked: 1,
			wantTotal:     2,
		},
		{
			name:        "uppercase [X] accepted",
			body:        "- [X] a\n",
			wantStatus:  StatusPassed,
			wantChecked: 1,
			wantTotal:   1,
		},
		{
			name:          "asterisk and plus bullets",
			body:          "* [ ] a\n+ [x] b\n",
			wantStatus:    StatusFailed,
			wantChecked:   1,
			wantUnchecked: 1,
			wantTotal:     2,
		},
		{
			name:          "indented checkbox still counts",
			body:          "  - [ ] sub\n",
			wantStatus:    StatusFailed,
			wantUnchecked: 1,
			wantTotal:     1,
		},
		{
			name:       "boxes inside ``` fence ignored",
			body:       "## AC\n\n```yaml\n- [ ] x\n- [x] y\n```\n",
			wantStatus: StatusNotApplicable,
		},
		{
			name:       "boxes inside ~~~ fence ignored",
			body:       "intro\n\n~~~\n- [x] y\n~~~\n",
			wantStatus: StatusNotApplicable,
		},
		{
			name:          "CR-LF line endings",
			body:          "- [x] a\r\n- [ ] b\r\n",
			wantStatus:    StatusFailed,
			wantChecked:   1,
			wantUnchecked: 1,
			wantTotal:     2,
		},
		{
			name:       "substring inside prose ignored",
			body:       "see also - [x] in foo.md and - [ ] elsewhere.\n",
			wantStatus: StatusNotApplicable,
		},
		{
			name:        "long line outside fence still counts",
			body:        "- [x] " + strings.Repeat("y", 100*1024) + "\n",
			wantStatus:  StatusPassed,
			wantChecked: 1,
			wantTotal:   1,
		},
		{
			name:          "mix of checked, unchecked, and fenced",
			body:          "## AC\n\n- [x] one\n- [ ] two\n\n```\n- [ ] not counted\n```\n\n- [X] three\n",
			wantStatus:    StatusFailed,
			wantChecked:   2,
			wantUnchecked: 1,
			wantTotal:     3,
		},
		{
			name:       "tab-prefixed checkbox",
			body:       "\t- [x] tabbed\n",
			wantStatus: StatusPassed,
			// Regex accepts leading tabs
			wantChecked: 1,
			wantTotal:   1,
		},
		{
			name:        "no whitespace after closing bracket → not a checkbox",
			body:        "- [x]nospace\n",
			wantStatus:  StatusNotApplicable,
			wantChecked: 0,
			wantTotal:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.body)
			if got.V != 1 {
				t.Errorf("Parse(...).V = %d, want 1", got.V)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Parse(...).Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Checked != tt.wantChecked {
				t.Errorf("Parse(...).Checked = %d, want %d", got.Checked, tt.wantChecked)
			}
			if got.Unchecked != tt.wantUnchecked {
				t.Errorf("Parse(...).Unchecked = %d, want %d", got.Unchecked, tt.wantUnchecked)
			}
			if got.Total != tt.wantTotal {
				t.Errorf("Parse(...).Total = %d, want %d", got.Total, tt.wantTotal)
			}
		})
	}
}

func TestParseStability(t *testing.T) {
	body := "## AC\n\n- [x] one\n- [ ] two\n- [X] three\n"
	first := Parse(body)
	for i := 0; i < 5; i++ {
		got := Parse(body)
		if got != first {
			t.Fatalf("Parse is not pure: iteration %d returned %+v, expected %+v", i, got, first)
		}
	}
}
