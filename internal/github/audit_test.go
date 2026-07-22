package github

import (
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestResolveItemRepo(t *testing.T) {
	tests := []struct {
		name                      string
		itemRepo, fbOwner, fbRepo string
		wantOwner, wantRepo       string
	}{
		{"cross-repo owner/repo wins over fallback", "acme/platform", "nightgauge", "nightgauge", "acme", "platform"},
		{"different owner is honored", "OtherOrg/some-repo", "nightgauge", "nightgauge", "OtherOrg", "some-repo"},
		{"empty item repo falls back", "", "nightgauge", "nightgauge", "nightgauge", "nightgauge"},
		{"bare name (no slash) falls back", "just-a-name", "nightgauge", "nightgauge", "nightgauge", "nightgauge"},
		{"malformed empty-owner falls back", "/lonely-repo", "nightgauge", "nightgauge", "nightgauge", "nightgauge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOwner, gotRepo := resolveItemRepo(tt.itemRepo, tt.fbOwner, tt.fbRepo)
			if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
				t.Errorf("resolveItemRepo(%q,%q,%q) = (%q,%q), want (%q,%q)",
					tt.itemRepo, tt.fbOwner, tt.fbRepo, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestNewLifecycleAuditService(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewLifecycleAuditService(client, "nightgauge", 5)
	if svc == nil {
		t.Fatal("NewLifecycleAuditService returned nil")
	}
	if svc.client != client {
		t.Error("client not set correctly")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.projectNumber != 5 {
		t.Errorf("projectNumber = %d, want 5", svc.projectNumber)
	}
}

func TestBuildAuditSummary_Empty(t *testing.T) {
	s := buildAuditSummary(nil)
	if s.Total != 0 {
		t.Errorf("Total = %d, want 0", s.Total)
	}
	if s.Fixed != 0 || s.Errors != 0 {
		t.Errorf("empty findings produced non-zero Fixed/Errors: %+v", s)
	}
}

func TestBuildAuditSummary_AllCategories(t *testing.T) {
	findings := []LifecycleFinding{
		{Category: "STALE_EPIC", Fixed: true},
		{Category: "STALE_EPIC"},
		{Category: "BOARD_STATUS_DRIFT", Fixed: true},
		{Category: "PREMATURE_DONE", FixError: "failed"},
		{Category: "ORPHANED_ISSUE"},
		{Category: "STALE_BLOCKER"},
	}
	s := buildAuditSummary(findings)

	if s.Total != 6 {
		t.Errorf("Total = %d, want 6", s.Total)
	}
	if s.StaleEpics != 2 {
		t.Errorf("StaleEpics = %d, want 2", s.StaleEpics)
	}
	if s.StatusDrift != 1 {
		t.Errorf("StatusDrift = %d, want 1", s.StatusDrift)
	}
	if s.PrematureDone != 1 {
		t.Errorf("PrematureDone = %d, want 1", s.PrematureDone)
	}
	if s.Orphaned != 1 {
		t.Errorf("Orphaned = %d, want 1", s.Orphaned)
	}
	if s.StaleBlocker != 1 {
		t.Errorf("StaleBlocker = %d, want 1", s.StaleBlocker)
	}
	if s.Fixed != 2 {
		t.Errorf("Fixed = %d, want 2", s.Fixed)
	}
	if s.Errors != 1 {
		t.Errorf("Errors = %d, want 1", s.Errors)
	}
}

func TestLifecycleFindingJSON(t *testing.T) {
	f := LifecycleFinding{
		Category:    "STALE_EPIC",
		Severity:    "high",
		IssueNumber: 42,
		IssueTitle:  "Test Epic",
		IssueState:  "OPEN",
		Detail:      "all sub-issues closed",
		Fixed:       true,
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got LifecycleFinding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Category != "STALE_EPIC" {
		t.Errorf("Category = %q, want STALE_EPIC", got.Category)
	}
	if got.IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", got.IssueNumber)
	}
	if !got.Fixed {
		t.Error("Fixed should be true")
	}
}

func TestLifecycleFindingJSON_OmitEmpty(t *testing.T) {
	// board_status and fix_error should be omitted when empty
	f := LifecycleFinding{
		Category:    "STALE_EPIC",
		Severity:    "high",
		IssueNumber: 1,
		IssueTitle:  "Epic",
		IssueState:  "OPEN",
		Detail:      "stale",
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if contains(jsonStr, "board_status") {
		t.Errorf("empty board_status should be omitted: %s", jsonStr)
	}
	if contains(jsonStr, "fix_error") {
		t.Errorf("empty fix_error should be omitted: %s", jsonStr)
	}
}

func TestLifecycleAuditResultJSON(t *testing.T) {
	result := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      "nightgauge/nightgauge",
		RunAt:     "2026-03-23T00:00:00Z",
		FixMode:   false,
		Findings: []LifecycleFinding{
			{
				Category:    "STALE_EPIC",
				Severity:    "high",
				IssueNumber: 100,
				IssueTitle:  "Old Epic",
				IssueState:  "OPEN",
				Detail:      "all sub-issues closed",
			},
		},
		Summary: AuditSummary{
			Total:      1,
			StaleEpics: 1,
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got LifecycleAuditResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Dimension != "epic-lifecycle" {
		t.Errorf("Dimension = %q, want epic-lifecycle", got.Dimension)
	}
	if got.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", got.Repo)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("Findings len = %d, want 1", len(got.Findings))
	}
	if got.Findings[0].IssueNumber != 100 {
		t.Errorf("Findings[0].IssueNumber = %d, want 100", got.Findings[0].IssueNumber)
	}
	if got.Summary.StaleEpics != 1 {
		t.Errorf("Summary.StaleEpics = %d, want 1", got.Summary.StaleEpics)
	}
}

func TestAuditSummaryJSON(t *testing.T) {
	s := AuditSummary{
		Total:         10,
		StaleEpics:    2,
		StatusDrift:   3,
		PrematureDone: 1,
		Orphaned:      2,
		StaleBlocker:  2,
		Fixed:         5,
		Errors:        1,
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got AuditSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Total != 10 || got.Fixed != 5 || got.Errors != 1 {
		t.Errorf("round-trip failed: %+v", got)
	}
}

func TestBuildAuditSummary_UnknownCategory(t *testing.T) {
	// Unknown categories do not increment any named counter but do count toward Total.
	findings := []LifecycleFinding{
		{Category: "UNKNOWN_FUTURE_CATEGORY"},
	}
	s := buildAuditSummary(findings)
	if s.Total != 1 {
		t.Errorf("Total = %d, want 1", s.Total)
	}
	if s.StaleEpics+s.StatusDrift+s.PrematureDone+s.Orphaned+s.StaleBlocker != 0 {
		t.Errorf("unexpected category counter incremented: %+v", s)
	}
}

func TestBuildAuditSummary_NewCategories(t *testing.T) {
	findings := []LifecycleFinding{
		{Category: "CLOSED_WITH_OPEN_PR", Fixed: true},
		{Category: "CLOSED_WITH_OPEN_PR"},
		{Category: "OPEN_PR_CLOSED_ISSUE"},
	}
	s := buildAuditSummary(findings)

	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.ClosedWithOpenPR != 2 {
		t.Errorf("ClosedWithOpenPR = %d, want 2", s.ClosedWithOpenPR)
	}
	if s.OpenPRClosedIssue != 1 {
		t.Errorf("OpenPRClosedIssue = %d, want 1", s.OpenPRClosedIssue)
	}
	if s.Fixed != 1 {
		t.Errorf("Fixed = %d, want 1", s.Fixed)
	}
}

func TestPRReferencesIssue_BranchName(t *testing.T) {
	pr := types.PullRequest{HeadRef: "feat/42-my-feature", Body: ""}
	if !prReferencesIssue(pr, 42) {
		t.Error("expected prReferencesIssue to match feat/42-* branch")
	}
	if prReferencesIssue(pr, 43) {
		t.Error("expected prReferencesIssue to NOT match issue 43")
	}
}

func TestPRReferencesIssue_FixBranch(t *testing.T) {
	pr := types.PullRequest{HeadRef: "fix/100-bugfix", Body: ""}
	if !prReferencesIssue(pr, 100) {
		t.Error("expected prReferencesIssue to match fix/100-* branch")
	}
}

func TestPRReferencesIssue_PRBodyCloseKeyword(t *testing.T) {
	tests := []struct {
		body    string
		wantNum int
	}{
		{"Closes #99", 99},
		{"closes #99", 99},
		{"Fixes #200\nsome text", 200},
		{"Resolves #1", 1},
	}
	for _, tc := range tests {
		pr := types.PullRequest{HeadRef: "some-unrelated-branch", Body: tc.body}
		if !prReferencesIssue(pr, tc.wantNum) {
			t.Errorf("expected prReferencesIssue to match issue #%d from body %q", tc.wantNum, tc.body)
		}
	}
}

func TestPRReferencesIssue_NoMatch(t *testing.T) {
	pr := types.PullRequest{HeadRef: "main", Body: "nothing here"}
	if prReferencesIssue(pr, 42) {
		t.Error("expected no match for unrelated PR")
	}
}

func TestExtractIssueNumber_Branch(t *testing.T) {
	pr := types.PullRequest{HeadRef: "feat/3794-pipeline-issues"}
	n := extractIssueNumber(pr)
	if n != 3794 {
		t.Errorf("extractIssueNumber = %d, want 3794", n)
	}
}

func TestExtractIssueNumber_Body(t *testing.T) {
	pr := types.PullRequest{HeadRef: "some-branch", Body: "Closes #55"}
	n := extractIssueNumber(pr)
	if n != 55 {
		t.Errorf("extractIssueNumber = %d, want 55", n)
	}
}

func TestExtractIssueNumber_None(t *testing.T) {
	pr := types.PullRequest{HeadRef: "main", Body: "no issue ref"}
	n := extractIssueNumber(pr)
	if n != 0 {
		t.Errorf("extractIssueNumber = %d, want 0 for unrelated PR", n)
	}
}

func TestAuditSummaryJSON_NewFields(t *testing.T) {
	s := AuditSummary{
		Total:             5,
		ClosedWithOpenPR:  2,
		OpenPRClosedIssue: 3,
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AuditSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ClosedWithOpenPR != 2 {
		t.Errorf("ClosedWithOpenPR = %d, want 2", got.ClosedWithOpenPR)
	}
	if got.OpenPRClosedIssue != 3 {
		t.Errorf("OpenPRClosedIssue = %d, want 3", got.OpenPRClosedIssue)
	}
}
