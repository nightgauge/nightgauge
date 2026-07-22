package audit

import (
	"context"
	"strings"
	"testing"
)

// mockIssueCreator records calls but does nothing — used to verify dry-run skips all mutations.
type mockIssueCreator struct {
	createCalls       int
	addSubIssueCalls  int
	addBlockedByCalls int
	addToBoardCalls   int
	setStatusCalls    int
	searchResults     map[string]struct {
		number int
		nodeID string
		found  bool
	}
}

func newMockIssueCreator() *mockIssueCreator {
	return &mockIssueCreator{
		searchResults: make(map[string]struct {
			number int
			nodeID string
			found  bool
		}),
	}
}

func (m *mockIssueCreator) GetRepositoryID(_ context.Context, _, _ string) (string, error) {
	return "repo-node-id", nil
}

func (m *mockIssueCreator) CreateIssueWithID(_ context.Context, _, _, _, _ string, _ []string) (string, int, error) {
	m.createCalls++
	return "new-node-id", m.createCalls, nil
}

func (m *mockIssueCreator) AddSubIssue(_ context.Context, _, _ string) error {
	m.addSubIssueCalls++
	return nil
}

func (m *mockIssueCreator) AddBlockedBy(_ context.Context, _, _ string) error {
	m.addBlockedByCalls++
	return nil
}

func (m *mockIssueCreator) AddToProjectBoard(_ context.Context, _ string, _ int, _ string) error {
	m.addToBoardCalls++
	return nil
}

func (m *mockIssueCreator) SetProjectItemStatus(_ context.Context, _ string, _ int, _, _ string) error {
	m.setStatusCalls++
	return nil
}

func (m *mockIssueCreator) SearchOpenIssueByTitle(_ context.Context, _, _, title string) (int, string, bool, error) {
	if r, ok := m.searchResults[title]; ok {
		return r.number, r.nodeID, r.found, nil
	}
	return 0, "", false, nil
}

func (m *mockIssueCreator) GetLabelID(_ context.Context, _, _, _ string) (string, error) {
	return "label-node-id", nil
}

// --- helpers ---

func makeReport(dimensions []*DimensionResult) *SynthesisReport {
	return &SynthesisReport{
		Dimensions: dimensions,
	}
}

func makeFinding(id, category, repo, severity string) AuditFinding {
	return AuditFinding{
		ID:          id,
		Category:    category,
		Repository:  repo,
		Severity:    severity,
		Description: "test finding " + id,
	}
}

// --- tests ---

func TestGroupFindingsByEpic(t *testing.T) {
	// 3 findings across 2 dimension/repo combos.
	dim1 := &DimensionResult{
		Name: "Dimension 1: API Alignment",
		Findings: []AuditFinding{
			makeFinding("f1", "CAT_A", "repo-alpha", "high"),
			makeFinding("f2", "CAT_B", "repo-alpha", "critical"),
		},
	}
	dim2 := &DimensionResult{
		Name: "Dimension 2: Lifecycle",
		Findings: []AuditFinding{
			makeFinding("f3", "CAT_C", "repo-beta", "medium"),
		},
	}

	report := makeReport([]*DimensionResult{dim1, dim2})
	epics := GroupFindingsByEpic(report)

	if len(epics) != 2 {
		t.Fatalf("expected 2 epics, got %d", len(epics))
	}

	// Verify epic titles contain both dimension name and repo.
	for _, e := range epics {
		if !strings.Contains(e.Title, e.Dimension) {
			t.Errorf("epic title %q does not contain dimension %q", e.Title, e.Dimension)
		}
		if !strings.Contains(e.Title, e.Repository) {
			t.Errorf("epic title %q does not contain repo %q", e.Title, e.Repository)
		}
	}

	// The epic for dim1 should have 2 findings sorted critical-first.
	var dim1Epic *Epic
	for _, e := range epics {
		if e.Dimension == dim1.Name {
			dim1Epic = e
			break
		}
	}
	if dim1Epic == nil {
		t.Fatal("could not find epic for Dimension 1")
	}
	if len(dim1Epic.Findings) != 2 {
		t.Fatalf("expected 2 findings in dim1 epic, got %d", len(dim1Epic.Findings))
	}
	if strings.ToLower(dim1Epic.Findings[0].Severity) != "critical" {
		t.Errorf("expected first finding to be critical after sort, got %q", dim1Epic.Findings[0].Severity)
	}
}

func TestGroupFindingsByEpicEmpty(t *testing.T) {
	report := makeReport([]*DimensionResult{})
	epics := GroupFindingsByEpic(report)
	if len(epics) != 0 {
		t.Fatalf("expected 0 epics for empty report, got %d", len(epics))
	}
}

