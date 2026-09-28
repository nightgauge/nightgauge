package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"
)

// #2087: every ledger record names whose bucket paid, as a label derived from
// the credential's owner, never the token.
func TestLedgerRecordsIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	path := withTestLedger(t)

	app := NewClientWithURL("ghs_installationsecret0000", srv.URL+"/graphql")
	app.app = &AppCredentials{AppID: "1", InstallationID: 42, Slug: "nightgauge-pipeline"}
	app.ledgerIdentity.Store(appLedgerIdentity(app.app))

	user := NewClientWithURL("ghp_personalsecret000000", srv.URL+"/graphql").
		WithLedgerIdentity(UserLedgerIdentity("octocat"))

	anon := NewClientWithURL("gho_unresolvedsecret0000", srv.URL+"/graphql")

	ctx := context.Background()
	for _, c := range []*Client{app, user, anon} {
		if _, err := c.restGet(ctx, "/repos/o/r"); err != nil {
			t.Fatalf("restGet: %v", err)
		}
	}

	recs := readLedgerRecords(t, path)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	want := []string{"app:nightgauge-pipeline", "user:octocat", "unknown"}
	for i, r := range recs {
		if got := LedgerIdentity(r.Identity); got != want[i] {
			t.Errorf("record %d identity = %q, want %q", i, got, want[i])
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`gh[pousr]_`).Match(raw) {
		t.Errorf("a token prefix reached the ledger:\n%s", raw)
	}
}

func TestLedgerIdentityLabelRefusesTokens(t *testing.T) {
	for _, tok := range []string{"ghp_abc123", "ghs_abc", "github_pat_11AA", "a b", ""} {
		if got := UserLedgerIdentity(tok); got != "" {
			t.Errorf("UserLedgerIdentity(%q) = %q, want refused", tok, got)
		}
	}
	if got := appLedgerIdentity(&AppCredentials{InstallationID: 7}); got != "app:installation-7" {
		t.Errorf("slugless app identity = %q", got)
	}
	if got := LedgerIdentity(""); got != UnknownLedgerIdentity {
		t.Errorf("old record identity = %q, want unknown", got)
	}
}

func TestSummarizeWindowNamesTheBucketUnderPressure(t *testing.T) {
	w := SummarizeWindow([]APILedgerRecord{
		{Kind: "graphql", Remaining: 4000, HeaderObserved: true, Identity: "app:bot"},
		{Kind: "graphql", Remaining: 12, HeaderObserved: true, Identity: "user:octocat"},
		{Kind: "graphql", Remaining: 3000, HeaderObserved: true},
	}, time.Time{}, time.Time{})
	if w.LowWaterRemaining != 12 || w.LowWaterIdentity != "user:octocat" {
		t.Errorf("low water = %d on %q, want 12 on user:octocat", w.LowWaterRemaining, w.LowWaterIdentity)
	}
}
