package graduation

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

func TestScoreADR_Table(t *testing.T) {
	cases := []struct {
		name        string
		sig         signalSet
		wantScore   int
		wantReasons []string
	}{
		{
			name: "all positive signals with 2 distinct recall hits",
			sig: signalSet{
				RecallHitsDistinct: 2,
				GeneralLanguage:    true,
				PatternLanguage:    true,
				FilledConsequences: true,
			},
			wantScore:   8,
			wantReasons: []string{"recall_hits:2", "general_language", "pattern_language", "filled_consequences"},
		},
		{
			name: "file paths in decision suppress general_language",
			sig: signalSet{
				RecallHitsDistinct: 0,
				GeneralLanguage:    false,
				PatternLanguage:    true,
				FilledConsequences: true,
			},
			wantScore:   3,
			wantReasons: []string{"pattern_language", "filled_consequences"},
		},
		{
			name: "graduated marker applies -2",
			sig: signalSet{
				GeneralLanguage:    true,
				PatternLanguage:    true,
				FilledConsequences: true,
				AlreadyGraduated:   true,
			},
			wantScore:   3,
			wantReasons: []string{"general_language", "pattern_language", "filled_consequences", "already_graduated"},
		},
		{
			name: "issue-specific title applies -1",
			sig: signalSet{
				GeneralLanguage:    true,
				PatternLanguage:    true,
				IssueSpecificTitle: true,
			},
			wantScore:   3,
			wantReasons: []string{"general_language", "pattern_language", "issue_specific_title"},
		},
		{
			name: "empty consequences yields no +1",
			sig: signalSet{
				GeneralLanguage:    true,
				PatternLanguage:    true,
				FilledConsequences: false,
			},
			wantScore:   4,
			wantReasons: []string{"general_language", "pattern_language"},
		},
		{
			name:        "threshold boundary - score 3 (sub-threshold)",
			sig:         signalSet{GeneralLanguage: true, FilledConsequences: true},
			wantScore:   3,
			wantReasons: []string{"general_language", "filled_consequences"},
		},
		{
			name:        "threshold boundary - score 4 (qualifies)",
			sig:         signalSet{GeneralLanguage: true, PatternLanguage: true},
			wantScore:   4,
			wantReasons: []string{"general_language", "pattern_language"},
		},
		{
			name:        "single recall hit below distinct-2 threshold gives no +3",
			sig:         signalSet{RecallHitsDistinct: 1, GeneralLanguage: true},
			wantScore:   2,
			wantReasons: []string{"general_language"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, reasons := scoreADR(tc.sig)
			if score != tc.wantScore {
				t.Errorf("score = %d, want %d (reasons=%v)", score, tc.wantScore, reasons)
			}
			if !reflect.DeepEqual(reasons, tc.wantReasons) {
				t.Errorf("reasons = %v, want %v", reasons, tc.wantReasons)
			}
		})
	}
}

func TestContainsPatternKeyword(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"This pattern MUST be applied", true},
		{"We must ship this — but lowercase 'must' is not RFC 2119", false},
		{"always prefer the simpler approach", true},
		{"never roll your own crypto", true},
		{"every service must register", true},
		{"all callers should validate", true},
		{"any service that recalls", true},
		{"completely unrelated prose with no rule keywords", false},
		{"wallet uses installments", false}, // "all" must be word-bounded
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := containsPatternKeyword(tc.text); got != tc.want {
				t.Errorf("containsPatternKeyword(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestIsFilledConsequences(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty", "", false},
		{"short prose under 30 chars", "Too short.", false},
		{"template placeholder marker", "Please replace this placeholder with real text.", false},
		{"bracketed placeholder literal", "[Expected impact, trade-offs, and follow-up actions]", false},
		{"substantive prose", "Reviewers gain unambiguous candidates and false positives stay low.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFilledConsequences(tc.body); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecallSignal_DistinctIssueCounting(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	decisionsRel := ".nightgauge/knowledge/features/100-feature/decisions.md"
	decisionsAbs := "/abs/.nightgauge/knowledge/features/100-feature/decisions.md"

	events := []telemetry.Event{
		// 3 events from 2 distinct issues -> distinct=2 triggers +3.
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 200, Timestamp: "2026-02-01T00:00:00Z"},
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 200, Timestamp: "2026-02-02T00:00:00Z"},
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 201, Timestamp: "2026-02-03T00:00:00Z"},
		// Source issue itself - must be filtered.
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 100, Timestamp: "2026-02-04T00:00:00Z"},
		// Before cutoff - filtered.
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 202, Timestamp: "2025-12-01T00:00:00Z"},
		// Wrong path - filtered.
		{Type: telemetry.EventRecallHit, Path: "other.md", IssueNumber: 203, Timestamp: "2026-02-05T00:00:00Z"},
		// Non-RecallHit type - filtered.
		{Type: telemetry.EventRead, Path: decisionsRel, IssueNumber: 204, Timestamp: "2026-02-06T00:00:00Z"},
		// Malformed timestamp - filtered.
		{Type: telemetry.EventRecallHit, Path: decisionsRel, IssueNumber: 205, Timestamp: "not-a-date"},
	}

	distinct, total := recallSignal(events, decisionsRel, decisionsAbs, 100, cutoff)
	if distinct != 2 {
		t.Errorf("distinct = %d, want 2", distinct)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
}

