package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// linkCall records one two-ended issue link exactly as the creator issued it.
// Both ends are kept because each carries its own owner/repo: a link is only
// correct if BOTH refs name the right repository, so recording a single
// identifier per end (as the old node-ID mock did) could not tell a
// same-repo link from a cross-repo one.
type linkCall struct {
	from forge.IssueRef // epic for sub-issue links, blocked issue for blockedBy
	to   forge.IssueRef // sub-issue for sub-issue links, blocker for blockedBy
}

// String renders the call the way an operator would read it, so a failed
// assertion prints "owner/repo#1 -> owner/other#2".
func (c linkCall) String() string { return c.from.String() + " -> " + c.to.String() }

// mockIssueCreator records calls — used to verify dry-run skips all mutations
// and that live runs address both ends of every link by the right ref.
type mockIssueCreator struct {
	createCalls       int
	addSubIssueCalls  int
	addBlockedByCalls int
	addToBoardCalls   int
	setStatusCalls    int

	subIssueLinks  []linkCall // epic -> sub-issue
	blockedByLinks []linkCall // blocked -> blocker

	// createdByTitle maps an issue title to the ref the mock handed back, so
	// assertions can name issues by title instead of hard-coding the numbers
	// that fall out of creation order.
	createdByTitle map[string]forge.IssueRef

	searchResults map[string]struct {
		number int
		nodeID string
		found  bool
	}
}

