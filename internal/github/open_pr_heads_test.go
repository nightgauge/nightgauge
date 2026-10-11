package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

// openPRForge serves a repository's open PRs pages pages long, one PR a page
// (number 100+page, head feat/<page>-x). failOn names a page, counted from 1,
// that fails; 0 fails none.
func openPRForge(t *testing.T, pages, failOn int) (*Client, *int) {
	t.Helper()
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		served++
		page := 1
		if after, ok := req.Variables["after"].(string); ok {
			if _, err := fmt.Sscanf(after, "cursor-%d", &page); err != nil {
				t.Errorf("unexpected cursor %q", after)
			}
			page++
		}
		w.Header().Set("Content-Type", "application/json")
		if page == failOn {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Something went wrong while executing your query."}]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
			"repository": map[string]interface{}{"pullRequests": map[string]interface{}{
				"pageInfo": map[string]interface{}{"hasNextPage": page < pages, "endCursor": fmt.Sprintf("cursor-%d", page)},
				"nodes":    []interface{}{map[string]interface{}{"number": 100 + page, "headRefName": fmt.Sprintf("feat/%d-x", page)}},
			}},
		}})
	}))
	t.Cleanup(srv.Close)
	c := NewClientWithURL("test-token", srv.URL)
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	return c, &served
}

// TestListOpenPRHeads reads every page of a repository's open PRs, and a walk
// it cannot finish is an error, never a short list (#1707): the failed-run
// revert reads "no open PR backs this issue" off the result.
func TestListOpenPRHeads(t *testing.T) {
	ctx := context.Background()

	t.Run("every page", func(t *testing.T) {
		c, served := openPRForge(t, 3, 0)
		heads, err := NewPRService(c).ListOpenPRHeads(ctx, "acme", "widgets")
		if err != nil {
			t.Fatalf("ListOpenPRHeads: %v", err)
		}
		if len(heads) != 3 || heads[2] != (OpenPRHead{Number: 103, HeadRefName: "feat/3-x"}) {
			t.Fatalf("heads = %+v, want the 3 pages' PRs", heads)
		}
		if *served != 3 {
			t.Errorf("pages read = %d, want 3", *served)
		}
	})

	t.Run("a later page fails", func(t *testing.T) {
		c, _ := openPRForge(t, 3, 2)
		if heads, err := NewPRService(c).ListOpenPRHeads(ctx, "acme", "widgets"); err == nil {
			t.Fatalf("ListOpenPRHeads = %+v, want an error for the unread page", heads)
		}
	})

	t.Run("more pages than the walk reads", func(t *testing.T) {
		c, served := openPRForge(t, maxOpenPRPages+1, 0)
		if heads, err := NewPRService(c).ListOpenPRHeads(ctx, "acme", "widgets"); err == nil {
			t.Fatalf("ListOpenPRHeads returned %d PRs, want an error past %d pages", len(heads), maxOpenPRPages)
		}
		if *served != maxOpenPRPages {
			t.Errorf("pages read = %d, want %d", *served, maxOpenPRPages)
		}
	})
}
