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

func TestPRCreatedAtAndBody(t *testing.T) {
	c := graphQLStub(t, `{"data":{"repository":{"pullRequest":{"createdAt":"2026-11-01T00:00:00Z","body":"## Acceptance criteria\n\n- x"}}}}`)
	at, body, err := c.PRCreatedAtAndBody(context.Background(), "o", "r", 9)
	if err != nil || !at.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || body != "## Acceptance criteria\n\n- x" {
		t.Fatalf("got %v, %q, %v", at, body, err)
	}
	c = graphQLStub(t, `{"data":{"repository":{"pullRequest":null}}}`)
	if _, _, err := c.PRCreatedAtAndBody(context.Background(), "o", "r", 9); err == nil {
		t.Fatal("missing PR: want an error")
	}
	if _, _, err := c.PRCreatedAtAndBody(context.Background(), "o", "r", 0); err == nil {
		t.Fatal("PR 0: want an error")
	}
}
