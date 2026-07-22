package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphqlAliasedResponse builds a mock GraphQL response for aliased iid queries.
func graphqlAliasedResponse(iids []int) []byte {
	projectData := map[string]interface{}{}
	for _, iid := range iids {
		aliasKey := fmt.Sprintf("iid_%d", iid)
		projectData[aliasKey] = map[string]interface{}{
			"iid":         fmt.Sprintf("%d", iid),
			"title":       fmt.Sprintf("Issue %d", iid),
			"description": fmt.Sprintf("Body of issue %d", iid),
			"state":       "opened",
			"webUrl":      fmt.Sprintf("https://gitlab.example.com/issues/%d", iid),
			"labels": map[string]interface{}{
				"nodes": []map[string]interface{}{
					{"title": "bug"},
				},
			},
			"assignees": map[string]interface{}{
				"nodes": []map[string]interface{}{},
			},
		}
	}
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"project": projectData,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// mockGraphQLServer returns a test server that responds to POST /api/graphql.
func mockGraphQLServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/graphql", handler)
	return httptest.NewServer(mux)
}

func TestGetIssuesByNumbers_aliasedBatch(t *testing.T) {
	callCount := 0
	srv := mockGraphQLServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(graphqlAliasedResponse([]int{1, 2, 3}))
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	svc := NewIssueService(client)

	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{1, 2, 3})
	if err != nil {
		t.Fatalf("GetIssuesByNumbers: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(issues))
	}
	// All 3 fetched in exactly 1 GraphQL call.
	if callCount != 1 {
		t.Fatalf("expected 1 GraphQL call, got %d (aliased batch broken)", callCount)
	}
	for _, iid := range []int{1, 2, 3} {
		issue, ok := issues[iid]
		if !ok {
			t.Fatalf("missing issue %d in result", iid)
		}
		if issue.Number != iid {
			t.Fatalf("issue %d has wrong Number: %d", iid, issue.Number)
		}
		expectedTitle := fmt.Sprintf("Issue %d", iid)
		if issue.Title != expectedTitle {
			t.Fatalf("issue %d title: got %q, want %q", iid, issue.Title, expectedTitle)
		}
	}
}

func TestGetIssuesByNumbers_nullAlias(t *testing.T) {
	srv := mockGraphQLServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Return iid_1 as null (issue doesn't exist), iid_2 as valid.
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"project": map[string]interface{}{
					"iid_1": nil,
					"iid_2": map[string]interface{}{
						"iid":         "2",
						"title":       "Issue 2",
						"description": "",
						"state":       "opened",
						"webUrl":      "https://gitlab.example.com/issues/2",
						"labels":      map[string]interface{}{"nodes": []interface{}{}},
						"assignees":   map[string]interface{}{"nodes": []interface{}{}},
					},
				},
			},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	svc := NewIssueService(client)

	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{1, 2})
	if err != nil {
		t.Fatalf("GetIssuesByNumbers: %v", err)
	}
	// iid_1 is null — should be skipped.
	if _, ok := issues[1]; ok {
		t.Fatalf("null alias iid=1 should be skipped")
	}
	if issues[2] == nil {
		t.Fatalf("iid=2 should be returned")
	}
}

func TestGetIssuesByNumbers_fallback(t *testing.T) {
	// GraphQL returns an error — should fall back to serial REST.
	restCallCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Return GraphQL error, no data.
		resp := map[string]interface{}{
			"errors": []map[string]interface{}{
				{"message": "Field 'iid_1' doesn't exist on type 'Project'"},
			},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		restCallCount++
		// Extract the issue iid from the path (last path segment).
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		iidStr := parts[len(parts)-1]
		iidInt := 0
		fmt.Sscan(iidStr, &iidInt)
		// If it looks like a links endpoint, return empty list.
		if strings.Contains(r.URL.Path, "links") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		}
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

	client := NewClient(srv.URL, "test-token")
	svc := NewIssueService(client)

	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{1, 2})
	if err != nil {
		t.Fatalf("GetIssuesByNumbers with fallback: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues via fallback, got %d", len(issues))
	}
	// REST was called once per issue (2 calls) + 1 links call per issue.
	if restCallCount < 2 {
		t.Fatalf("expected at least 2 REST calls for fallback, got %d", restCallCount)
	}
}

func TestGetIssuesByNumbers_deduplication(t *testing.T) {
	callCount := 0
	srv := mockGraphQLServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(graphqlAliasedResponse([]int{5}))
	})
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	svc := NewIssueService(client)

	// Pass duplicates — should dedup to one issue.
	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{5, 5, 5})
	if err != nil {
		t.Fatalf("GetIssuesByNumbers: %v", err)
	}
	if len(issues) > 1 {
		t.Fatalf("duplicates should be deduplicated: got %d issues", len(issues))
	}
}

func TestGetIssuesByNumbers_empty(t *testing.T) {
	client := NewClient("", "test-token")
	svc := NewIssueService(client)

	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{})
	if err != nil {
		t.Fatalf("empty input should return no error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("empty input should return empty map, got %d", len(issues))
	}
}

func TestGetIssuesByNumbers_negativeFiltered(t *testing.T) {
	client := NewClient("", "test-token")
	svc := NewIssueService(client)

	issues, err := svc.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{0, -1, -5})
	if err != nil {
		t.Fatalf("non-positive iids should return no error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("non-positive iids should produce empty result, got %d", len(issues))
	}
}
