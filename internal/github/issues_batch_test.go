package github

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetIssuesByNumbers_EmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be called for empty input")
		http.Error(w, "unexpected call", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClientWithURL("token", srv.URL)
	s := NewIssueService(c)

	got, err := s.GetIssuesByNumbers(context.Background(), "owner", "repo", nil)
	if err != nil {
		t.Fatalf("nil input: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil input: want empty map, got %d entries", len(got))
	}

	got, err = s.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{})
	if err != nil {
		t.Fatalf("empty slice: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty slice: want empty map, got %d entries", len(got))
	}

	// All non-positive entries collapse to empty.
	got, err = s.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{0, -1})
	if err != nil {
		t.Fatalf("non-positive numbers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("non-positive numbers: want empty map, got %d entries", len(got))
	}
}

func TestGetIssuesByNumbers_BatchedAliasQuery(t *testing.T) {
	var receivedQueries []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r.Body); err != nil {
			http.Error(w, "read body", 500)
			return
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
			http.Error(w, "decode", 400)
			return
		}
		q, _ := payload["query"].(string)
		receivedQueries = append(receivedQueries, q)

		// Respond with two issues (101 and 102), with 103 absent (deleted/inaccessible).
		resp := `{
		  "data": {
		    "repository": {
		      "i101": {
		        "id": "I_101",
		        "number": 101,
		        "title": "First",
		        "state": "OPEN",
		        "url": "https://example/101",
		        "labels": {"nodes": [{"name": "size:M"}]},
		        "blockedBy": {"nodes": [{"id": "B1", "number": 50, "title": "Blocker", "state": "OPEN", "repository": {"nameWithOwner": "owner/repo"}}]}
		      },
		      "i102": {
		        "id": "I_102",
		        "number": 102,
		        "title": "Second",
		        "state": "CLOSED",
		        "url": "https://example/102",
		        "labels": {"nodes": []},
		        "blockedBy": {"nodes": []}
		      },
		      "i103": null
		    }
		  }
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := NewClientWithURL("token", srv.URL)
	s := NewIssueService(c)

	out, err := s.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{102, 101, 102, 103})
	if err != nil {
		t.Fatalf("batch fetch: %v", err)
	}

	// Single HTTP call regardless of input length.
	if len(receivedQueries) != 1 {
		t.Fatalf("want 1 GraphQL request, got %d", len(receivedQueries))
	}
	q := receivedQueries[0]

	// Aliased query format: i<NUM>: issue(number: <NUM>) for each unique number,
	// in sorted order (101 before 102 before 103).
	for _, want := range []string{"i101: issue(number: 101)", "i102: issue(number: 102)", "i103: issue(number: 103)"} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing alias %q in:\n%s", want, q)
		}
	}
	// Dedup: 102 appeared twice in input, must appear once in query.
	if strings.Count(q, "i102: issue(number: 102)") != 1 {
		t.Errorf("dedup failed: i102 appears %d times", strings.Count(q, "i102: issue(number: 102)"))
	}
	// Sort: 101 must precede 102.
	if strings.Index(q, "i101:") > strings.Index(q, "i102:") {
		t.Error("query aliases not in sorted order")
	}

	// Result map: 101, 102 present; 103 omitted (null in response).
	if len(out) != 2 {
		t.Fatalf("want 2 issues in result, got %d", len(out))
	}
	iss101, ok := out[101]
	if !ok {
		t.Fatal("missing #101")
	}
	if iss101.State != "OPEN" || iss101.Title != "First" {
		t.Errorf("#101 fields: state=%s title=%s", iss101.State, iss101.Title)
	}
	if len(iss101.Labels) != 1 || iss101.Labels[0] != "size:M" {
		t.Errorf("#101 labels: %v", iss101.Labels)
	}
	if len(iss101.BlockedBy) != 1 || iss101.BlockedBy[0].Number != 50 {
		t.Errorf("#101 blockedBy: %v", iss101.BlockedBy)
	}
	iss102, ok := out[102]
	if !ok {
		t.Fatal("missing #102")
	}
	if iss102.State != "CLOSED" {
		t.Errorf("#102 state: %s", iss102.State)
	}
	if _, has103 := out[103]; has103 {
		t.Error("#103 should be omitted (null in response)")
	}
}

func TestGetIssuesByNumbers_GraphQLErrorWithNoData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"repository":{}},"errors":[{"message":"could not resolve repository"}]}`))
	}))
	defer srv.Close()

	c := NewClientWithURL("token", srv.URL)
	s := NewIssueService(c)

	_, err := s.GetIssuesByNumbers(context.Background(), "owner", "repo", []int{1, 2})
	if err == nil {
		t.Fatal("expected error when repository is empty and errors present")
	}
	if !strings.Contains(err.Error(), "could not resolve repository") {
		t.Errorf("error message did not surface GraphQL error: %v", err)
	}
}
