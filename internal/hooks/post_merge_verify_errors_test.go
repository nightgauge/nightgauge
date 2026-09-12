package hooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

type statusTransport struct{ server *httptest.Server }

func (t statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(req)
}

type refusedTransport struct{}

func (refusedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

// TestVerifyMergeCommit_MeasurementErrorsAreNeverRed drives the hook through
// the real forge reader (#1691): an auth failure, a 5xx, a network error and
// an unparseable body each mean main was not measured. The verdict is error
// ("could not verify"), never red, and no failing check is invented.
func TestVerifyMergeCommit_MeasurementErrorsAreNeverRed(t *testing.T) {
	// The real client records every request in the API ledger, by default
	// under the working directory — the package source tree. Keep it out.
	t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(t.TempDir(), "github-api.jsonl"))
	serve := func(status int, body string) http.RoundTripper {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return statusTransport{srv}
	}
	cases := map[string]http.RoundTripper{
		"401 unauthorized":     serve(http.StatusUnauthorized, `{"message":"Bad credentials"}`),
		"502":                  serve(http.StatusBadGateway, `{"message":"Server Error"}`),
		"network error":        refusedTransport{},
		"unparseable response": serve(http.StatusOK, `<html>not json</html>`),
	}
	for name, rt := range cases {
		t.Run(name, func(t *testing.T) {
			reader := gh.NewCIService(gh.NewClientWithHTTPClient(&http.Client{Transport: rt}))
			res := VerifyMergeCommit(context.Background(), reader, "o", "r", "main", "abc1234", fastWait(3, 2))
			if res.Verdict != MainChecksError {
				t.Fatalf("Verdict = %q, want %q — a failure to measure is not a red main", res.Verdict, MainChecksError)
			}
			if res.Error == "" {
				t.Error("Error is empty; the reason main was not verified must be recorded")
			}
			if len(res.Failing) != 0 || res.Bad != 0 {
				t.Errorf("Failing = %v Bad = %d, want none — nothing was observed", res.Failing, res.Bad)
			}
		})
	}
}
