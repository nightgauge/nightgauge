package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListBaselineRuns pins #2055's baseline: the frozen push runs on main
// age out behind the pull_request runs that gated the last merges, and a
// merged head with no run of the workflow contributes nothing.
func TestListBaselineRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/o/r/pulls":
			if q.Get("base") != "main" || q.Get("state") != "closed" {
				t.Errorf("pulls query = %v", q)
			}
			_, _ = w.Write([]byte(`[
				{"merged_at":"2026-09-20T00:00:00Z","head":{"sha":"h1"}},
				{"merged_at":null,"head":{"sha":"open-or-closed-unmerged"}},
				{"merged_at":"2026-09-22T00:00:00Z","head":{"sha":"h2"}},
				{"merged_at":"2026-09-21T00:00:00Z","head":{"sha":"h-no-run"}}]`))
		case r.URL.Path == "/repos/o/r/actions/workflows/ci.yml/runs" && q.Get("head_sha") != "":
			if q.Get("event") != "pull_request" || q.Get("status") != "completed" {
				t.Errorf("head runs query = %v", q)
			}
			switch q.Get("head_sha") {
			case "h1":
				_, _ = w.Write([]byte(`{"workflow_runs":[{"id":11,"status":"completed","conclusion":"success","created_at":"2026-09-19T12:00:00Z","head_sha":"h1"}]}`))
			case "h2":
				_, _ = w.Write([]byte(`{"workflow_runs":[{"id":12,"status":"completed","conclusion":"success","created_at":"2026-09-21T12:00:00Z","head_sha":"h2"}]}`))
			default:
				_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
			}
		case r.URL.Path == "/repos/o/r/actions/workflows/ci.yml/runs":
			if q.Get("branch") != "main" {
				t.Errorf("branch runs query = %v", q)
			}
			// The frozen pre-#2055 push runs: red, and older than every PR run.
			_, _ = w.Write([]byte(`{"workflow_runs":[
				{"id":1,"status":"completed","conclusion":"failure","created_at":"2026-09-01T00:00:00Z"},
				{"id":2,"status":"completed","conclusion":"failure","created_at":"2026-08-31T00:00:00Z"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	runs, err := newCIServiceForRESTTest(srv).ListBaselineRuns(context.Background(), "o", "r", ".github/workflows/ci.yml", "main", 3)
	if err != nil {
		t.Fatalf("ListBaselineRuns: %v", err)
	}
	var ids []int64
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	want := []int64{12, 11, 1}
	if len(ids) != len(want) {
		t.Fatalf("run ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("run ids = %v, want %v (newest first, cut to n)", ids, want)
		}
	}
}
