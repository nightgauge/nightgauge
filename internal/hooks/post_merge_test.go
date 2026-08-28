package hooks

import (
	"context"
	"fmt"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockIssueCloser implements IssueCloser for testing.
type mockIssueCloser struct {
	err    error
	called bool
	nodeID string
}

func (m *mockIssueCloser) CloseIssue(_ context.Context, issueID string) error {
	m.called = true
	m.nodeID = issueID
	return m.err
}

// mockEpicAutoCloser implements EpicAutoCloser for testing.
type mockEpicAutoCloser struct {
	result     *gh.AutoCloseSingleResult
	err        error
	called     bool
	calledWith struct {
		owner         string
		repo          string
		epicNumber    int
		projectNumber int
	}

	orphanResult     *gh.OrphanCloseResult
	orphanErr        error
	orphanCalled     bool
	orphanCalledWith int
}

func (m *mockEpicAutoCloser) AutoCloseSingle(ctx context.Context, owner, repo string, epicNumber, projectNumber int) (*gh.AutoCloseSingleResult, error) {
	m.called = true
	m.calledWith.owner = owner
	m.calledWith.repo = repo
	m.calledWith.epicNumber = epicNumber
	m.calledWith.projectNumber = projectNumber
	return m.result, m.err
}

// orphanResult/orphanErr drive CloseOrphanSubs; orphanCalled records invocation.
func (m *mockEpicAutoCloser) CloseOrphanSubs(_ context.Context, _, _ string, epicNumber, _ int, _ ...gh.OwnerType) (*gh.OrphanCloseResult, error) {
	m.orphanCalled = true
	m.orphanCalledWith = epicNumber
	if m.orphanResult == nil && m.orphanErr == nil {
		return &gh.OrphanCloseResult{EpicNumber: epicNumber, Guard: "no_orphans"}, nil
	}
	return m.orphanResult, m.orphanErr
}

// mockBoardSyncer is a fake project board, not a call recorder: it models
// whether an issue HAS a row, because that is the distinction #691 turns on.
// A row-less issue fails SyncStatus with ErrIssueNotOnBoard exactly as
// *github.ProjectService does, so the repair path is exercised end to end
// rather than asserted about.
//
// `rows` nil means "every issue is already on the board" -- the shape every
// pre-#691 test assumed.
type mockBoardSyncer struct {
	rows       map[int]bool
	err        error // forced SyncStatus failure, e.g. a network error
	addErr     error // forced AddIssueByNumber failure
	adds       []int
	called     bool
	syncs      int
	calledWith struct {
		number int
		status string
	}
}

func (m *mockBoardSyncer) onBoard(number int) bool {
	if m.rows == nil {
		return true
	}
	return m.rows[number]
}

func (m *mockBoardSyncer) SyncStatus(_ context.Context, _, _ string, issueNumber int, status string) error {
	m.called = true
	m.syncs++
	m.calledWith.number = issueNumber
	m.calledWith.status = status
	if m.err != nil {
		return m.err
	}
	if !m.onBoard(issueNumber) {
		// Wrapped the same way ProjectService.findItemID wraps it, so a test
		// that stopped matching the real error would fail here too.
		return fmt.Errorf("%w: issue #%d", gh.ErrIssueNotOnBoard, issueNumber)
	}
	return nil
}

func (m *mockBoardSyncer) AddIssueByNumber(_ context.Context, _, _ string, number int) (string, error) {
	m.adds = append(m.adds, number)
	if m.addErr != nil {
		return "", m.addErr
	}
	if m.rows == nil {
		m.rows = map[int]bool{}
	}
	m.rows[number] = true
	return "PVTI_fake", nil
}

func TestPostMergeIssueIsClosedAfterMerge(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			NodeID:            "I_node100",
			Number:            100,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     100,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if !issueCloser.called {
		t.Error("expected CloseIssue to be called")
	}
	if issueCloser.nodeID != "I_node100" {
		t.Errorf("expected CloseIssue called with node ID %q, got %q", "I_node100", issueCloser.nodeID)
	}
	if !result.IssueClosed {
		t.Error("expected IssueClosed=true after successful close")
	}
}

func TestPostMergeIssueCloseFailsNonBlocking(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			NodeID:            "I_node100",
			Number:            100,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{err: fmt.Errorf("network timeout")}
	epicCloser := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     100,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	// Non-blocking: failure to close must not prevent the hook from completing
	if result.IssueClosed {
		t.Error("expected IssueClosed=false when CloseIssue fails")
	}
	// Error must not be propagated as a hard failure
	if result.Error != "" {
		t.Errorf("expected Error to be empty for non-blocking close failure, got %q", result.Error)
	}
}

func TestPostMergeParentEpicAutoClosesSuccessfully(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#100": {
			NodeID:            "I_node100",
			Number:            100,
			ParentIssueNumber: 99,
		},
	}}
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "closed", Reason: "all_closed"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     100,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   0,
	})

	if !result.IssueClosed {
		t.Error("expected IssueClosed=true")
	}
	if !result.AutoClosed {
		t.Errorf("expected AutoClosed=true, got false")
	}
	if result.EpicNumber != 99 {
		t.Errorf("expected EpicNumber=99, got %d", result.EpicNumber)
	}
	if result.Reason != "closed" {
		t.Errorf("expected Reason=closed, got %q", result.Reason)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
	if !closer.called {
		t.Error("expected AutoCloseSingle to be called")
	}
	if closer.calledWith.epicNumber != 99 {
		t.Errorf("expected AutoCloseSingle called with epicNumber=99, got %d", closer.calledWith.epicNumber)
	}
}

