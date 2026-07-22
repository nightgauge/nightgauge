package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPipelineStatusToForgeState_TableMapping(t *testing.T) {
	cases := map[string]string{
		"success":              "SUCCESS",
		"failed":               "FAILURE",
		"canceled":             "ERROR",
		"skipped":              "SUCCESS",
		"pending":              "PENDING",
		"running":              "PENDING",
		"created":              "PENDING",
		"waiting_for_resource": "PENDING",
		"preparing":            "PENDING",
		"manual":               "PENDING",
		"scheduled":            "PENDING",
		"unknown_garbage":      "PENDING", // unknown → PENDING fallback
	}
	for in, want := range cases {
		if got := mapPipelineState(in); got != want {
			t.Errorf("mapPipelineState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeSHA(t *testing.T) {
	if !looksLikeSHA("abcdef0123456789abcdef0123456789abcdef01") {
		t.Error("40-char hex should look like SHA")
	}
	if looksLikeSHA("main") {
		t.Error("branch name should not look like SHA")
	}
	if looksLikeSHA("abcdef0123456789abcdef0123456789abcdefXX") {
		t.Error("non-hex should not look like SHA")
	}
}

func TestGetCheckStatus_NoPipeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/merge_requests/42/pipelines") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	got, err := svc.GetCheckStatus(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "PENDING" {
		t.Errorf("State = %q, want PENDING", got.State)
	}
	if got.IsTerminal {
		t.Error("IsTerminal should be false on no-pipeline")
	}
	if got.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", got.PRNumber)
	}
}

func TestGetCheckStatus_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/42/pipelines", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]rawPipeline{
			{ID: 99, Status: "success", SHA: "abc"},
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 99, Status: "success", SHA: "abc"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/99/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]rawJob{
			{ID: 1, Name: "lint", Stage: "test", Status: "success"},
			{ID: 2, Name: "build", Stage: "build", Status: "success"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	got, err := svc.GetCheckStatus(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "SUCCESS" {
		t.Errorf("State = %q, want SUCCESS", got.State)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal should be true for SUCCESS")
	}
	if got.Total != 2 || got.Successful != 2 || got.Completed != 2 {
		t.Errorf("counts = total=%d successful=%d completed=%d, want 2/2/2", got.Total, got.Successful, got.Completed)
	}
}

func TestGetCheckStatus_MultiStageRollup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/7/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 200, Status: "running", SHA: "deadbeef"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 200, Status: "running", SHA: "deadbeef"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/200/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawJob{
			{Name: "lint", Stage: "test", Status: "success"},
			{Name: "unit", Stage: "test", Status: "success"},
			{Name: "deploy", Stage: "deploy", Status: "running"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	got, err := svc.GetCheckStatus(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "PENDING" {
		t.Errorf("State = %q, want PENDING", got.State)
	}
	if got.Pending != 1 || got.Successful != 2 {
		t.Errorf("counts = pending=%d successful=%d, want 1/2", got.Pending, got.Successful)
	}
	// Names should be qualified by stage.
	want := map[string]bool{"test/lint": true, "test/unit": true, "deploy/deploy": true}
	for _, c := range got.Checks {
		if !want[c.Name] {
			t.Errorf("unexpected check name %q", c.Name)
		}
	}
}

func TestGetIndividualCheckRuns_AllowFailureCollapsesToNeutral(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		// Both ?ref=main and ?sha= variants land here; we don't assert the
		// exact form, only that the upstream returns a pipeline.
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 11, Status: "failed", SHA: "main"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/11/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawJob{
			{Name: "lint", Stage: "test", Status: "failed", AllowFailure: true},
			{Name: "build", Stage: "build", Status: "failed", AllowFailure: false},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	checks, err := svc.GetIndividualCheckRuns(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("len = %d, want 2", len(checks))
	}
	var lintConcl, buildConcl string
	for _, c := range checks {
		if strings.HasSuffix(c.Name, "/lint") {
			lintConcl = c.Conclusion
		}
		if strings.HasSuffix(c.Name, "/build") {
			buildConcl = c.Conclusion
		}
	}
	if lintConcl != "NEUTRAL" {
		t.Errorf("allow_failure lint Conclusion = %q, want NEUTRAL", lintConcl)
	}
	if buildConcl != "FAILURE" {
		t.Errorf("required build Conclusion = %q, want FAILURE", buildConcl)
	}
}

func TestGetIndividualCheckRuns_PicksShaQueryWhenRefIsSHA(t *testing.T) {
	var capturedQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]rawPipeline{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	_, _ = svc.GetIndividualCheckRuns(context.Background(), "o", "r", "abcdef0123456789abcdef0123456789abcdef01")
	if !strings.Contains(capturedQuery, "sha=") {
		t.Errorf("query missing sha=: %s", capturedQuery)
	}
}

func TestGetIndividualCheckRuns_PicksRefQueryForBranchName(t *testing.T) {
	var capturedQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]rawPipeline{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	_, _ = svc.GetIndividualCheckRuns(context.Background(), "o", "r", "main")
	if !strings.Contains(capturedQuery, "ref=main") {
		t.Errorf("query missing ref=main: %s", capturedQuery)
	}
}

func TestGetRequiredCheckNames_NoProtection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404"}`, 404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	names, err := svc.GetRequiredCheckNames(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if names != nil {
		t.Errorf("names = %v, want nil for 404", names)
	}
}

func TestGetRequiredCheckNames_MergesAndDedupes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "main"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approval_rules", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "code-review"},
			{"name": "qa"},
		})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/external_status_checks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "qa"},    // duplicate of approval-rule
			{"name": "sonar"}, // unique
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	names, err := svc.GetRequiredCheckNames(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if len(got) != 3 {
		t.Errorf("dedup count = %d (got %v), want 3 unique", len(got), got)
	}
	for _, want := range []string{"code-review", "qa", "sonar"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestGetRequiredCheckNames_PremiumEndpointsReturn403(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "main"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/approval_rules", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"403"}`, 403)
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/external_status_checks", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"403"}`, 403)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	names, err := svc.GetRequiredCheckNames(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if names != nil {
		t.Errorf("names = %v, want nil when only protected-branch reachable", names)
	}
}

func TestGetRunLogs_FailedFetchesTrace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/123", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawJob{ID: 123, Status: "failed", WebURL: "https://gitlab/jobs/123"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/123/trace", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "lots of failure output here")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	got, err := svc.GetRunLogs(context.Background(), "o", "r", 123)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.RunID != 123 {
		t.Errorf("RunID = %d, want 123", got.RunID)
	}
	if !strings.Contains(got.Content, "failure output") {
		t.Errorf("Content missing trace: %q", got.Content)
	}
}

