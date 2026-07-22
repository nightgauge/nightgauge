package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewEpicService(t *testing.T) {
	client := NewClientWithToken("test")
	svc := NewEpicService(client)
	if svc == nil {
		t.Fatal("NewEpicService returned nil")
	}
	if svc.client != client {
		t.Error("client not set correctly")
	}
}

func TestClassifyTier(t *testing.T) {
	tests := []struct {
		total    int
		progress float64
		want     string
	}{
		{1, 0.0, "minimal"},
		{3, 0.5, "minimal"},
		{5, 0.5, "standard"},
		{10, 0.3, "standard"},
		{15, 0.9, "standard"}, // large but nearly complete
		{15, 0.5, "detailed"}, // large and incomplete
		{20, 0.3, "detailed"},
		{5, 0.85, "standard"}, // >0.8 progress
	}

	for _, tt := range tests {
		got := classifyTier(tt.total, tt.progress)
		if got != tt.want {
			t.Errorf("classifyTier(%d, %.1f) = %q, want %q", tt.total, tt.progress, got, tt.want)
		}
	}
}

func TestBuildSummaryText(t *testing.T) {
	// Complete epic
	s := &EpicSummary{
		EpicNumber: 42,
		Title:      "Test Epic",
		Progress:   1.0,
		Total:      5,
		Closed:     5,
		Open:       0,
	}
	text := buildSummaryText(s)
	if text == "" {
		t.Error("buildSummaryText returned empty for complete epic")
	}
	if !containsStr(text, "complete") {
		t.Errorf("complete epic summary should mention 'complete': %s", text)
	}

	// Incomplete epic
	s2 := &EpicSummary{
		EpicNumber: 43,
		Title:      "In Progress",
		Progress:   0.6,
		Total:      10,
		Closed:     6,
		Open:       4,
	}
	text2 := buildSummaryText(s2)
	if !containsStr(text2, "60%") {
		t.Errorf("incomplete epic should show 60%%: %s", text2)
	}
	if !containsStr(text2, "4 remaining") {
		t.Errorf("incomplete epic should show remaining count: %s", text2)
	}
}

func TestSplitEpicOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"nightgauge/nightgauge", "nightgauge", "nightgauge"},
		{"org/repo-name", "org", "repo-name"},
		{"justname", "", "justname"},
	}

	for _, tt := range tests {
		gotOwner, gotRepo := splitEpicOwnerRepo(tt.input)
		if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
			t.Errorf("splitEpicOwnerRepo(%q) = (%q, %q), want (%q, %q)",
				tt.input, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
		}
	}
}

// containsStr is a test helper for substring matching.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && contains(s, substr)
}