func TestPostMergeNoParentEpic(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#50": {
			NodeID:            "I_node50",
			Number:            50,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     50,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if result.AutoClosed {
		t.Error("expected AutoClosed=false for issue with no parent")
	}
	if result.Reason != "no_parent" {
		t.Errorf("expected Reason=no_parent, got %q", result.Reason)
	}
	if closer.called {
		t.Error("expected AutoCloseSingle NOT to be called when issue has no parent epic")
	}
	// Issue itself should still be closed even without a parent epic
	if !issueCloser.called {
		t.Error("expected CloseIssue to be called even when issue has no parent epic")
	}
	if !result.IssueClosed {
		t.Error("expected IssueClosed=true even when issue has no parent epic")
	}
}

func TestPostMergeAutoCloseFailsNonBlocking(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#101": {
			NodeID:            "I_node101",
			Number:            101,
			ParentIssueNumber: 99,
		},
	}}
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{
		err: fmt.Errorf("context deadline exceeded"),
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     101,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	// Non-blocking: hook must not return an error even when auto-close fails
	if result.AutoClosed {
		t.Error("expected AutoClosed=false on error")
	}
	if result.Reason != "auto_close_error" {
		t.Errorf("expected Reason=auto_close_error, got %q", result.Reason)
	}
	if result.Error == "" {
		t.Error("expected Error field to contain error message")
	}
	if result.EpicNumber != 99 {
		t.Errorf("expected EpicNumber=99, got %d", result.EpicNumber)
	}
	// Issue close should still have succeeded even when epic auto-close fails
	if !result.IssueClosed {
		t.Error("expected IssueClosed=true even when epic auto-close fails")
	}
}

func TestPostMergeGetIssueFails(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{}} // empty — all lookups fail
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     999,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if result.AutoClosed {
		t.Error("expected AutoClosed=false when issue fetch fails")
	}
	if result.IssueClosed {
		t.Error("expected IssueClosed=false when issue fetch fails (no node ID available)")
	}
	if result.Reason != "issue_fetch_error" {
		t.Errorf("expected Reason=issue_fetch_error, got %q", result.Reason)
	}
	if result.Error == "" {
		t.Error("expected Error field to be populated")
	}
	if closer.called {
		t.Error("expected AutoCloseSingle NOT to be called when issue fetch fails")
	}
	if issueCloser.called {
		t.Error("expected CloseIssue NOT to be called when issue fetch fails")
	}
}

func TestPostMergeProjectNumberOptional(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#200": {
			NodeID:            "I_node200",
			Number:            200,
			ParentIssueNumber: 99,
		},
	}}
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "closed", Reason: "all_closed"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     200,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   0, // Optional — zero means no project board sync
	})

	if !result.AutoClosed {
		t.Errorf("expected AutoClosed=true, got false")
	}
	if closer.calledWith.projectNumber != 0 {
		t.Errorf("expected AutoCloseSingle called with projectNumber=0, got %d", closer.calledWith.projectNumber)
	}
}

func TestPostMergeEpicAlreadyClosed(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#102": {
			NodeID:            "I_node102",
			Number:            102,
			ParentIssueNumber: 99,
		},
	}}
	issueCloser := &mockIssueCloser{}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "skipped", Reason: "already_closed"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, closer, nil, nil, PostMergeInput{
		IssueNumber:     102,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if result.AutoClosed {
		t.Error("expected AutoClosed=false when epic is already closed")
	}
	if result.Reason != "skipped" {
		t.Errorf("expected Reason=skipped, got %q", result.Reason)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
}

// TestPostMergeAutonomousPathFiresEpicClose simulates the wave orchestrator
// path: the sub-issue (#200) has a parent epic (#99), so EvaluatePostMerge
// must call AutoCloseSingle with the correct epic number. This mirrors what
// checkEpicCompletion does in internal/orchestrator/epic.go after a pipeline
// completes via the autonomous scheduler.
func TestPostMergeAutonomousPathFiresEpicClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#200": {
			NodeID:            "I_node200",
			Number:            200,
			ParentIssueNumber: 99,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "closed", Reason: "all_closed"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     200,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   42,
	})

	if !issueCloser.called {
		t.Error("expected CloseIssue to be called for the sub-issue")
	}
	if !epicCloser.called {
		t.Error("expected AutoCloseSingle to be called for the parent epic")
	}
	if epicCloser.calledWith.epicNumber != 99 {
		t.Errorf("AutoCloseSingle called with epicNumber=%d, want 99", epicCloser.calledWith.epicNumber)
	}
	if epicCloser.calledWith.projectNumber != 42 {
		t.Errorf("AutoCloseSingle called with projectNumber=%d, want 42", epicCloser.calledWith.projectNumber)
	}
	if !result.AutoClosed {
		t.Error("expected AutoClosed=true when epic closes successfully")
	}
	if result.EpicNumber != 99 {
		t.Errorf("expected EpicNumber=99, got %d", result.EpicNumber)
	}
}