func TestGetRunLogs_SuccessSkipsTrace(t *testing.T) {
	traceCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/55", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rawJob{ID: 55, Status: "success", WebURL: "https://gitlab/jobs/55"})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/jobs/55/trace", func(w http.ResponseWriter, r *http.Request) {
		traceCalled = true
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	got, err := svc.GetRunLogs(context.Background(), "o", "r", 55)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Content != "" {
		t.Errorf("Content = %q, want empty for success", got.Content)
	}
	if traceCalled {
		t.Error("trace endpoint should not be called for success")
	}
}

func TestListWorkflowRuns_QueryConstruction(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]rawPipeline{
			{ID: 1, Ref: "main", Status: "success", WebURL: "https://gl/p/1", CreatedAt: "2026-05-09T00:00:00Z"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	since, _ := time.Parse(time.RFC3339, "2026-05-01T00:00:00Z")
	runs, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "main", since, 5)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if !strings.Contains(capturedQuery, "ref=main") {
		t.Errorf("query missing ref=main: %s", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "updated_after=") {
		t.Errorf("query missing updated_after: %s", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "per_page=5") {
		t.Errorf("query missing per_page=5: %s", capturedQuery)
	}
	if runs[0].Status != "completed" || runs[0].Conclusion != "success" {
		t.Errorf("mapping: status=%q conclusion=%q", runs[0].Status, runs[0].Conclusion)
	}
}

func TestListWorkflowRuns_404Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, 404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	_, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "main", time.Time{}, 0)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestIteratePipelines_LinkHeaderWalk(t *testing.T) {
	page := 0
	var hostURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v4/projects/o%%2Fr/pipelines?page=2&per_page=100>; rel="next"`, hostURL))
			_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 1, Ref: "main", Status: "success"}})
		default:
			_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 2, Ref: "main", Status: "failed"}})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	hostURL = srv.URL

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	it := svc.IteratePipelines(context.Background(), "o", "r", "main", time.Time{})
	defer it.Close()

	var seen []int64
	for {
		v, err := it.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		seen = append(seen, v.ID)
	}
	if len(seen) != 2 {
		t.Fatalf("seen = %v, want 2 entries", seen)
	}
}

func TestWaitForChecks_TerminalOnFirstPoll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/o%2Fr/merge_requests/1/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 9, Status: "success", SHA: "abc"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawPipeline{{ID: 9, Status: "success", SHA: "abc"}})
	})
	mux.HandleFunc("/api/v4/projects/o%2Fr/pipelines/9/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]rawJob{{Name: "lint", Stage: "test", Status: "success"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	svc := NewCIService(c)
	cfg := WaitConfig{Timeout: time.Second, PollInterval: 100 * time.Millisecond}
	got, err := svc.WaitForChecks(context.Background(), "o", "r", 1, cfg)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "SUCCESS" || !got.IsTerminal {
		t.Errorf("state=%q terminal=%v, want SUCCESS/true", got.State, got.IsTerminal)
	}
}

func TestGetRequiredOnlyStatusWithChecks_AllPassing(t *testing.T) {
	svc := &CIService{}
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "noisy", Status: "COMPLETED", Conclusion: "FAILURE"}, // not required
	}
	got, err := svc.getRequiredOnlyStatusWithChecks(checks, []string{"lint", "build"}, 1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "SUCCESS" || !got.RequiredPassed {
		t.Errorf("state=%q requiredPassed=%v", got.State, got.RequiredPassed)
	}
}

func TestGetRequiredOnlyStatusWithChecks_RequiredFailureBlocksRollup(t *testing.T) {
	svc := &CIService{}
	checks := []CheckDetail{
		{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"},
	}
	got, err := svc.getRequiredOnlyStatusWithChecks(checks, []string{"lint", "build"}, 1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.State != "FAILURE" {
		t.Errorf("state=%q, want FAILURE", got.State)
	}
}