func TestAutoCloseResultJSON(t *testing.T) {
	result := &AutoCloseResult{
		Checked: 5,
		Closed:  2,
		Skipped: 3,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var unmarshaled AutoCloseResult
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if unmarshaled.Checked != 5 || unmarshaled.Closed != 2 || unmarshaled.Skipped != 3 {
		t.Errorf("round-trip failed: got %+v", unmarshaled)
	}
}

func TestAutoCloseResultJSONFields(t *testing.T) {
	result := &AutoCloseResult{
		Checked: 1,
		Closed:  1,
		Skipped: 0,
		Summary: []struct {
			EpicNumber int    `json:"epicNumber"`
			Title      string `json:"title"`
			Status     string `json:"status"`
			Reason     string `json:"reason,omitempty"`
			Error      string `json:"error,omitempty"`
		}{
			{EpicNumber: 42, Title: "Test Epic", Status: "closed", Reason: "all_closed"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	jsonStr := string(data)
	for _, want := range []string{`"checked":1`, `"closed":1`, `"epicNumber":42`, `"all_closed"`} {
		if !containsStr(jsonStr, want) {
			t.Errorf("JSON missing %q: %s", want, jsonStr)
		}
	}
}

func TestAutoCloseResultSummaryOmitEmpty(t *testing.T) {
	// reason and error should be omitted when empty
	result := &AutoCloseResult{
		Checked: 1,
		Skipped: 1,
		Summary: []struct {
			EpicNumber int    `json:"epicNumber"`
			Title      string `json:"title"`
			Status     string `json:"status"`
			Reason     string `json:"reason,omitempty"`
			Error      string `json:"error,omitempty"`
		}{
			{EpicNumber: 10, Title: "Empty Epic", Status: "skipped", Reason: "no_subs"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	jsonStr := string(data)
	if containsStr(jsonStr, `"error"`) {
		t.Errorf("empty error field should be omitted: %s", jsonStr)
	}
}

// --- SweepEpics resilience tests ---

// epicListResponse builds a mock GraphQL response for ListIssues returning one epic.
func epicListResponse(epicNumbers ...int) string {
	var nodes []string
	for _, n := range epicNumbers {
		nodes = append(nodes, fmt.Sprintf(
			`{"id":"I_%d","number":%d,"title":"Epic %d","state":"OPEN","url":"","labels":{"nodes":[]}}`,
			n, n, n,
		))
	}
	return fmt.Sprintf(`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[%s]}}}}`,
		strings.Join(nodes, ","))
}

// epicGetIssueSuccess builds a mock GraphQL response for GetIssue returning a complete epic.
func epicGetIssueSuccess(epicNumber int) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"I_%d","number":%d,"title":"Epic %d","body":"","state":"OPEN","url":"",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"type:epic"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[
			{"id":"SUB_1","number":200,"title":"Sub","state":"CLOSED","repository":{"nameWithOwner":"o/r"}}
		]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, epicNumber, epicNumber, epicNumber)
}

func TestSweepEpics_EmptyRepo_ReturnsEmptyNoError(t *testing.T) {
	response := `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`
	client, cleanup := mockGraphQLServer(t, response)
	defer cleanup()

	svc := NewEpicService(client)
	results, err := svc.SweepEpics(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("SweepEpics empty repo returned unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("SweepEpics empty repo = %d results, want 0", len(results))
	}
}

func TestSweepEpics_AllFail_ReturnsError(t *testing.T) {
	// ListIssues returns 1 epic; GetIssue for it fails → all checks failed → error.
	// This test was PASSING with the old firstErr code too; it validates the total-failure path.
	listResp := epicListResponse(100)
	errResp := `{"errors":[{"message":"not found"}]}`

	client, cleanup := mockGraphQLServer(t, listResp, errResp)
	defer cleanup()

	svc := NewEpicService(client)
	_, err := svc.SweepEpics(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("SweepEpics with all-failed checks should return an error")
	}
}

func TestSweepEpics_PartialFailure_ContinuesWithPartialResults(t *testing.T) {
	// ListIssues returns 2 epics. GetIssue for one fails, the other succeeds.
	// The pre-fix firstErr code would abort on the first failure and return an error.
	// The fixed code collects per-epic errors and returns partial results without error.
	listResp := epicListResponse(100, 200)

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&callCount, 1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// ListIssues
			fmt.Fprint(w, listResp)
		default:
			// GetIssue calls — parse body to route by issue number
			body, _ := io.ReadAll(r.Body)
			reqStr := string(body)
			if strings.Contains(reqStr, `"number":100`) || strings.Contains(reqStr, `"number": 100`) {
				// Epic #100 fails
				fmt.Fprint(w, `{"errors":[{"message":"not found"}]}`)
			} else {
				// Epic #200 succeeds with one closed sub-issue
				fmt.Fprint(w, epicGetIssueSuccess(200))
			}
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := NewEpicService(client)

	results, err := svc.SweepEpics(context.Background(), "o", "r")
	if err != nil {
		// The old firstErr code would return an error here; the fix should not.
		t.Fatalf("SweepEpics partial failure returned unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Error("SweepEpics partial failure should return results for the successful epic")
	}
}

// --- FilterByEpicNumber tests ---

func TestFilterByEpicNumber_MatchesSingleEpic(t *testing.T) {
	base := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      "o/r",
		RunAt:     "2026-04-30T00:00:00Z",
		Findings: []LifecycleFinding{
			{Category: "STALE_EPIC", IssueNumber: 100, IssueTitle: "Target Epic", Severity: "high"},
			{Category: "BOARD_STATUS_DRIFT", IssueNumber: 200, IssueTitle: "Other Issue", Severity: "medium"},
			{Category: "STALE_BLOCKER", IssueNumber: 100, IssueTitle: "Target Epic", Severity: "medium"},
		},
	}
	base.Summary = buildAuditSummary(base.Findings)

	filtered := base.FilterByEpicNumber(100)

	if len(filtered.Findings) != 2 {
		t.Fatalf("FilterByEpicNumber(100) returned %d findings, want 2", len(filtered.Findings))
	}
	for _, f := range filtered.Findings {
		if f.IssueNumber != 100 {
			t.Errorf("unexpected finding for issue #%d in filtered result", f.IssueNumber)
		}
	}
}

func TestFilterByEpicNumber_NoMatch_ReturnsEmpty(t *testing.T) {
	base := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      "o/r",
		Findings: []LifecycleFinding{
			{Category: "STALE_EPIC", IssueNumber: 200, Severity: "high"},
		},
	}

	filtered := base.FilterByEpicNumber(999)

	if filtered == nil {
		t.Fatal("FilterByEpicNumber returned nil")
	}
	if len(filtered.Findings) != 0 {
		t.Errorf("FilterByEpicNumber(999) returned %d findings, want 0", len(filtered.Findings))
	}
	if filtered.Summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", filtered.Summary.Total)
	}
}

func TestFilterByEpicNumber_SummaryRebuilt(t *testing.T) {
	base := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      "o/r",
		Findings: []LifecycleFinding{
			{Category: "STALE_EPIC", IssueNumber: 100, Severity: "high"},
			{Category: "STALE_BLOCKER", IssueNumber: 100, Severity: "medium"},
			{Category: "ORPHANED_ISSUE", IssueNumber: 300, Severity: "low"},
		},
	}

	filtered := base.FilterByEpicNumber(100)

	if filtered.Summary.Total != 2 {
		t.Errorf("Summary.Total = %d, want 2", filtered.Summary.Total)
	}
	if filtered.Summary.StaleEpics != 1 {
		t.Errorf("Summary.StaleEpics = %d, want 1", filtered.Summary.StaleEpics)
	}
	if filtered.Summary.StaleBlocker != 1 {
		t.Errorf("Summary.StaleBlocker = %d, want 1", filtered.Summary.StaleBlocker)
	}
	if filtered.Summary.Orphaned != 0 {
		t.Errorf("Summary.Orphaned = %d, want 0 (issue #300 excluded)", filtered.Summary.Orphaned)
	}
}

func TestFilterByEpicNumber_PreservesMetadata(t *testing.T) {
	base := &LifecycleAuditResult{
		Dimension: "epic-lifecycle",
		Repo:      "nightgauge/nightgauge",
		RunAt:     "2026-04-30T12:00:00Z",
		FixMode:   true,
		Findings:  []LifecycleFinding{},
	}

	filtered := base.FilterByEpicNumber(42)

	if filtered.Dimension != base.Dimension {
		t.Errorf("Dimension = %q, want %q", filtered.Dimension, base.Dimension)
	}
	if filtered.Repo != base.Repo {
		t.Errorf("Repo = %q, want %q", filtered.Repo, base.Repo)
	}
	if filtered.RunAt != base.RunAt {
		t.Errorf("RunAt = %q, want %q", filtered.RunAt, base.RunAt)
	}
	if filtered.FixMode != base.FixMode {
		t.Errorf("FixMode = %v, want %v", filtered.FixMode, base.FixMode)
	}
}

func TestAutoClose_ListIssuesError_ReturnsEmptyResult(t *testing.T) {
	// When ListIssues fails, AutoClose should return an empty result (exit 0)
	// rather than propagating the error (exit 1). This was the root cause of
	// the nightly sweep failing for repos without the type:epic label.
	errResp := `{"errors":[{"message":"label not found"}]}`

	client, cleanup := mockGraphQLServer(t, errResp)
	defer cleanup()

	svc := NewEpicService(client)
	result, err := svc.AutoClose(context.Background(), "o", "r", 0)
	if err != nil {
		t.Fatalf("AutoClose should not propagate ListIssues error; got: %v", err)
	}
	if result == nil {
		t.Fatal("AutoClose should return non-nil result even when ListIssues fails")
	}
	if result.Checked != 0 {
		t.Errorf("AutoClose.Checked = %d, want 0", result.Checked)
	}
}

// epicGetIssueWithOpenSub returns a mock GraphQL response for GetIssue where the
// epic has one OPEN sub-issue (simulating eventual consistency: not yet propagated).
func epicGetIssueWithOpenSub(epicNumber int) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"I_%d","number":%d,"title":"Epic %d","body":"","state":"OPEN","url":"",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"type:epic"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[
			{"id":"SUB_1","number":200,"title":"Sub","state":"OPEN","repository":{"nameWithOwner":"o/r"}}
		]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, epicNumber, epicNumber, epicNumber)
}

// epicIssueOpenState returns a mock GraphQL response for GetIssue for the epic itself (OPEN state).
func epicIssueOpenState(epicNumber int) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"I_%d_epic","number":%d,"title":"Epic %d","body":"","state":"OPEN","url":"",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[{"name":"type:epic"}]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, epicNumber, epicNumber, epicNumber)
}

