// Coverage closure tests — narrow tests for the 0%-coverage functions in
// project.go, ci.go, issues.go, and mrs.go. The goal is per-function coverage
// for the previously untested boundaries; richer behaviour is exercised by
// the dedicated *_test.go files alongside each source file.
//
// Each test is intentionally small: stub the minimal endpoints, exercise the
// API boundary, assert the contract. Failures here pin specific functions —
// useful for future refactors where a behaviour change should be visible at
// the boundary.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// --- project.go 0%-coverage closure ---

func TestNewProjectService_DefaultsBoundEmpty(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectService(c)
	if p == nil {
		t.Fatal("NewProjectService returned nil")
	}
	if p.strategy != StrategyLabelStatus {
		t.Errorf("strategy = %q, want StrategyLabelStatus default", p.strategy)
	}
}

func TestProjectService_AddItem_AppliesBacklogStatus(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	installIssueDispatcher(srv, 42, MarshalRawIssue(42, "x", nil))

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	itemID, err := p.AddItem(context.Background(), "gitlab:o/r#42")
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if itemID != "gitlab:o/r#42" {
		t.Errorf("itemID = %q, want gitlab:o/r#42", itemID)
	}
}

func TestProjectService_AddIssueByNumber_AppliesBacklogStatus(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	installIssueDispatcher(srv, 99, MarshalRawIssue(99, "x", nil))

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	itemID, err := p.AddIssueByNumber(context.Background(), "o", "r", 99)
	if err != nil {
		t.Fatalf("AddIssueByNumber: %v", err)
	}
	if !strings.HasSuffix(itemID, "#99") {
		t.Errorf("itemID = %q, want .../#99 suffix", itemID)
	}
}

func TestProjectService_BulkAddIssues_TalliesAddAndError(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	installIssueDispatcher(srv, 1, MarshalRawIssue(1, "x", nil))
	// Issue #2 deliberately not stubbed — its handler 404s and AddIssueByNumber
	// must surface that as a per-issue error in the BulkAddResult.

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	res := p.BulkAddIssues(context.Background(), "o", "r", []forgetypes.Issue{
		{Number: 1},
		{Number: 2},
	})
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2", res.Total)
	}
	if res.Added < 1 {
		t.Errorf("Added = %d, want at least 1", res.Added)
	}
	if res.Failed < 1 {
		t.Errorf("Failed = %d, want at least 1 (issue #2 has no stub)", res.Failed)
	}
}

func TestProjectService_MoveStatus_DelegatesToSyncStatus(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.MoveStatus(context.Background(), "o", "r", 42, "Ready"); err != nil {
		t.Fatalf("MoveStatus: %v", err)
	}
	labelStr, _ := (*captured)["labels"].(string)
	if !strings.Contains(labelStr, "Status::Ready") {
		t.Errorf("labels = %q, want Status::Ready", labelStr)
	}
}

func TestProjectService_SetTextField_EncodesAsScopedLabel(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetTextField(context.Background(), "gitlab:o/r#42", "Owner", "alice"); err != nil {
		t.Fatalf("SetTextField: %v", err)
	}
	labelStr, _ := (*captured)["labels"].(string)
	if !strings.Contains(labelStr, "Owner::alice") {
		t.Errorf("labels = %q, want Owner::alice", labelStr)
	}
}

func TestProjectService_SetTextField_RejectsValueWithDelimiter(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	err := p.SetTextField(context.Background(), "gitlab:o/r#42", "Owner", "alice::bob")
	if err == nil || !strings.Contains(err.Error(), "::") {
		t.Errorf("expected error about :: delimiter, got %v", err)
	}
}

func TestProjectService_SetTextFieldOptional_DelegatesToSetText(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetTextFieldOptional(context.Background(), "gitlab:o/r#42", "Notes", "todo"); err != nil {
		t.Fatalf("SetTextFieldOptional: %v", err)
	}
	labelStr, _ := (*captured)["labels"].(string)
	if !strings.Contains(labelStr, "Notes::todo") {
		t.Errorf("labels = %q, want Notes::todo", labelStr)
	}
}