// TestPostMergeAutonomousPathNoParentSkipsEpicClose ensures that when the
// sub-issue has no parent (ParentIssueNumber=0), AutoCloseSingle is NOT called.
// This is the common case for standalone issues dispatched by the scheduler.
func TestPostMergeAutonomousPathNoParentSkipsEpicClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#300": {
			NodeID:            "I_node300",
			Number:            300,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     300,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if epicCloser.called {
		t.Error("AutoCloseSingle must NOT be called when sub-issue has no parent epic")
	}
	if result.AutoClosed {
		t.Error("AutoClosed must be false for standalone issues")
	}
	if result.Reason != "no_parent" {
		t.Errorf("expected Reason=no_parent, got %q", result.Reason)
	}
}

// mockPRVerifier implements PRVerifier for testing.
type mockPRVerifier struct {
	state string
	err   error
}

func (m *mockPRVerifier) GetPRState(_ context.Context, _, _ string, _ int) (string, error) {
	return m.state, m.err
}

// mockPRVerifierWithMerge also implements PRMergeInfoFetcher (#4133) so it can
// drive the post-merge ground-truth breadcrumb capture. infoErr simulates a
// best-effort fetch failure; mergeInfoCalled records whether the capture ran.
type mockPRVerifierWithMerge struct {
	state           string
	err             error
	sha             string
	mergedAt        string
	infoErr         error
	mergeInfoCalled bool
}

func (m *mockPRVerifierWithMerge) GetPRState(_ context.Context, _, _ string, _ int) (string, error) {
	return m.state, m.err
}

func (m *mockPRVerifierWithMerge) GetPRMergeInfo(_ context.Context, _, _ string, _ int) (string, string, error) {
	m.mergeInfoCalled = true
	if m.infoErr != nil {
		return "", "", m.infoErr
	}
	return m.sha, m.mergedAt, nil
}

func TestPostMergeCapturesMergeBreadcrumb(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#410": {NodeID: "I_node410", Number: 410},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifierWithMerge{
		state:    "MERGED",
		sha:      "abc123def456",
		mergedAt: "2026-06-26T12:00:00Z",
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     410,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        130,
	})

	if !verifier.mergeInfoCalled {
		t.Error("GetPRMergeInfo must be called when the verifier supports it")
	}
	if result.MergedCommitSha != "abc123def456" {
		t.Errorf("MergedCommitSha = %q, want abc123def456", result.MergedCommitSha)
	}
	if result.MergedAt != "2026-06-26T12:00:00Z" {
		t.Errorf("MergedAt = %q, want 2026-06-26T12:00:00Z", result.MergedAt)
	}
	if !result.IssueClosed {
		t.Error("issue must still be closed alongside the breadcrumb capture")
	}
	// (#4151) A single-issue merge with a captured breadcrumb is survival-eligible.
	if !result.SurvivalEligible {
		t.Error("single-issue merge with a captured breadcrumb must be survival-eligible")
	}
}

func TestPostMergeMergeInfoFetchErrorIsNonBlocking(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#411": {NodeID: "I_node411", Number: 411},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifierWithMerge{
		state:   "MERGED",
		infoErr: fmt.Errorf("forge timeout"),
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     411,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        131,
	})

	if result.MergedCommitSha != "" || result.MergedAt != "" {
		t.Errorf("breadcrumb must be empty on fetch error, got sha=%q at=%q", result.MergedCommitSha, result.MergedAt)
	}
	// (#4151) No breadcrumb → not survival-eligible.
	if result.SurvivalEligible {
		t.Error("survival must not be eligible when the breadcrumb is empty")
	}
	// Non-blocking: the issue-close path still runs unchanged.
	if !issueCloser.called {
		t.Error("CloseIssue must still run when the breadcrumb fetch fails")
	}
	if !result.IssueClosed {
		t.Error("IssueClosed must be true — breadcrumb failure must not block the merge path")
	}
}

