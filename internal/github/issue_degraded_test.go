package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetIssueWithRelations_DegradedResponseIsAnError: an HTTP 200 whose issue
// is null or partial must be an error, not a zero-valued issue with a nil
// error. The partial shape is the #1915 incident: labels present, title empty,
// which branch-create turned into `fix/1911-`.
func TestGetIssueWithRelations_DegradedResponseIsAnError(t *testing.T) {
	cases := map[string]string{
		"null issue": `{"data":{"repository":{"issue":null}}}`,
		"labels without a title": `{"data":{"repository":{"issue":{"id":"I_1911","number":1911,"title":"",` +
			`"labels":{"nodes":[{"name":"type:bug"}]}}}}}`,
		"another issue's number": `{"data":{"repository":{"issue":{"id":"I_7","number":7,"title":"Other"}}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)

			svc := NewIssueService(NewClientWithURL("test-token", srv.URL))
			got, err := svc.GetIssueWithRelations(context.Background(), "acme", "widgets", 1911, NoRelations)
			if err == nil {
				t.Fatalf("GetIssueWithRelations returned %+v with a nil error, want an error", got)
			}
			if !strings.Contains(err.Error(), "#1911") || !strings.Contains(err.Error(), "incomplete") {
				t.Errorf("error does not name the issue and the incomplete response: %v", err)
			}
		})
	}
}
