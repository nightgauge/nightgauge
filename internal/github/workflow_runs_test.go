package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListWorkflowRuns_Success(t *testing.T) {
	var capturedPath, capturedQuery, capturedAPIVersion string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		capturedAPIVersion = r.Header.Get("X-GitHub-Api-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"workflow_runs": []map[string]interface{}{
				{"id": 1, "name": "CI", "head_branch": "main", "conclusion": "success", "status": "completed"},
				{"id": 2, "name": "CI", "head_branch": "main", "conclusion": "failure", "status": "completed"},
			},
		})
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	runs, err := svc.ListWorkflowRuns(context.Background(), "owner", "repo", "ci.yml", "main", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}

	wantPath := "/repos/owner/repo/actions/workflows/ci.yml/runs"
	if capturedPath != wantPath {
		t.Errorf("path = %q, want %q", capturedPath, wantPath)
	}
	if !strings.Contains(capturedQuery, "branch=main") {
		t.Errorf("query missing branch=main: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "status=completed") {
		t.Errorf("query missing status=completed: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "per_page=5") {
		t.Errorf("query missing per_page=5: %q", capturedQuery)
	}
	if capturedAPIVersion != "2026-03-10" {
		t.Errorf("X-GitHub-Api-Version = %q, want 2026-03-10", capturedAPIVersion)
	}
	if runs[1].Conclusion != "failure" {
		t.Errorf("runs[1].Conclusion = %q, want failure", runs[1].Conclusion)
	}
}

func TestListWorkflowRuns_StripsWorkflowsPrefix(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	if _, err := svc.ListWorkflowRuns(context.Background(), "o", "r", ".github/workflows/ci.yml", "main", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/repos/o/r/actions/workflows/ci.yml/runs"
	if capturedPath != want {
		t.Errorf("path = %q, want %q", capturedPath, want)
	}
}

func TestListWorkflowRuns_DefaultPerPage(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	if _, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "ci.yml", "main", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedQuery, "per_page=5") {
		t.Errorf("default per_page=5 not in query: %q", capturedQuery)
	}
}

func TestListWorkflowRuns_PerPageCappedAt100(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	if _, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "ci.yml", "main", 500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedQuery, "per_page=100") {
		t.Errorf("per_page should be capped at 100, got query: %q", capturedQuery)
	}
}

func TestListWorkflowRuns_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	_, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "missing.yml", "main", 5)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestListWorkflowRuns_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"Internal Server Error"}`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	_, err := svc.ListWorkflowRuns(context.Background(), "o", "r", "ci.yml", "main", 5)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestListRunJobs_Success(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jobs": []map[string]interface{}{
				{"id": 10, "name": "Integration & E2E Tests", "status": "completed", "conclusion": "failure"},
				{"id": 11, "name": "Unit Tests", "status": "completed", "conclusion": "success"},
			},
		})
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	jobs, err := svc.ListRunJobs(context.Background(), "owner", "repo", 12345)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(jobs))
	}
	if jobs[0].Name != "Integration & E2E Tests" || jobs[0].Conclusion != "failure" {
		t.Errorf("first job mismatch: %+v", jobs[0])
	}
	want := "/repos/owner/repo/actions/runs/12345/jobs"
	if capturedPath != want {
		t.Errorf("path = %q, want %q", capturedPath, want)
	}
}

func TestListRunJobs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`bad`))
	}))
	defer srv.Close()

	svc := newCIServiceForRESTTest(srv)
	_, err := svc.ListRunJobs(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