func TestPostMergeBreadcrumbSkippedWithoutFetcher(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#412": {NodeID: "I_node412", Number: 412},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	// Plain mockPRVerifier does NOT implement PRMergeInfoFetcher.
	verifier := &mockPRVerifier{state: "MERGED"}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     412,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        132,
	})

	if result.MergedCommitSha != "" || result.MergedAt != "" {
		t.Errorf("breadcrumb must be empty when verifier lacks merge-info support, got sha=%q at=%q", result.MergedCommitSha, result.MergedAt)
	}
	if !result.IssueClosed {
		t.Error("issue must still be closed without a merge-info fetcher")
	}
}

func TestPostMergePRNotMergedSkipsIssueClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#400": {
			NodeID: "I_node400",
			Number: 400,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifier{state: "OPEN"}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     400,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        123,
	})

	if issueCloser.called {
		t.Error("CloseIssue must NOT be called when PR is not MERGED")
	}
	if result.IssueClosed {
		t.Error("IssueClosed must be false when PR guard fires")
	}
	if result.Reason != "pr_not_merged" {
		t.Errorf("Reason = %q, want pr_not_merged", result.Reason)
	}
	if result.Error == "" {
		t.Error("Error must be populated with PR state")
	}
}

func TestPostMergePRMergedAllowsIssueClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#401": {
			NodeID:            "I_node401",
			Number:            401,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifier{state: "MERGED"}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     401,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        124,
	})

	if !issueCloser.called {
		t.Error("CloseIssue must be called when PR is MERGED")
	}
	if !result.IssueClosed {
		t.Error("IssueClosed must be true when PR is MERGED and close succeeds")
	}
}

func TestPostMergePRVerifyErrorSkipsIssueClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#402": {
			NodeID: "I_node402",
			Number: 402,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifier{err: fmt.Errorf("network timeout")}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     402,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        125,
	})

	if issueCloser.called {
		t.Error("CloseIssue must NOT be called when PR state verification fails")
	}
	if result.Reason != "pr_verify_error" {
		t.Errorf("Reason = %q, want pr_verify_error", result.Reason)
	}
	if result.Error == "" {
		t.Error("Error must be populated with verification error message")
	}
}

func TestPostMergeNilVerifierSkipsGuard(t *testing.T) {
	// When prVerifier is nil but PRNumber is set, the guard must be skipped
	// (nil verifier = no GitHub client = cannot verify = fall through).
	// This ensures backward compatibility for callers that cannot wire a verifier.
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#403": {
			NodeID:            "I_node403",
			Number:            403,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     403,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        126,
	})

	if !issueCloser.called {
		t.Error("CloseIssue must be called when prVerifier is nil (guard skipped)")
	}
	if !result.IssueClosed {
		t.Error("IssueClosed must be true when guard is skipped and close succeeds")
	}
}

func TestPostMergeZeroPRNumberSkipsGuard(t *testing.T) {
	// When PRNumber is 0 the guard is skipped regardless of whether a verifier is present.
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#404": {
			NodeID:            "I_node404",
			Number:            404,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	// Verifier that would fail if called
	verifier := &mockPRVerifier{err: fmt.Errorf("should not be called")}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     404,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        0, // zero — guard must be skipped
	})

	if !issueCloser.called {
		t.Error("CloseIssue must be called when PRNumber=0 (guard skipped)")
	}
	if !result.IssueClosed {
		t.Error("IssueClosed must be true when guard is skipped")
	}
}

// --- Post-merge fan-out: board Status sync (#3981) + orphan-sub close (#3979) ---

func TestPostMergeSyncsClosedIssueToDone(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#500": {
			NodeID:            "I_node500",
			Number:            500,
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{}
	epicCloser := &mockEpicAutoCloser{}
	board := &mockBoardSyncer{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, board, PostMergeInput{
		IssueNumber:     500,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
	})

	if !board.called {
		t.Fatal("expected board SyncStatus to be called for a closed issue with a project")
	}
	if board.calledWith.number != 500 || board.calledWith.status != "Done" {
		t.Errorf("SyncStatus called with (#%d, %q), want (#500, Done)", board.calledWith.number, board.calledWith.status)
	}
	if result.IssueDoneSync != BoardSyncSynced {
		t.Errorf("IssueDoneSync = %q, want %q", result.IssueDoneSync, BoardSyncSynced)
	}
}

func TestPostMergeBoardSyncSkippedWithoutProject(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#501": {NodeID: "I_node501", Number: 501},
	}}
	board := &mockBoardSyncer{}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
		IssueNumber:     501,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   0, // no project → no board sync
	})

	if board.called {
		t.Error("board SyncStatus must NOT be called when ProjectNumber=0")
	}
	if result.IssueDoneSync != BoardSyncNotAttempted {
		t.Errorf("IssueDoneSync = %q, want %q when no project is configured", result.IssueDoneSync, BoardSyncNotAttempted)
	}
}