func TestWaveForSeverity(t *testing.T) {
	cases := []struct {
		severity string
		want     int
	}{
		{"critical", 0},
		{"CRITICAL", 0},
		{"high", 1},
		{"High", 1},
		{"medium", 2},
		{"low", 3},
		{"info", 3},
		{"unknown", 3},
		{"", 3},
	}

	for _, tc := range cases {
		got := waveForSeverity(tc.severity)
		if got != tc.want {
			t.Errorf("waveForSeverity(%q) = %d, want %d", tc.severity, got, tc.want)
		}
	}
}

func TestGenerateEpicTitle(t *testing.T) {
	title := GenerateEpicTitle("Dimension 1: API Alignment", "repo-alpha")
	expected := "Dimension 1: API Alignment (repo-alpha)"
	if title != expected {
		t.Errorf("GenerateEpicTitle = %q, want %q", title, expected)
	}
}

func TestGenerateEpicTitleTruncates(t *testing.T) {
	longDim := strings.Repeat("X", 150)
	longRepo := strings.Repeat("Y", 100)
	title := GenerateEpicTitle(longDim, longRepo)
	if len(title) > 200 {
		t.Errorf("expected title truncated to 200 chars, got %d", len(title))
	}
}

func TestGenerateSubIssueTitle(t *testing.T) {
	f := &AuditFinding{
		Category:    "API_MISMATCH",
		Description: "Endpoint /foo is missing",
	}
	title := GenerateSubIssueTitle(f)
	if title != "[API_MISMATCH] Endpoint /foo is missing" {
		t.Errorf("unexpected sub-issue title: %q", title)
	}
}

func TestGenerateSubIssueTitleTruncates(t *testing.T) {
	f := &AuditFinding{
		Category:    "CAT",
		Description: strings.Repeat("A", 300),
	}
	title := GenerateSubIssueTitle(f)
	if len(title) > 200 {
		t.Errorf("expected sub-issue title truncated to 200 chars, got %d", len(title))
	}
}

func TestGenerateSubIssueBody(t *testing.T) {
	f := &AuditFinding{
		Description:        "Something is broken",
		AcceptanceCriteria: []string{"Fix the thing", "Add a test"},
	}
	body := GenerateSubIssueBody(f, 2)

	if !strings.Contains(body, "<!-- wave: 2 -->") {
		t.Errorf("body missing wave annotation, got:\n%s", body)
	}
	if !strings.Contains(body, "## Acceptance Criteria") {
		t.Errorf("body missing Acceptance Criteria section")
	}
	if !strings.Contains(body, "Fix the thing") {
		t.Errorf("body missing first acceptance criterion")
	}
	if !strings.Contains(body, "Add a test") {
		t.Errorf("body missing second acceptance criterion")
	}
}

func TestGenerateSubIssueBodyDefaultCriteria(t *testing.T) {
	f := &AuditFinding{
		Description:        "Some finding",
		AcceptanceCriteria: nil,
	}
	body := GenerateSubIssueBody(f, 1)
	if !strings.Contains(body, "Resolve finding and verify fix") {
		t.Errorf("expected default acceptance criterion in body, got:\n%s", body)
	}
}

func TestRunIssueCreation_DryRun(t *testing.T) {
	dim := &DimensionResult{
		Name: "Dimension 1: API Alignment",
		Findings: []AuditFinding{
			makeFinding("f1", "CAT_A", "repo-alpha", "high"),
		},
	}
	report := makeReport([]*DimensionResult{dim})

	cfg := IssueCreatorConfig{
		Owner:         "test-owner",
		Repo:          "repo-alpha",
		ProjectNumber: 42,
		EpicLabel:     "type:epic",
		DryRun:        true,
	}

	mock := newMockIssueCreator()
	result, err := RunIssueCreation(context.Background(), report, cfg, mock)
	if err != nil {
		t.Fatalf("RunIssueCreation returned error: %v", err)
	}

	// In dry-run mode no mutation methods should be called.
	if mock.createCalls != 0 {
		t.Errorf("expected 0 CreateIssueWithID calls in dry-run, got %d", mock.createCalls)
	}
	if mock.addSubIssueCalls != 0 {
		t.Errorf("expected 0 AddSubIssue calls in dry-run, got %d", mock.addSubIssueCalls)
	}
	if mock.addToBoardCalls != 0 {
		t.Errorf("expected 0 AddToProjectBoard calls in dry-run, got %d", mock.addToBoardCalls)
	}
	if mock.setStatusCalls != 0 {
		t.Errorf("expected 0 SetProjectItemStatus calls in dry-run, got %d", mock.setStatusCalls)
	}
	if mock.addBlockedByCalls != 0 {
		t.Errorf("expected 0 AddBlockedBy calls in dry-run, got %d", mock.addBlockedByCalls)
	}

	// Counts should all be zero since nothing was actually created.
	if result.EpicsCreated != 0 {
		t.Errorf("expected 0 epics created in dry-run, got %d", result.EpicsCreated)
	}
	if result.IssuesCreated != 0 {
		t.Errorf("expected 0 issues created in dry-run, got %d", result.IssuesCreated)
	}
}
