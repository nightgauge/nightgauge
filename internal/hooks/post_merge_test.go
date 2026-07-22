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

// mockBoardSyncer implements BoardSyncer for testing.
type mockBoardSyncer struct {
	err        error
	called     bool
	calledWith struct {
		number int
		status string
	}
}

func (m *mockBoardSyncer) SyncStatus(_ context.Context, _, _ string, issueNumber int, status string) error {
	m.called = true
	m.calledWith.number = issueNumber
	m.calledWith.status = status
	return m.err
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
	if !result.IssueDoneSynced {
		t.Error("expected IssueDoneSynced=true")
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
	if result.IssueDoneSynced {
		t.Error("IssueDoneSynced must be false when no project is configured")
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