func TestPostMergeEpicUmbrellaClosesOrphanSubs(t *testing.T) {
	// The merged issue is itself an epic (umbrella PR). The hook must call
	// CloseOrphanSubs and surface the count.
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#85": {
			NodeID:            "I_node85",
			Number:            85,
			ParentIssueNumber: 0,
			IsEpic:            true,
		},
	}}
	epicCloser := &mockEpicAutoCloser{
		orphanResult: &gh.OrphanCloseResult{EpicNumber: 85, Guard: "completed", Closed: 3},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     85,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if !epicCloser.orphanCalled {
		t.Fatal("expected CloseOrphanSubs to be called when the merged issue is an epic")
	}
	if epicCloser.orphanCalledWith != 85 {
		t.Errorf("CloseOrphanSubs called with epic #%d, want #85", epicCloser.orphanCalledWith)
	}
	if result.OrphanSubsClosed != 3 {
		t.Errorf("OrphanSubsClosed = %d, want 3", result.OrphanSubsClosed)
	}
}

func TestPostMergeNonEpicSkipsOrphanClose(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {
			NodeID:            "I_node600",
			Number:            600,
			ParentIssueNumber: 0,
			IsEpic:            false,
		},
	}}
	epicCloser := &mockEpicAutoCloser{}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, epicCloser, nil, nil, PostMergeInput{
		IssueNumber:     600,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if epicCloser.orphanCalled {
		t.Error("CloseOrphanSubs must NOT be called when the merged issue is not an epic")
	}
	if result.OrphanSubsClosed != 0 {
		t.Errorf("OrphanSubsClosed = %d, want 0", result.OrphanSubsClosed)
	}
}

// --- Board sync / issue-closed race (#686) ---
//
// GitHub's Closes-keyword auto-close and this hook's own CloseIssue call race
// to close the same issue. GetIssue (called before CloseIssue) captures
// issue.State as of fetch time; when the keyword mechanism already won, that
// pre-fetched State already reads CLOSED and the hook's own CloseIssue call
// then errors (issues.go has no already-closed special case — see
// mockIssueCloser err below simulating that GraphQL error). The board-Done
// sync and SurvivalEligible must both key off "is the issue actually
// closed", not "did this hook's own CloseIssue call succeed".

// TestPostMergeBoardSyncFiresWhenClosedByKeywordRace reproduces the actual
// production race (#686): the merged PR body contained a Closes-keyword and
// GitHub closed the issue as part of the merge, before this hook ran. The
// pre-fetched issue.State already reads CLOSED, and the hook's own
// CloseIssue call errors (racing against — and losing to — the already-
// applied close). The board-Done sync must still fire.
func TestPostMergeBoardSyncFiresWhenClosedByKeywordRace(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#700": {
			NodeID:            "I_node700",
			Number:            700,
			State:             "CLOSED", // already closed via Closes-keyword before the hook ran
			ParentIssueNumber: 0,
		},
	}}
	issueCloser := &mockIssueCloser{err: fmt.Errorf("GraphQL: Validation Failed: Could not close issue: already closed")}
	epicCloser := &mockEpicAutoCloser{}
	board := &mockBoardSyncer{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, board, PostMergeInput{
		IssueNumber:     700,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
	})

	if !board.called {
		t.Fatal("expected board SyncStatus to fire when the issue is already closed (Closes-keyword race), even though the hook's own CloseIssue call errored")
	}
	if board.calledWith.number != 700 || board.calledWith.status != "Done" {
		t.Errorf("SyncStatus called with (#%d, %q), want (#700, Done)", board.calledWith.number, board.calledWith.status)
	}
	if result.IssueDoneSync != BoardSyncSynced {
		t.Errorf("IssueDoneSync = %q, want %q for the closed-by-keyword race case", result.IssueDoneSync, BoardSyncSynced)
	}
	if !result.IssueClosed {
		t.Error("expected IssueClosed=true — the issue IS closed, regardless of who closed it")
	}
}

// TestPostMergeSurvivalEligibleWhenClosedByKeywordRace extends the same race
// to SurvivalEligible: a single-issue merge that closed via the keyword race
// (this hook's own CloseIssue call errors) must still be eligible to seed a
// survival record when a merge breadcrumb was captured.
func TestPostMergeSurvivalEligibleWhenClosedByKeywordRace(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#702": {
			NodeID: "I_node702",
			Number: 702,
			State:  "CLOSED",
		},
	}}
	issueCloser := &mockIssueCloser{err: fmt.Errorf("already closed")}
	epicCloser := &mockEpicAutoCloser{}
	verifier := &mockPRVerifierWithMerge{
		state:    "MERGED",
		sha:      "racesha",
		mergedAt: "2026-06-26T12:00:00Z",
	}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     702,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        150,
	})

	if !result.SurvivalEligible {
		t.Error("expected SurvivalEligible=true when the issue closed via the keyword race, breadcrumb captured, non-epic merge")
	}
}

