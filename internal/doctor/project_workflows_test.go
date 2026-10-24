package doctor

import (
	"sort"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func TestProjectWorkflowFindings(t *testing.T) {
	closed := gh.ProjectWorkflow{Name: "Item closed", Enabled: true}
	merged := gh.ProjectWorkflow{Name: "Pull request merged", Enabled: false}
	archive := gh.ProjectWorkflow{Name: "Auto-archive items", Enabled: true}
	cases := []struct {
		name     string
		declared map[string]any
		board    []gh.ProjectWorkflow
		want     []string // "state/severity", sorted
	}{
		{"agrees", map[string]any{"item_closed_to_done": true, "pr_merged_to_done": false, "auto_archive_days": 14},
			[]gh.ProjectWorkflow{closed, merged, archive}, nil},
		{"declared on, board off", map[string]any{"pr_merged_to_done": true}, []gh.ProjectWorkflow{merged},
			[]string{"drift/blocker"}},
		{"declared off, board on", map[string]any{"item_closed_to_done": false}, []gh.ProjectWorkflow{closed},
			[]string{"drift/blocker"}},
		{"archive zero is off", map[string]any{"auto_archive_days": 0}, []gh.ProjectWorkflow{archive},
			[]string{"drift/blocker"}},
		{"missing on board", map[string]any{"auto_close_issue": true}, nil,
			[]string{"missing_on_board/warning"}},
		{"undeclared board workflow", map[string]any{"item_closed_to_done": true}, []gh.ProjectWorkflow{closed, merged},
			[]string{"undeclared/warning"}},
		{"unknown key", map[string]any{"made_up": true}, nil, []string{"unknown_key/warning"}},
		{"wrong type", map[string]any{"item_closed_to_done": "yes"}, []gh.ProjectWorkflow{closed},
			[]string{"bad_value/warning", "undeclared/warning"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range projectWorkflowFindings(tc.declared, tc.board) {
				got = append(got, f.Evidence["state"]+"/"+string(f.Severity))
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProjectWorkflowDriftNamesTheWorkflow(t *testing.T) {
	fs := projectWorkflowFindings(map[string]any{"pr_merged_to_done": true},
		[]gh.ProjectWorkflow{{Name: "Pull request merged", Enabled: false}})
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "Pull request merged") ||
		!strings.Contains(fs[0].Title, "pr_merged_to_done") || fs[0].Severity != SeverityBlocker {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestProjectWorkflowsRegistered(t *testing.T) {
	for _, c := range DefaultRegistry().Checks() {
		if c.ID == "project_workflows" {
			if c.Code != "NGD048" {
				t.Errorf("code = %s", c.Code)
			}
			return
		}
	}
	t.Fatal("project_workflows is not registered")
}
