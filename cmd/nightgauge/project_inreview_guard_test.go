package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// inReviewGuardForge answers the open-PR list with body; reads counts requests.
func inReviewGuardForge(t *testing.T, body string) (*gh.Client, *int) {
	t.Helper()
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return gh.NewClientWithURL("test-token", srv.URL), &reads
}

func prListJSON(nodes string) string {
	return `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[` + nodes + `]}}}}`
}

func TestRequireOpenPRForInReview(t *testing.T) {
	ctx := context.Background()

	t.Run("open PR of the issue allows In review", func(t *testing.T) {
		c, _ := inReviewGuardForge(t, prListJSON(`{"number":42,"headRefName":"feat/7-x","isCrossRepository":false}`))
		if err := requireOpenPRForInReview(ctx, c, "O", "a", 7, "in-review"); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("no open PR refuses (a merged PR is not in the list)", func(t *testing.T) {
		c, _ := inReviewGuardForge(t, prListJSON(`{"number":43,"headRefName":"feat/8-y","isCrossRepository":false}`))
		err := requireOpenPRForInReview(ctx, c, "O", "a", 7, "in-review")
		if !errors.Is(err, errInReviewWithoutOpenPR) {
			t.Fatalf("err = %v, want errInReviewWithoutOpenPR", err)
		}
	})

	t.Run("a fork's PR does not back the issue", func(t *testing.T) {
		c, _ := inReviewGuardForge(t, prListJSON(`{"number":42,"headRefName":"feat/7-x","isCrossRepository":true}`))
		if err := requireOpenPRForInReview(ctx, c, "O", "a", 7, "in-review"); !errors.Is(err, errInReviewWithoutOpenPR) {
			t.Fatalf("err = %v, want errInReviewWithoutOpenPR", err)
		}
	})

	t.Run("unreadable PR list refuses, but not as 'no PR'", func(t *testing.T) {
		c, _ := inReviewGuardForge(t, `{"errors":[{"message":"boom"}]}`)
		err := requireOpenPRForInReview(ctx, c, "O", "a", 7, "in-review")
		if err == nil || errors.Is(err, errInReviewWithoutOpenPR) || !strings.Contains(err.Error(), "could not list open PRs") {
			t.Fatalf("err = %v, want a list error distinct from the no-PR refusal", err)
		}
	})

	t.Run("other statuses are not checked", func(t *testing.T) {
		c, reads := inReviewGuardForge(t, `{"errors":[{"message":"boom"}]}`)
		for _, s := range []string{"ready", "in-progress", "done", "backlog"} {
			if err := requireOpenPRForInReview(ctx, c, "O", "a", 7, s); err != nil {
				t.Errorf("%s: err = %v, want nil", s, err)
			}
		}
		if *reads != 0 {
			t.Errorf("PR list read %d time(s) for non-In-review statuses", *reads)
		}
	})
}