func TestProjectService_SetDateField_DueDateMapsToDueDate(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetDateField(context.Background(), "gitlab:o/r#42", "Due date", "2026-12-31"); err != nil {
		t.Fatalf("SetDateField: %v", err)
	}
	if (*captured)["due_date"] != "2026-12-31" {
		t.Errorf("due_date = %v, want 2026-12-31", (*captured)["due_date"])
	}
}

func TestProjectService_SetDateField_NonDueDateEncodesAsScopedLabel(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetDateField(context.Background(), "gitlab:o/r#42", "Started on", "2026-01-15"); err != nil {
		t.Fatalf("SetDateField: %v", err)
	}
	labelStr, _ := (*captured)["labels"].(string)
	if !strings.Contains(labelStr, "Started on::2026-01-15") {
		t.Errorf("labels = %q, want Started on::2026-01-15", labelStr)
	}
}

func TestProjectService_SetDateFieldOptional_DelegatesToSetDateField(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetDateFieldOptional(context.Background(), "gitlab:o/r#42", "Due date", "2026-06-15"); err != nil {
		t.Fatalf("SetDateFieldOptional: %v", err)
	}
	if (*captured)["due_date"] != "2026-06-15" {
		t.Errorf("due_date = %v, want 2026-06-15", (*captured)["due_date"])
	}
}

func TestProjectService_SetDateFieldByNumber_BridgesToSetDateField(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetDateFieldByNumber(context.Background(), "o", "r", 42, "Due date", "2026-09-01"); err != nil {
		t.Fatalf("SetDateFieldByNumber: %v", err)
	}
	if (*captured)["due_date"] != "2026-09-01" {
		t.Errorf("due_date = %v, want 2026-09-01", (*captured)["due_date"])
	}
}

func TestProjectService_SetFields_BatchAppliesAll(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	calls := 0
	currentLabels := []string{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PUT" {
				calls++
				var body map[string]any
				_ = json.Unmarshal(srv.lastBody, &body)
				if v, ok := body["labels"].(string); ok {
					currentLabels = nil
					for _, l := range strings.Split(v, ",") {
						if l != "" {
							currentLabels = append(currentLabels, l)
						}
					}
				}
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(42, "x", currentLabels)))
		})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	err := p.SetFields(context.Background(), "o", "r", 42, map[string]string{
		"Status":   "Ready",
		"Priority": "P0",
	})
	if err != nil {
		t.Fatalf("SetFields: %v", err)
	}
	if calls < 2 {
		t.Errorf("PUT calls = %d, want at least 2 (one per field)", calls)
	}
}

func TestProjectService_DriftFix_ReappliesExpectedScopedLabel(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	// Stub the board listing endpoint so DriftCheck has something to walk.
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		// No issues with drift in this stub — DriftFix returns an empty
		// fixed slice. The contract pinned here is "DriftFix returns nil
		// without error when no drift is present".
		_, _ = w.Write([]byte("[]"))
	})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	fixed, err := p.DriftFix(context.Background())
	if err != nil {
		t.Fatalf("DriftFix: %v", err)
	}
	if len(fixed) != 0 {
		t.Errorf("fixed = %v, want empty (no drift in stub)", fixed)
	}
}

func TestIsWeightCEError_ClassifiesSentinel(t *testing.T) {
	if isWeightCEError(nil) {
		t.Error("nil err should not classify as weight CE error")
	}
	if !isWeightCEError(forge.ErrUnsupportedOnEdition) {
		t.Error("ErrUnsupportedOnEdition must classify as weight CE error")
	}
	wrapped := fmt.Errorf("HTTP 400: weight is not allowed")
	if !isWeightCEError(wrapped) {
		t.Errorf("HTTP 400 mentioning weight should classify, got false")
	}
	if isWeightCEError(fmt.Errorf("HTTP 500: server error")) {
		t.Error("HTTP 500 should not classify as weight CE error")
	}
}

func TestIsHealthCEError_ClassifiesSentinel(t *testing.T) {
	if isHealthCEError(nil) {
		t.Error("nil err should not classify as health CE error")
	}
	if !isHealthCEError(forge.ErrUnsupportedOnEdition) {
		t.Error("ErrUnsupportedOnEdition must classify as health CE error")
	}
	wrapped := fmt.Errorf("HTTP 400: health_status field unsupported")
	if !isHealthCEError(wrapped) {
		t.Errorf("HTTP 400 mentioning health_status should classify, got false")
	}
	if isHealthCEError(fmt.Errorf("HTTP 503: degraded")) {
		t.Error("HTTP 503 should not classify as health CE error")
	}
}