func TestRecallSignal_AbsolutePathMatch(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rel := ".nightgauge/knowledge/features/100-feature/decisions.md"
	abs := "/workspace/" + rel

	events := []telemetry.Event{
		{Type: telemetry.EventRecallHit, Path: abs, IssueNumber: 200, Timestamp: "2026-02-01T00:00:00Z"},
		{Type: telemetry.EventRecallHit, Path: abs, IssueNumber: 201, Timestamp: "2026-02-02T00:00:00Z"},
	}
	distinct, _ := recallSignal(events, rel, abs, 100, cutoff)
	if distinct != 2 {
		t.Errorf("distinct = %d, want 2 (absolute path mismatch)", distinct)
	}
}

func TestSuggestedDest_KeywordFamilies(t *testing.T) {
	docsDir := t.TempDir()
	files := []string{
		"ARCHITECTURE.md", "CODE_STANDARDS.md", "TESTING.md", "GIT_WORKFLOW.md",
		"KNOWLEDGE_BASE.md", "FORGE_ABSTRACTION.md",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(docsDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"architecture keyword", "Pipeline architecture refactor", "docs/ARCHITECTURE.md"},
		{"standards keyword", "Naming standards for handlers", "docs/CODE_STANDARDS.md"},
		{"testing keyword", "Test coverage policy", "docs/TESTING.md"},
		{"git keyword", "Branch workflow changes", "docs/GIT_WORKFLOW.md"},
		{"forge keyword", "Forge abstraction routing", "docs/FORGE_ABSTRACTION.md"},
		{"no match fallback", "Totally unrelated title", FallbackDest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestedDest(knowledge.ADRBlock{Title: tc.title}, docsDir)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestedDest_FallbackWhenDocsMissing(t *testing.T) {
	got := suggestedDest(knowledge.ADRBlock{Title: "Architecture decision"}, "/nonexistent")
	if got != FallbackDest {
		t.Errorf("got %q, want %q (fallback)", got, FallbackDest)
	}
}

func TestSuggestedDest_TieBreakAlphabetical(t *testing.T) {
	docsDir := t.TempDir()
	// Both files contain the token "policy" once; alphabetical wins.
	for _, f := range []string{"ALPHA_POLICY.md", "BETA_POLICY.md"} {
		if err := os.WriteFile(filepath.Join(docsDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := suggestedDest(knowledge.ADRBlock{Title: "Some policy"}, docsDir)
	if !strings.HasSuffix(got, "ALPHA_POLICY.md") {
		t.Errorf("got %q, want alphabetical tie-break winner ALPHA_POLICY.md", got)
	}
}

func TestDetectSignals_FromADR(t *testing.T) {
	adr := knowledge.ADRBlock{
		Title:        "Generic decision",
		Decision:     "Always prefer the simpler approach.",
		Consequences: "Reviewers get unambiguous candidates and false positives stay low.",
		Graduated:    false,
	}
	sig := detectSignals(adr, 3)
	if !sig.GeneralLanguage || !sig.PatternLanguage || !sig.FilledConsequences {
		t.Errorf("expected positive signals, got %+v", sig)
	}
	if sig.RecallHitsDistinct != 3 {
		t.Errorf("RecallHitsDistinct = %d, want 3", sig.RecallHitsDistinct)
	}
	if sig.IssueSpecificTitle {
		t.Errorf("IssueSpecificTitle should be false for %q", adr.Title)
	}

	adrIssueSpecific := knowledge.ADRBlock{Title: "Hack for issue #42", Decision: "Touches packages/foo/bar.go"}
	sig2 := detectSignals(adrIssueSpecific, 0)
	if !sig2.IssueSpecificTitle {
		t.Errorf("expected IssueSpecificTitle=true for %q", adrIssueSpecific.Title)
	}
	if sig2.GeneralLanguage {
		t.Errorf("expected GeneralLanguage=false when packages/ path present")
	}
}
