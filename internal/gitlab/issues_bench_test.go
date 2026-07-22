package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const benchIssueCount = 50

// buildBenchIssues constructs a slice of iids 1..n for benchmarks.
func buildBenchIssues(n int) []int {
	iids := make([]int, n)
	for i := range iids {
		iids[i] = i + 1
	}
	return iids
}

// BenchmarkGetIssuesByNumbers_aliased measures the aliased GraphQL batch path.
// One GraphQL call for all benchIssueCount issues.
func BenchmarkGetIssuesByNumbers_aliased(b *testing.B) {
	iids := buildBenchIssues(benchIssueCount)

	// Build a static aliased response for all iids.
	projectData := make(map[string]interface{}, benchIssueCount)
	for _, iid := range iids {
		aliasKey := fmt.Sprintf("iid_%d", iid)
		projectData[aliasKey] = map[string]interface{}{
			"iid":         fmt.Sprintf("%d", iid),
			"title":       fmt.Sprintf("Issue %d", iid),
			"description": "",
			"state":       "opened",
			"webUrl":      fmt.Sprintf("https://gitlab.example.com/issues/%d", iid),
			"labels":      map[string]interface{}{"nodes": []interface{}{}},
			"assignees":   map[string]interface{}{"nodes": []interface{}{}},
		}
	}
	resp := map[string]interface{}{
		"data": map[string]interface{}{"project": projectData},
	}
	respBytes, _ := json.Marshal(resp)

	var callCounter int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&callCounter, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(srv.URL, "bench-token")
	svc := NewIssueService(client)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		atomic.StoreInt64(&callCounter, 0)
		_, _ = svc.getIssuesByAliasedBatch(context.Background(), "owner", "repo", iids)
		count := atomic.LoadInt64(&callCounter)
		if count != 1 {
			b.Fatalf("aliased batch made %d calls, expected 1", count)
		}
	}
	b.ReportMetric(float64(benchIssueCount), "issues/op")
	b.ReportMetric(1, "graphql_calls/op")
}

// BenchmarkGetIssuesByNumbers_serial measures the serial REST fallback path.
// benchIssueCount REST calls for benchIssueCount issues.
func BenchmarkGetIssuesByNumbers_serial(b *testing.B) {
	iids := buildBenchIssues(benchIssueCount)

	var callCounter int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		// Skip link fetch calls from the call counter (they are overhead, not round trips per issue).
		if strings.Contains(r.URL.Path, "links") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		atomic.AddInt64(&callCounter, 1)
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		iidStr := parts[len(parts)-1]
		iidInt := 0
		fmt.Sscan(iidStr, &iidInt)
		resp := map[string]interface{}{
			"id":          int64(iidInt),
			"iid":         iidInt,
			"project_id":  42,
			"title":       fmt.Sprintf("Issue %d", iidInt),
			"description": "",
			"state":       "opened",
			"web_url":     fmt.Sprintf("https://gitlab.example.com/issues/%d", iidInt),
			"labels":      []string{},
			"assignees":   []interface{}{},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(srv.URL, "bench-token")
	svc := NewIssueService(client)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		atomic.StoreInt64(&callCounter, 0)
		_, _ = svc.getIssuesByNumbersFallback(context.Background(), "owner", "repo", iids)
		count := atomic.LoadInt64(&callCounter)
		if count < int64(benchIssueCount) {
			b.Logf("serial fallback made %d REST calls for %d issues", count, benchIssueCount)
		}
	}
	b.ReportMetric(float64(benchIssueCount), "issues/op")
}

// TestBenchmark_roundTripReduction verifies the >10x call-count reduction at
// test (non-benchmark) level using mock call counters.
func TestBenchmark_roundTripReduction(t *testing.T) {
	iids := buildBenchIssues(benchIssueCount)

	// --- Aliased batch: measure GraphQL call count ---
	var aliasedCalls int64
	projectData := make(map[string]interface{}, benchIssueCount)
	for _, iid := range iids {
		aliasKey := fmt.Sprintf("iid_%d", iid)
		projectData[aliasKey] = map[string]interface{}{
			"iid":         fmt.Sprintf("%d", iid),
			"title":       fmt.Sprintf("Issue %d", iid),
			"description": "",
			"state":       "opened",
			"webUrl":      fmt.Sprintf("https://gitlab.example.com/issues/%d", iid),
			"labels":      map[string]interface{}{"nodes": []interface{}{}},
			"assignees":   map[string]interface{}{"nodes": []interface{}{}},
		}
	}
	aliasedResp := map[string]interface{}{
		"data": map[string]interface{}{"project": projectData},
	}
	aliasedBytes, _ := json.Marshal(aliasedResp)

	aliasedMux := http.NewServeMux()
	aliasedMux.HandleFunc("/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&aliasedCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(aliasedBytes)
	})
	aliasedSrv := httptest.NewServer(aliasedMux)
	defer aliasedSrv.Close()

	aliasedClient := NewClient(aliasedSrv.URL, "test-token")
	aliasedSvc := NewIssueService(aliasedClient)
	_, _ = aliasedSvc.getIssuesByAliasedBatch(context.Background(), "owner", "repo", iids)

	// --- Serial fallback: measure REST call count ---
	var serialCalls int64
	serialMux := http.NewServeMux()
	serialMux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "links") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		atomic.AddInt64(&serialCalls, 1)
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		iidStr := parts[len(parts)-1]
		iidInt := 0
		fmt.Sscan(iidStr, &iidInt)
		resp := map[string]interface{}{
			"id":          int64(iidInt),
			"iid":         iidInt,
			"project_id":  42,
			"title":       fmt.Sprintf("Issue %d", iidInt),
			"description": "",
			"state":       "opened",
			"web_url":     "",
			"labels":      []string{},
			"assignees":   []interface{}{},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
	serialSrv := httptest.NewServer(serialMux)
	defer serialSrv.Close()

	serialClient := NewClient(serialSrv.URL, "test-token")
	serialSvc := NewIssueService(serialClient)
	_, _ = serialSvc.getIssuesByNumbersFallback(context.Background(), "owner", "repo", iids)

	aliasedCount := atomic.LoadInt64(&aliasedCalls)
	serialCount := atomic.LoadInt64(&serialCalls)

	t.Logf("aliased batch: %d call(s) for %d issues", aliasedCount, benchIssueCount)
	t.Logf("serial REST:   %d call(s) for %d issues", serialCount, benchIssueCount)

	if aliasedCount != 1 {
		t.Fatalf("aliased batch should make exactly 1 call, made %d", aliasedCount)
	}
	if serialCount < int64(benchIssueCount) {
		t.Fatalf("serial fallback should make at least %d calls, made %d", benchIssueCount, serialCount)
	}

	ratio := float64(serialCount) / float64(aliasedCount)
	if ratio < 10 {
		t.Fatalf("expected >10x round-trip reduction, got %.1fx (serial=%d aliased=%d)",
			ratio, serialCount, aliasedCount)
	}
	t.Logf("Round-trip reduction: %.1fx", ratio)
}
