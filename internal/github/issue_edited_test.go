package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func graphQLStub(t *testing.T, payload string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL)
}

func TestIssueLastEditedAt(t *testing.T) {
	c := graphQLStub(t, `{"data":{"repository":{"issue":{"lastEditedAt":"2026-11-02T03:04:05Z"}}}}`)
	got, err := c.IssueLastEditedAt(context.Background(), "o", "r", 7)
	if err != nil || got == nil || !got.Equal(time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("got %v, %v", got, err)
	}
	c = graphQLStub(t, `{"data":{"repository":{"issue":{"lastEditedAt":null}}}}`)
	if got, err := c.IssueLastEditedAt(context.Background(), "o", "r", 7); err != nil || got != nil {
		t.Fatalf("never edited: got %v, %v", got, err)
	}
	c = graphQLStub(t, `{"data":{"repository":{"issue":null}}}`)
	if _, err := c.IssueLastEditedAt(context.Background(), "o", "r", 7); err == nil {
		t.Fatal("missing issue: want an error")
	}
}

func TestPartialPR(t *testing.T) {
	c := graphQLStub(t, `{"data":{"repository":{"pullRequest":{"createdAt":"2026-11-01T00:00:00Z","body":"## Acceptance criteria\n\n- x","mergeCommit":{"oid":"abc123"}}}}}`)
	got, err := c.PartialPR(context.Background(), "o", "r", 9)
	if err != nil || !got.CreatedAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) ||
		got.Body != "## Acceptance criteria\n\n- x" || got.MergeSHA != "abc123" {
		t.Fatalf("got %+v, %v", got, err)
	}
	c = graphQLStub(t, `{"data":{"repository":{"pullRequest":{"createdAt":"2026-11-01T00:00:00Z","body":"","mergeCommit":null}}}}`)
	if got, err := c.PartialPR(context.Background(), "o", "r", 9); err != nil || got.MergeSHA != "" {
		t.Fatalf("unmerged PR: got %+v, %v", got, err)
	}
	c = graphQLStub(t, `{"data":{"repository":{"pullRequest":null}}}`)
	if _, err := c.PartialPR(context.Background(), "o", "r", 9); err == nil {
		t.Fatal("missing PR: want an error")
	}
	if _, err := c.PartialPR(context.Background(), "o", "r", 0); err == nil {
		t.Fatal("PR 0: want an error")
	}
}