// TestPostMergeBoardSyncFiresWhenHookClosesIssueItself preserves the
// pre-existing behaviour for the other arm: the issue is genuinely OPEN at
// fetch time (GitHub's keyword mechanism did NOT fire) and this hook's own
// CloseIssue call is what closes it. The board sync must still fire.
func TestPostMergeBoardSyncFiresWhenHookClosesIssueItself(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#703": {
			NodeID: "I_node703",
			Number: 703,
			State:  "OPEN", // genuinely open before the hook runs
		},
	}}
	issueCloser := &mockIssueCloser{} // succeeds
	epicCloser := &mockEpicAutoCloser{}
	board := &mockBoardSyncer{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, board, PostMergeInput{
		IssueNumber:     703,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
	})

	if !board.called {
		t.Fatal("expected board SyncStatus to fire when the hook's own CloseIssue call closes the issue")
	}
	if !result.IssueClosed {
		t.Error("expected IssueClosed=true when the hook's own close call succeeds")
	}
}

// TestPostMergeBoardSyncSkippedWhenIssueGenuinelyOpenAndCloseFails preserves
// the sane, documented behaviour for a real failure: the issue is genuinely
// still OPEN at fetch time AND the close attempt genuinely fails (e.g. a
// network timeout, not an already-closed race). The board sync must NOT
// fire — there is nothing to reconcile because the issue never closed.
func TestPostMergeBoardSyncSkippedWhenIssueGenuinelyOpenAndCloseFails(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#701": {
			NodeID: "I_node701",
			Number: 701,
			State:  "OPEN",
		},
	}}
	issueCloser := &mockIssueCloser{err: fmt.Errorf("network timeout")}
	epicCloser := &mockEpicAutoCloser{}
	board := &mockBoardSyncer{}

	result := EvaluatePostMerge(context.Background(), fetcher, issueCloser, epicCloser, nil, board, PostMergeInput{
		IssueNumber:     701,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
	})

	if board.called {
		t.Error("board SyncStatus must NOT fire when the issue is genuinely still open and the close attempt genuinely failed")
	}
	if result.IssueDoneSync != BoardSyncNotAttempted {
		t.Errorf("IssueDoneSync = %q, want %q — the issue never closed, so there was nothing to sync",
			result.IssueDoneSync, BoardSyncNotAttempted)
	}
	if result.IssueClosed {
		t.Error("expected IssueClosed=false — the issue is not actually closed")
	}
}

// TestPostMergeEpicUmbrellaNotSurvivalEligible asserts the #4151 attribution
// boundary: an epic-umbrella merge (N issues → 1 commit) is NOT survival-eligible
// even when a merge breadcrumb is captured, because the N→1 mapping makes
// "which issue's prediction held up?" ambiguous.
func TestPostMergeEpicUmbrellaNotSurvivalEligible(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#86": {
			NodeID:            "I_node86",
			Number:            86,
			ParentIssueNumber: 0,
			IsEpic:            true,
		},
	}}
	epicCloser := &mockEpicAutoCloser{
		orphanResult: &gh.OrphanCloseResult{EpicNumber: 86, Guard: "completed", Closed: 1},
	}
	verifier := &mockPRVerifierWithMerge{
		state:    "MERGED",
		sha:      "epicmergesha",
		mergedAt: "2026-06-26T12:00:00Z",
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, epicCloser, verifier, nil, PostMergeInput{
		IssueNumber:     86,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		PRNumber:        140,
	})

	// The breadcrumb is still captured (it is the epic's own merge)...
	if result.MergedCommitSha != "epicmergesha" {
		t.Errorf("MergedCommitSha = %q, want epicmergesha", result.MergedCommitSha)
	}
	// ...but survival attribution is skipped for the umbrella PR.
	if result.SurvivalEligible {
		t.Error("epic-umbrella merge must NOT be survival-eligible (ambiguous N→1 attribution)")
	}
}

// --- #691: the board row that was never created ---------------------------
//
// Issues filed ad-hoc with `gh issue create` never reach the project board. The
// hook then looked up a row that did not exist, logged a warning, and still
// exited 0: the issue closed on the tracker while the board showed nothing.
// Observed three times in production on #675, #801 and again on #722/#723.

