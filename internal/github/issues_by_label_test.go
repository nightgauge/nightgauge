package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

// ListIssuesByLabel reads every page, both states, and the close reason
// (#2573): a program's status is counted over its whole issue set.
func TestListIssuesByLabel_ReadsEveryPageAndBothStates(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		queries = append(queries, req.Query)
		page := map[string]any{
			"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"},
			"nodes": []any{map[string]any{
				"id": "I1", "number": 1, "title": "one", "state": "OPEN", "stateReason": "", "url": "u1",
				"labels": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"name": "program:x"}}},
			}},
		}
		if req.Variables["after"] == "c1" {
			page = map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes": []any{map[string]any{
					"id": "I2", "number": 2, "title": "two", "state": "CLOSED", "stateReason": "COMPLETED", "url": "u2",
					"labels": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"name": "program:x"}}},
				}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"issues": page}}})
	}))
	defer srv.Close()
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)

	got, err := NewIssueService(c).ListIssuesByLabel(context.Background(), "o", "a", "program:x")
	if err != nil {
		t.Fatalf("ListIssuesByLabel: %v", err)
	}
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 {
		t.Fatalf("issues = %+v, want #1 then #2 across two pages", got)
	}
	if got[1].State != "CLOSED" || got[1].StateReason != "COMPLETED" || got[1].Repo != "o/a" {
		t.Errorf("#2 = %+v, want CLOSED/COMPLETED in o/a", got[1])
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "states: [OPEN, CLOSED]") {
		t.Errorf("queries = %q, want two reads of both states", queries)
	}
}
