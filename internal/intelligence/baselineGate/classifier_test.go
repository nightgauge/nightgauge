package baselineGate

import (
	"testing"
)

func TestClassifyAC_TriggerKeywords(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantHit  bool
		wantTrig string
	}{
		{"required check phrase", "Make CI a required check on main", true, "required check"},
		{"required status phrase", "Promote build to required status", true, "required status"},
		{"branch protection phrase", "Update branch protection rules", true, "branch protection"},
		{"ruleset phrase", "Add a new ruleset for main", true, "ruleset"},
		{"promote-to-required regex", "Promote `Integration` to required", true, ""},
		{"enforce-on-main regex", "Enforce the test gate on main", true, ""},
		{"make-required-check regex", "Make Integration & E2E Tests a required check", true, ""},
		{"no trigger — generic feature", "Add a button to settings page", false, ""},
		{"no trigger — empty body", "", false, ""},
		{"case-insensitive substring", "REQUIRED CHECK on the main branch", true, "required check"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ClassifyAC(tt.text)
			if m.Triggered != tt.wantHit {
				t.Errorf("ClassifyAC(%q).Triggered = %v, want %v (trigger=%q)", tt.text, m.Triggered, tt.wantHit, m.TriggerText)
			}
			if tt.wantTrig != "" && m.TriggerText != tt.wantTrig {
				t.Errorf("TriggerText = %q, want %q", m.TriggerText, tt.wantTrig)
			}
		})
	}
}

func TestClassifyAC_WorkflowExtraction(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"explicit workflow path",
			"Make `.github/workflows/ci.yml` a required check",
			"ci.yml",
		},
		{
			"workflow with hyphen and dot",
			"Promote .github/workflows/integration-e2e.yaml to required",
			"integration-e2e.yaml",
		},
		{
			"no workflow reference",
			"Make CI a required check",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ClassifyAC(tt.text)
			if !m.Triggered {
				t.Fatalf("expected trigger to fire on %q", tt.text)
			}
			if m.Workflow != tt.want {
				t.Errorf("Workflow = %q, want %q", m.Workflow, tt.want)
			}
		})
	}
}

func TestClassifyAC_JobExtraction(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"backticked display-name job",
			"Make `Integration & E2E Tests` a required check on main",
			"Integration & E2E Tests",
		},
		{
			"workflow path then backticked job",
			"Promote .github/workflows/ci.yml `Build and Test` to required",
			"Build and Test",
		},
		{
			"no backticked phrase",
			"Make CI a required check",
			"",
		},
		{
			"backticked path is rejected as job name",
			"Promote `.github/workflows/ci.yml` to required check",
			"",
		},
		{
			"backticked yaml file is rejected",
			"Promote `ci.yml` as a required check",
			"",
		},
		{
			"backticked single lowercase identifier rejected",
			"Make `ci` a required check",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ClassifyAC(tt.text)
			if !m.Triggered {
				t.Fatalf("expected trigger to fire on %q", tt.text)
			}
			if m.Job != tt.want {
				t.Errorf("Job = %q, want %q", m.Job, tt.want)
			}
		})
	}
}

func TestSplitACList(t *testing.T) {
	body := `Some preamble text.

- [ ] First criterion: make X required
- [x] Second criterion: protect main
- [ ] Third — multi-line
  with continuation

Trailing prose.`
	items := SplitACList(body)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %v", len(items), items)
	}
	if items[2] == "" || !contains(items[2], "Third") {
		t.Errorf("third item missing 'Third': %q", items[2])
	}
	if items[2] == "" || !contains(items[2], "continuation") {
		t.Errorf("third item should include continuation: %q", items[2])
	}
}

func TestSplitACList_NumberedList(t *testing.T) {
	body := `1. First item
2. Second item
3. Third item`
	items := SplitACList(body)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
}

func TestSplitACList_NoMarkers(t *testing.T) {
	body := "Just a single paragraph of text with no list markers."
	items := SplitACList(body)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0] != body {
		t.Errorf("got %q, want %q", items[0], body)
	}
}

func TestSplitACList_Empty(t *testing.T) {
	if got := SplitACList(""); got != nil {
		t.Errorf("SplitACList(\"\") = %v, want nil", got)
	}
}

// contains is a tiny helper used by tests above to avoid importing strings.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