func newMockIssueCreator() *mockIssueCreator {
	return &mockIssueCreator{
		createdByTitle: make(map[string]forge.IssueRef),
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

// CreateIssueWithID hands back a realistic, non-zero issue number. The number
// is load-bearing now: it becomes the IssueRef the link calls address, and a
// zero would make the creator skip the link entirely.
func (m *mockIssueCreator) CreateIssueWithID(_ context.Context, owner, repo, title, _ string, _ []string) (string, int, error) {
	m.createCalls++
	number := 100 + m.createCalls
	m.createdByTitle[title] = forge.IssueRef{Owner: owner, Repo: repo, Number: number}
	return fmt.Sprintf("node-%d", number), number, nil
}

func (m *mockIssueCreator) AddSubIssue(_ context.Context, parent, child forge.IssueRef) error {
	m.addSubIssueCalls++
	m.subIssueLinks = append(m.subIssueLinks, linkCall{from: parent, to: child})
	return nil
}

func (m *mockIssueCreator) AddBlockedBy(_ context.Context, blocked, blocker forge.IssueRef) error {
	m.addBlockedByCalls++
	m.blockedByLinks = append(m.blockedByLinks, linkCall{from: blocked, to: blocker})
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

// refForTitle returns the ref the mock assigned to a created issue, failing
// the test if that issue was never created.
func (m *mockIssueCreator) refForTitle(t *testing.T, title string) forge.IssueRef {
	t.Helper()
	ref, ok := m.createdByTitle[title]
	if !ok {
		t.Fatalf("no issue was created with title %q; created: %v", title, m.createdByTitle)
	}
	return ref
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

func TestRunIssueCreation_LinksSubIssuesByRef(t *testing.T) {
	dim := &DimensionResult{
		Name: "Dimension 1: API Alignment",
		Findings: []AuditFinding{
			makeFinding("f1", "CAT_A", "repo-alpha", "high"),
		},
	}
	report := makeReport([]*DimensionResult{dim})

	cfg := IssueCreatorConfig{
		Owner: "test-owner",
		Repo:  "repo-alpha",
	}

	mock := newMockIssueCreator()
	result, err := RunIssueCreation(context.Background(), report, cfg, mock)
	if err != nil {
		t.Fatalf("RunIssueCreation returned error: %v", err)
	}
	if result.EpicsCreated != 1 || result.IssuesCreated != 1 {
		t.Fatalf("expected 1 epic + 1 sub-issue created, got %d + %d", result.EpicsCreated, result.IssuesCreated)
	}

	epicRef := mock.refForTitle(t, GenerateEpicTitle(dim.Name, "repo-alpha"))
	subRef := mock.refForTitle(t, GenerateSubIssueTitle(&dim.Findings[0]))

	if len(mock.subIssueLinks) != 1 {
		t.Fatalf("expected 1 AddSubIssue call, got %d: %v", len(mock.subIssueLinks), mock.subIssueLinks)
	}
	got := mock.subIssueLinks[0]
	want := linkCall{from: epicRef, to: subRef}
	if got != want {
		t.Errorf("AddSubIssue linked %s, want %s", got, want)
	}
	// The link must address real issue numbers, not the zero value that would
	// have made the creator skip the call altogether.
	if got.from.Number == 0 || got.to.Number == 0 {
		t.Errorf("AddSubIssue was handed a zero issue number: %s", got)
	}
}

// TestRunIssueCreation_BlockedByCrossesRepositories is the reason both ends of
// the link carry their own owner/repo. The blockedBy pass walks every
// dimension, so a finding filed in one repository can be blocked by a finding
// filed in another — a single shared owner/repo pair would silently link
// within whichever one the caller named.
func TestRunIssueCreation_BlockedByCrossesRepositories(t *testing.T) {
	blocker := makeFinding("f1", "CAT_A", "repo-alpha", "high")

	blocked := makeFinding("f2", "CAT_B", "repo-beta", "medium")
	blocked.BlockedBy = []string{"f1"}

	dim1 := &DimensionResult{Name: "Dimension 1: API Alignment", Findings: []AuditFinding{blocker}}
	dim2 := &DimensionResult{Name: "Dimension 2: Lifecycle", Findings: []AuditFinding{blocked}}
	report := makeReport([]*DimensionResult{dim1, dim2})

	cfg := IssueCreatorConfig{
		Owner: "test-owner",
		Repo:  "repo-alpha", // default repo; repo-beta must come from the finding
	}

	mock := newMockIssueCreator()
	result, err := RunIssueCreation(context.Background(), report, cfg, mock)
	if err != nil {
		t.Fatalf("RunIssueCreation returned error: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	if result.BlockedByAdded != 1 {
		t.Fatalf("expected 1 blockedBy edge, got %d", result.BlockedByAdded)
	}

	blockerRef := mock.refForTitle(t, GenerateSubIssueTitle(&blocker))
	blockedRef := mock.refForTitle(t, GenerateSubIssueTitle(&blocked))

	if len(mock.blockedByLinks) != 1 {
		t.Fatalf("expected 1 AddBlockedBy call, got %d: %v", len(mock.blockedByLinks), mock.blockedByLinks)
	}
	got := mock.blockedByLinks[0]
	want := linkCall{from: blockedRef, to: blockerRef}
	if got != want {
		t.Errorf("AddBlockedBy linked %s, want %s", got, want)
	}
	// The whole point: the two ends name different repositories.
	if got.from.Repo != "repo-beta" {
		t.Errorf("blocked end resolved to repo %q, want repo-beta", got.from.Repo)
	}
	if got.to.Repo != "repo-alpha" {
		t.Errorf("blocker end resolved to repo %q, want repo-alpha", got.to.Repo)
	}
	if got.from.Repo == got.to.Repo {
		t.Errorf("both ends resolved to the same repository (%s); cross-repo linking is not being exercised", got.from.Repo)
	}
}

// TestRunIssueCreation_ExistingIssuesLinkByFoundNumber pins that the number
// SearchOpenIssueByTitle reports for an already-open issue is what the link
// call addresses. Before refs, that number was discarded and the node ID
// carried the identity; now a dropped number would silently skip the link.
func TestRunIssueCreation_ExistingIssuesLinkByFoundNumber(t *testing.T) {
	blocker := makeFinding("f1", "CAT_A", "repo-alpha", "high")
	blocked := makeFinding("f2", "CAT_B", "repo-alpha", "medium")
	blocked.BlockedBy = []string{"f1"}

	dim := &DimensionResult{Name: "Dimension 1: API Alignment", Findings: []AuditFinding{blocker, blocked}}
	report := makeReport([]*DimensionResult{dim})

	cfg := IssueCreatorConfig{Owner: "test-owner", Repo: "repo-alpha"}

	mock := newMockIssueCreator()
	type searchResult = struct {
		number int
		nodeID string
		found  bool
	}
	mock.searchResults[GenerateSubIssueTitle(&blocker)] = searchResult{number: 501, nodeID: "node-501", found: true}
	mock.searchResults[GenerateSubIssueTitle(&blocked)] = searchResult{number: 502, nodeID: "node-502", found: true}

	result, err := RunIssueCreation(context.Background(), report, cfg, mock)
	if err != nil {
		t.Fatalf("RunIssueCreation returned error: %v", err)
	}
	if result.IssuesSkipped != 2 {
		t.Fatalf("expected 2 skipped (already-open) sub-issues, got %d", result.IssuesSkipped)
	}

	if len(mock.blockedByLinks) != 1 {
		t.Fatalf("expected 1 AddBlockedBy call, got %d: %v", len(mock.blockedByLinks), mock.blockedByLinks)
	}
	got := mock.blockedByLinks[0]
	want := linkCall{
		from: forge.IssueRef{Owner: "test-owner", Repo: "repo-alpha", Number: 502},
		to:   forge.IssueRef{Owner: "test-owner", Repo: "repo-alpha", Number: 501},
	}
	if got != want {
		t.Errorf("AddBlockedBy linked %s, want %s", got, want)
	}
}