func TestPostMergeAddsMissingBoardRowThenSyncsDone(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#722": {NodeID: "I_node722", Number: 722},
	}}
	// The issue exists but has no board row — the ad-hoc-creation shape.
	board := &mockBoardSyncer{rows: map[int]bool{}}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
		IssueNumber:     722,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   3,
	})

	if len(board.adds) != 1 || board.adds[0] != 722 {
		t.Fatalf("AddIssueByNumber calls = %v, want exactly [722] — a missing row must be repaired, not warned about", board.adds)
	}
	if !board.onBoard(722) {
		t.Error("issue #722 is still not on the board after the hook ran")
	}
	if board.syncs != 2 {
		t.Errorf("SyncStatus calls = %d, want 2 (the failing probe, then the retry after the row exists)", board.syncs)
	}
	if result.IssueDoneSync != BoardSyncRepaired {
		t.Errorf("IssueDoneSync = %q, want %q", result.IssueDoneSync, BoardSyncRepaired)
	}
	if !result.IssueDoneSync.Done() {
		t.Error("a repaired sync must report Done() — the board row IS at Done")
	}
}

// The three outcomes a caller has to be able to tell apart. Before #691 the
// last two were both `IssueDoneSynced=false`, and the hook's exit code is 0 in
// every one of these cases by design, so the result field is the only signal
// there is.
func TestPostMergeBoardSyncOutcomesAreDistinguishable(t *testing.T) {
	run := func(t *testing.T, board *mockBoardSyncer, project int) PostMergeResult {
		t.Helper()
		fetcher := &mockFetcher{issues: map[string]*types.Issue{
			"nightgauge/nightgauge#900": {NodeID: "I_node900", Number: 900},
		}}
		return EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
			IssueNumber:     900,
			RepositoryOwner: "nightgauge",
			RepositoryName:  "nightgauge",
			ProjectNumber:   project,
		})
	}

	tests := []struct {
		name  string
		board *mockBoardSyncer
		proj  int
		want  BoardSyncOutcome
		done  bool
	}{
		{"row exists", &mockBoardSyncer{rows: map[int]bool{900: true}}, 3, BoardSyncSynced, true},
		{"row missing, repaired", &mockBoardSyncer{rows: map[int]bool{}}, 3, BoardSyncRepaired, true},
		{"nothing to sync", &mockBoardSyncer{}, 0, BoardSyncNotAttempted, false},
		{"sync genuinely failed", &mockBoardSyncer{err: fmt.Errorf("network timeout")}, 3, BoardSyncFailed, false},
		{"row missing and add failed", &mockBoardSyncer{rows: map[int]bool{}, addErr: fmt.Errorf("forbidden")}, 3, BoardSyncFailed, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, tc.board, tc.proj)
			if got.IssueDoneSync != tc.want {
				t.Errorf("IssueDoneSync = %q, want %q", got.IssueDoneSync, tc.want)
			}
			if got.IssueDoneSync.Done() != tc.done {
				t.Errorf("Done() = %v, want %v", got.IssueDoneSync.Done(), tc.done)
			}
		})
	}
}

// The four outcomes must be four DISTINCT values.
//
// Every other assertion in this file compares a result against these constants,
// which makes them all tautological if two constants collapse to the same
// string — collapsing `failed` into `not_attempted` (precisely the conflation
// #691 is about) leaves the table test above green. This is the arm that
// notices.
func TestBoardSyncOutcomesAreDistinctValues(t *testing.T) {
	all := map[string]BoardSyncOutcome{
		"BoardSyncNotAttempted": BoardSyncNotAttempted,
		"BoardSyncSynced":       BoardSyncSynced,
		"BoardSyncRepaired":     BoardSyncRepaired,
		"BoardSyncFailed":       BoardSyncFailed,
	}
	seen := map[BoardSyncOutcome]string{}
	for name, v := range all {
		if v == "" {
			t.Errorf("%s has the empty value, which is indistinguishable from an unset field", name)
		}
		if other, dup := seen[v]; dup {
			t.Errorf("%s and %s share the value %q — a caller cannot tell them apart", name, other, v)
		}
		seen[v] = name
	}
	if len(seen) != len(all) {
		t.Errorf("got %d distinct outcome values, want %d", len(seen), len(all))
	}

	// Done() must partition them, not merely be true for one.
	for name, v := range all {
		want := name == "BoardSyncSynced" || name == "BoardSyncRepaired"
		if v.Done() != want {
			t.Errorf("%s.Done() = %v, want %v", name, v.Done(), want)
		}
	}
}

// A non-repairable failure must NOT be retried as if the row were missing:
// writing to the board does not fix an auth or network error, and doing so
// would turn a clear failure into a confusing one.
func TestPostMergeDoesNotAddRowOnUnrelatedSyncError(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#901": {NodeID: "I_node901", Number: 901},
	}}
	board := &mockBoardSyncer{err: fmt.Errorf("401 Bad credentials")}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
		IssueNumber:     901,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   3,
	})

	if len(board.adds) != 0 {
		t.Errorf("AddIssueByNumber calls = %v, want none for a non-repairable error", board.adds)
	}
	if board.syncs != 1 {
		t.Errorf("SyncStatus calls = %d, want 1 (no retry)", board.syncs)
	}
	if result.IssueDoneSync != BoardSyncFailed {
		t.Errorf("IssueDoneSync = %q, want %q", result.IssueDoneSync, BoardSyncFailed)
	}
}

