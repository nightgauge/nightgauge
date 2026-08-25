package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureProbe runs ProjectUpdatedAt against a stub GraphQL server and returns
// the query body the client actually SENT, plus the parsed result.
//
// The sent body is the point. Every assertion about cost, and every hand-run
// `gh api graphql` check, is about a query a human wrote; the one that reaches
// GitHub in production is generated from the struct tags by shurcooL/graphql,
// and the two are only equal until someone edits a tag. #916 shipped a bug of
// exactly this shape — a `first: 250` that no fake could reject, because a fake
// has no page limit.
func captureProbe(t *testing.T, ownerType OwnerType, respBody string) (string, time.Time, error) {
	t.Helper()
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &payload)
		sent = payload.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)

	b := NewBoardService(NewClientWithURL("t0ken", srv.URL), "acme", 3, ownerType)
	ts, err := b.ProjectUpdatedAt(context.Background())
	return sent, ts, err
}

func TestProbeQueryIsOneFieldAndSelectsNoConnection(t *testing.T) {
	cases := []struct {
		name      string
		ownerType OwnerType
		root      string
		body      string
	}{
		{"org", OwnerTypeOrg, "organization", `{"data":{"organization":{"projectV2":{"updatedAt":"2026-08-25T19:23:41Z"}}}}`},
		{"user", OwnerTypeUser, "user", `{"data":{"user":{"projectV2":{"updatedAt":"2026-08-25T19:23:41Z"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent, ts, err := captureProbe(t, tc.ownerType, tc.body)
			if err != nil {
				t.Fatalf("ProjectUpdatedAt: %v", err)
			}
			want := time.Date(2026, 8, 25, 19, 23, 41, 0, time.UTC)
			if !ts.Equal(want) {
				t.Errorf("parsed %v, want %v", ts, want)
			}
			if !strings.Contains(sent, tc.root+"(login:") {
				t.Errorf("query does not root at %s: %s", tc.root, sent)
			}
			if !strings.Contains(sent, "updatedAt") {
				t.Errorf("query does not select updatedAt: %s", sent)
			}
			// The cost claim IS the feature. A connection selection is what
			// makes a GraphQL query expensive, so a probe that grows one has
			// silently stopped being a 1-point probe while every test that
			// only checks the parsed timestamp keeps passing.
			for _, forbidden := range []string{"items(", "totalCount", "first:", "fieldValues", "nodes"} {
				if strings.Contains(sent, forbidden) {
					t.Errorf("probe query contains %q — it is no longer the cheap probe #847 is built on: %s", forbidden, sent)
				}
			}
		})
	}
}

// A probe that cannot look must SAY so. Returning the zero time with a nil
// error would read to the cache as "the board changed at the epoch" — which,
// being in the past, renews the snapshot forever. Fail-open in the cache is
// only as good as the error this returns.
func TestProbeReportsRatherThanZeroesAnUnreadableTimestamp(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty timestamp", `{"data":{"organization":{"projectV2":{"updatedAt":""}}}}`},
		{"unparseable timestamp", `{"data":{"organization":{"projectV2":{"updatedAt":"last Tuesday"}}}}`},
		{"graphql error", `{"errors":[{"message":"Could not resolve to a ProjectV2 with the number 3."}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ts, err := captureProbe(t, OwnerTypeOrg, tc.body)
			if err == nil {
				t.Fatalf("ProjectUpdatedAt returned (%v, nil); an unreadable probe must error, not answer", ts)
			}
			if !ts.IsZero() {
				t.Errorf("errored probe also returned a timestamp %v, want zero", ts)
			}
		})
	}
}