// --- ci.go 0%-coverage closure ---

func TestDefaultWaitConfig_HasNonZeroDefaults(t *testing.T) {
	cfg := DefaultWaitConfig()
	if cfg.Timeout == 0 {
		t.Error("Timeout = 0, want non-zero default")
	}
	if cfg.PollInterval == 0 {
		t.Error("PollInterval = 0, want non-zero default")
	}
	if cfg.PollInterval >= cfg.Timeout {
		t.Errorf("PollInterval (%v) must be < Timeout (%v)", cfg.PollInterval, cfg.Timeout)
	}
}

func TestPipelineLifecycleStatus_TableMapping(t *testing.T) {
	cases := map[string]string{
		"success":              "completed",
		"failed":               "completed",
		"canceled":             "completed",
		"skipped":              "completed",
		"created":              "queued",
		"waiting_for_resource": "queued",
		"preparing":            "queued",
		"scheduled":            "queued",
		"manual":               "queued",
		"running":              "in_progress",
		"pending":              "in_progress",
		"unknown_status":       "in_progress",
	}
	for in, want := range cases {
		if got := pipelineLifecycleStatus(in); got != want {
			t.Errorf("pipelineLifecycleStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPipelineConclusion_TableMapping(t *testing.T) {
	cases := map[string]string{
		"success":  "success",
		"failed":   "failure",
		"canceled": "cancelled",
		"skipped":  "skipped",
		"running":  "",
		"pending":  "",
	}
	for in, want := range cases {
		if got := pipelineConclusion(in); got != want {
			t.Errorf("pipelineConclusion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCIService_GetRequiredOnlyStatus_HappyPath exercises the
// getRequiredOnlyStatus polling path (PR fetch → check-run fetch → required-only
// rollup). Mirrors GitHub's required-only mode for cross-forge symmetry.
func TestCIService_GetRequiredOnlyStatus_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 5042, "iid": 1, "project_id": 5,
			"title": "MR1", "state": "opened",
			"source_branch": "feat/foo", "target_branch": "main",
			"sha":     "abcdef0123456789abcdef0123456789abcdef01",
			"web_url": "https://gitlab.example.com/o/r/-/merge_requests/1",
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 9, Status: "success", SHA: "abc"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/9/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawJob{
			{Name: "lint", Stage: "test", Status: "success"},
			{Name: "build", Stage: "test", Status: "success"},
		})
	})
	srv := newStubServer(t)
	srv.srv.Config.Handler = mux

	c := NewClient(srv.srv.URL, "tok")
	svc := NewCIService(c)

	got, err := svc.getRequiredOnlyStatus(context.Background(), "o", "r", 1, []string{"test/lint", "test/build"})
	if err != nil {
		t.Fatalf("getRequiredOnlyStatus: %v", err)
	}
	if !got.RequiredPassed {
		t.Errorf("RequiredPassed = false, want true (all required checks passed)")
	}
	if got.State != "SUCCESS" {
		t.Errorf("State = %q, want SUCCESS", got.State)
	}
}

// --- issues.go stub-coverage closure ---

func TestIssueService_EditIssue_DelegatesToUpdateIssue(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("PUT", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)

	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.EditIssue(context.Background(), "o/r#42", "new body content")
	if err != nil {
		t.Fatalf("EditIssue: %v", err)
	}
	if got == nil {
		t.Fatal("EditIssue returned nil")
	}
	var body map[string]any
	_ = json.Unmarshal(srv.lastBody, &body)
	if body["description"] != "new body content" {
		t.Errorf("description = %v, want 'new body content'", body["description"])
	}
}

func TestIssueService_SearchIssues_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.SearchIssues(context.Background(), "o", "r", "term", 5)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestIssueService_HasLabel_ReturnsTrueWhenLabelMatches(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)

	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, err := svc.HasLabel(context.Background(), "o", "r", 42, "bug")
	if err != nil {
		t.Fatalf("HasLabel: %v", err)
	}
	if !got {
		t.Error("HasLabel(bug) = false, want true (sampleIssueJSON includes bug)")
	}
}

func TestIssueService_HasLabel_ReturnsFalseWhenLabelMissing(t *testing.T) {
	srv := newStubServer(t)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200, sampleIssueJSON)

	c := NewClient(srv.srv.URL, "tok")
	svc := NewIssueService(c)

	got, _ := svc.HasLabel(context.Background(), "o", "r", 42, "nonexistent")
	if got {
		t.Error("HasLabel(nonexistent) = true, want false")
	}
}

func TestIssueService_GetRepoLabels_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.GetRepoLabels(context.Background(), "o", "r")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestIssueService_GetEpicProgress_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.GetEpicProgress(context.Background(), "node-id")
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestIssueService_GetEpicProgressByNumber_ReturnsErrUnsupported(t *testing.T) {
	c := NewClient("", "tok")
	svc := NewIssueService(c)
	_, err := svc.GetEpicProgressByNumber(context.Background(), "o", "r", 99)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestPriorityFromLabels_TableMapping exercises every priority label →
// Priority enum branch including the empty-string fallthrough.
func TestPriorityFromLabels_TableMapping(t *testing.T) {
	cases := []struct {
		labels []string
		want   forgetypes.Priority
	}{
		{[]string{"priority:critical"}, forgetypes.Priority("P0")},
		{[]string{"priority:high"}, forgetypes.Priority("P1")},
		{[]string{"priority:medium"}, forgetypes.Priority("P2")},
		{[]string{"priority:low"}, forgetypes.Priority("P3")},
		{[]string{"unrelated"}, forgetypes.Priority("")},
		{nil, forgetypes.Priority("")},
	}
	for _, tc := range cases {
		if got := priorityFromLabels(tc.labels); got != tc.want {
			t.Errorf("priorityFromLabels(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
}

// TestSizeFromLabels_TableMapping exercises every size label → Size enum branch
// including the empty-string fallthrough.
func TestSizeFromLabels_TableMapping(t *testing.T) {
	cases := []struct {
		labels []string
		want   forgetypes.Size
	}{
		{[]string{"size:XS"}, forgetypes.Size("XS")},
		{[]string{"size:S"}, forgetypes.Size("S")},
		{[]string{"size:M"}, forgetypes.Size("M")},
		{[]string{"size:L"}, forgetypes.Size("L")},
		{[]string{"size:XL"}, forgetypes.Size("XL")},
		{[]string{"unrelated"}, forgetypes.Size("")},
		{nil, forgetypes.Size("")},
	}
	for _, tc := range cases {
		if got := sizeFromLabels(tc.labels); got != tc.want {
			t.Errorf("sizeFromLabels(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
}

// TestIsPassingConclusion_AllVariants exercises each conclusion enum value plus
// case-insensitivity (the helper applies strings.ToUpper internally).
func TestIsPassingConclusion_AllVariants(t *testing.T) {
	cases := map[string]bool{
		"SUCCESS":   true,
		"NEUTRAL":   true,
		"SKIPPED":   true,
		"FAILURE":   false,
		"CANCELLED": false,
		"":          false,
		"success":   true, // case-insensitive
		"failure":   false,
	}
	for in, want := range cases {
		if got := isPassingConclusion(in); got != want {
			t.Errorf("isPassingConclusion(%q) = %v, want %v", in, got, want)
		}
	}
}

// --- mrs.go IteratePRs closure ---

func TestPRService_IteratePRs_YieldsEachThenEOF(t *testing.T) {
	srv := newStubServer(t)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id": 5042, "iid": 7, "project_id": 5,
			"title": "Sample MR", "state": "opened",
			"source_branch": "feat/foo", "target_branch": "main",
			"sha": "abcdef0123456789abcdef0123456789abcdef01",
			"web_url": "https://gitlab.example.com/o/r/-/merge_requests/7"
		}]`))
	})

	c := NewClient(srv.srv.URL, "tok")
	svc := NewPRService(c)
	it := svc.IteratePRs(context.Background(), "o", "r", "opened", "")
	defer it.Close()

	first, err := it.Next(context.Background())
	if err != nil {
		t.Fatalf("Next #1: %v", err)
	}
	if first.Number != 7 {
		t.Errorf("first.Number = %d, want 7", first.Number)
	}
	if _, err := it.Next(context.Background()); err != io.EOF {
		t.Errorf("Next #2: want io.EOF, got %v", err)
	}
}