// The repair keys off the real sentinel, not off message text. If
// ProjectService stopped wrapping ErrIssueNotOnBoard, this is the test that
// notices — the hook would silently go back to warning instead of repairing.
func TestPostMergeRepairMatchesTheRealSentinel(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#902": {NodeID: "I_node902", Number: 902},
	}}
	// Verbatim shape of what findItemID returns, sentinel and all.
	board := &mockBoardSyncer{err: fmt.Errorf("%w: issue #%d (%s/%s) on project board %s/%d",
		gh.ErrIssueNotOnBoard, 902, "nightgauge", "nightgauge", "nightgauge", 3)}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, board, PostMergeInput{
		IssueNumber:     902,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   3,
	})

	if len(board.adds) != 1 {
		t.Fatalf("AddIssueByNumber calls = %v, want exactly one — the sentinel was not recognised", board.adds)
	}
	// `err` is forced, so the retry fails too; the point is that the repair was
	// ATTEMPTED, which is what the sentinel match decides.
	if result.IssueDoneSync != BoardSyncFailed {
		t.Errorf("IssueDoneSync = %q, want %q", result.IssueDoneSync, BoardSyncFailed)
	}
}

// TestPostMergeAutoCloseErrorStatusIsAFailure pins the path production actually
// takes (#1025).
//
// TestPostMergeAutoCloseFailsNonBlocking above drives the `err != nil` arm, and
// that arm is DEAD through *gh.EpicService: AutoCloseSingle swallows every
// error into result.Status="error" and returns a nil error unconditionally
// (internal/github/epic.go). So the one reason word any caller recognised was
// unreachable, every real failure arrived as the bare status word "error", and
// a green test sat next to it proving the unreachable half.
//
// Both shapes must now be indistinguishable to a caller.
func TestPostMergeAutoCloseErrorStatusIsAFailure(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#101": {
			NodeID:            "I_node101",
			Number:            101,
			ParentIssueNumber: 99,
		},
	}}
	closer := &mockEpicAutoCloser{
		// Exactly what *gh.EpicService produces on any API failure: a nil error
		// and a result carrying the cause.
		result: &gh.AutoCloseSingleResult{
			EpicNumber: 99,
			Status:     "error",
			Reason:     "check_failed",
			Error:      "GraphQL: rate limit exceeded",
		},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     101,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if !result.Failed {
		t.Error("Failed=false for a failed epic rollup — every consumer then has to " +
			"re-derive failure from a string vocabulary, which is how this went unread")
	}
	if result.Reason != "auto_close_error" {
		t.Errorf("Reason = %q, want auto_close_error — the bare word %q is not in the vocabulary "+
			"and no caller branches on it", result.Reason, "error")
	}
	if result.EpicReason != "check_failed" {
		t.Errorf("EpicReason = %q, want check_failed — the discriminator was computed and dropped",
			result.EpicReason)
	}
	if result.Error == "" {
		t.Error("Error is empty — the cause was computed by the epic service and never copied out")
	}
	if result.AutoClosed {
		t.Error("AutoClosed=true on a failure")
	}
}

// TestPostMergeSuccessIsNotFailed is the control: the failure flag must not be
// set on the paths that worked, or it is worthless as a discriminator.
func TestPostMergeSuccessIsNotFailed(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#101": {NodeID: "I_node101", Number: 101, ParentIssueNumber: 99},
	}}
	closer := &mockEpicAutoCloser{
		result: &gh.AutoCloseSingleResult{EpicNumber: 99, Status: "skipped", Reason: "has_open"},
	}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, closer, nil, nil, PostMergeInput{
		IssueNumber:     101,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if result.Failed {
		t.Error("Failed=true for a skipped rollup — skipped is a normal outcome, not a failure")
	}
	if result.Reason != "skipped" {
		t.Errorf("Reason = %q, want skipped", result.Reason)
	}
	// The field that made "Epic #N skipped: skipped" possible.
	if result.EpicReason != "has_open" {
		t.Errorf("EpicReason = %q, want has_open — without it the CLI can only re-print Reason",
			result.EpicReason)
	}
}

// TestPostMergeNoParentIsNotFailed keeps the most common outcome quiet.
func TestPostMergeNoParentIsNotFailed(t *testing.T) {
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#101": {NodeID: "I_node101", Number: 101},
	}}

	result := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{}, &mockEpicAutoCloser{}, nil, nil, PostMergeInput{
		IssueNumber:     101,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
	})

	if result.Failed {
		t.Error("Failed=true for an issue with no parent epic — the ordinary case must stay silent")
	}
	if result.Reason != "no_parent" {
		t.Errorf("Reason = %q, want no_parent", result.Reason)
	}
}
