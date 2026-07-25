package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// GetIndividualCheckRuns feeds the attention sweep's default-branch producer,
// which needs three things beyond pass/fail: when the check concluded (to hold
// a grace period over a failure that is about to be re-run green), where the
// run is (the card's only honest affordance is "go look"), and which commit it
// ran against.

func TestGetIndividualCheckRuns_CarriesTimingURLAndCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"check_runs":[
			{"name":"build","status":"completed","conclusion":"failure",
			 "completed_at":"2026-07-25T10:00:00Z",
			 "html_url":"https://github.com/o/r/runs/1",
			 "details_url":"https://ci.example/1",
			 "head_sha":"abcdef1234567890"},
			{"name":"lint","status":"in_progress","conclusion":null}
		]}`))
	}))
	defer srv.Close()

	runs, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("GetIndividualCheckRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	if runs[0].CompletedAt != "2026-07-25T10:00:00Z" {
		t.Errorf("CompletedAt = %q, want the check's own completion time", runs[0].CompletedAt)
	}
	// html_url is the page a human can read; details_url often points at the
	// provider's API or an app-specific deep link, so it is only the fallback.
	if runs[0].DetailsURL != "https://github.com/o/r/runs/1" {
		t.Errorf("DetailsURL = %q, want the html_url", runs[0].DetailsURL)
	}
	if runs[0].HeadSHA != "abcdef1234567890" {
		t.Errorf("HeadSHA = %q, want the commit the check ran against", runs[0].HeadSHA)
	}
	// A running check has no conclusion and no completion time — the producer
	// relies on that to tell "pending" apart from "failing".
	if runs[1].CompletedAt != "" {
		t.Errorf("CompletedAt = %q on an in-progress check, want empty", runs[1].CompletedAt)
	}
}

func TestGetIndividualCheckRuns_FallsBackToDetailsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"check_runs":[{"name":"build","status":"completed","conclusion":"failure","details_url":"https://ci.example/1"}]}`))
	}))
	defer srv.Close()

	runs, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("GetIndividualCheckRuns: %v", err)
	}
	if runs[0].DetailsURL != "https://ci.example/1" {
		t.Errorf("DetailsURL = %q, want the details_url fallback", runs[0].DetailsURL)
	}
}

// The sweep decides whether a producer failure is that producer's problem or
// the whole repo's by errors.Is against the forge sentinels. Without this
// translation an expired token reads as an ordinary producer failure, and every
// OTHER producer's empty result is then trusted as "the condition cleared" —
// which retracts live cards on an auth blip.
func TestGetIndividualCheckRuns_MapsStatusToForgeSentinels(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		remaining string
		want      error
	}{
		{"expired token", http.StatusUnauthorized, "", forge.ErrUnauthorized},
		{"forbidden", http.StatusForbidden, "42", forge.ErrPermissionDenied},
		{"exhausted quota reported as 403", http.StatusForbidden, "0", forge.ErrRateLimited},
		{"secondary rate limit", http.StatusTooManyRequests, "", forge.ErrRateLimited},
		{"missing repo", http.StatusNotFound, "", forge.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.remaining != "" {
					w.Header().Set("X-RateLimit-Remaining", tc.remaining)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"nope"}`))
			}))
			defer srv.Close()

			_, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "main")
			if err == nil {
				t.Fatalf("status %d returned no error", tc.status)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

func TestGetIndividualCheckRuns_ServerErrorStaysUnclassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "main")
	if err == nil {
		t.Fatal("500 returned no error")
	}
	// A 500 is transient and repo-specific; classifying it as unauthorized or
	// rate-limited would skip the whole sweep over a blip on one endpoint.
	for _, sentinel := range []error{forge.ErrUnauthorized, forge.ErrRateLimited, forge.ErrPermissionDenied, forge.ErrNotFound} {
		if errors.Is(err, sentinel) {
			t.Errorf("500 was classified as %v; it should stay an ordinary producer failure", sentinel)
		}
	}
}