// TestCloseOneEpicRetriesOnEventualConsistency verifies that closeOneEpic retries
// CheckCompletion when the first call returns open sub-issues (eventual consistency),
// and closes the epic once a retry confirms all sub-issues are closed.
//
// We test via AutoCloseSingle (which wraps closeOneEpic) to avoid exporting the
// private method, using time.Sleep mocking via a short retry interval.
//
// NOTE: the retry uses time.After with real durations. This test uses a mock
// server that immediately returns "complete" on the second call, so the only
// latency is the 2s base delay of the first retry. We accept this in the test
// suite — the retry logic is the important invariant to verify.
func TestCloseOneEpicEventualConsistencyRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode (requires ~2s for retry delay)")
	}

	// ListIssues returns one open epic.
	listResp := epicListResponse(100)

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&callCount, 1))
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		reqStr := string(body)
		_ = reqStr

		switch n {
		case 1:
			// ListIssues response
			fmt.Fprint(w, listResp)
		case 2:
			// First CheckCompletion: epic has 1 OPEN sub-issue (eventual consistency)
			fmt.Fprint(w, epicGetIssueWithOpenSub(100))
		case 3:
			// Second CheckCompletion (retry): all sub-issues now closed
			fmt.Fprint(w, epicGetIssueSuccess(100))
		case 4:
			// GetIssue for the epic itself (to check if OPEN before closing)
			fmt.Fprint(w, epicIssueOpenState(100))
		default:
			// Mutations (CloseIssue, AddComment, SyncStatus) can return minimal OK
			fmt.Fprint(w, `{"data":{}}`)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := NewEpicService(client)

	result, err := svc.AutoClose(context.Background(), "o", "r", 0)
	if err != nil {
		t.Fatalf("AutoClose returned unexpected error: %v", err)
	}
	if result.Closed != 1 {
		t.Errorf("AutoClose.Closed = %d, want 1 (epic closed after retry)", result.Closed)
	}

	calls := int(atomic.LoadInt32(&callCount))
	if calls < 3 {
		t.Errorf("expected at least 3 GraphQL calls (list + 2× completion check), got %d", calls)
	}
}

// TestCloseOneEpicNoRetryWhenComplete verifies that closeOneEpic does NOT retry
// when the first CheckCompletion call already reports Complete=true.
func TestCloseOneEpicNoRetryWhenComplete(t *testing.T) {
	listResp := epicListResponse(100)

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&callCount, 1))
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			fmt.Fprint(w, listResp)
		case 2:
			// First CheckCompletion: already complete
			fmt.Fprint(w, epicGetIssueSuccess(100))
		case 3:
			// GetIssue for epic itself (OPEN, ready to close)
			fmt.Fprint(w, epicIssueOpenState(100))
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	svc := NewEpicService(client)

	result, err := svc.AutoClose(context.Background(), "o", "r", 0)
	if err != nil {
		t.Fatalf("AutoClose returned unexpected error: %v", err)
	}
	if result.Closed != 1 {
		t.Errorf("AutoClose.Closed = %d, want 1", result.Closed)
	}
}
