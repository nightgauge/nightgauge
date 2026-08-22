package platform

// Live-contract tests for GET /v1/analytics/trends and GET /v1/analytics/runs
// (#801).
//
// Every fixture below is the body the PLATFORM ROUTE ACTUALLY BUILDS, copied
// from its handler and its own route tests — not a shape invented here. That
// distinction is the whole point of this file. Three consecutive bugs in this
// class (the Health tab's response shape, #800's parameter format, and both
// mismatches fixed here) shared one cause: the fixture and the client were
// written from the same belief, so they agreed with each other and disagreed
// with production.
//
// For /runs the published OpenAPI document is ALSO part of the problem. It
// declares `{items, has_more, next_cursor}`; the route returns
// `{runs, nextCursor}`. See TestGetAnalyticsRuns_SpecShapedBodyStaysEmpty.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// runsPageBody is the live GET /v1/analytics/runs 200 body: the route returns
// its query service's ListRunsResult verbatim.
const runsPageBody = `{
  "runs": [
    {
      "runId": "00000000-0000-4000-8000-000000000001",
      "issueNumber": 801,
      "repoFullName": "nightgauge/nightgauge",
      "branch": "fix/801-trends-runs-live-contract",
      "status": "success",
      "outcomeType": null,
      "startedAt": "2026-08-21T09:00:00.000Z",
      "completedAt": "2026-08-21T09:14:00.000Z",
      "totalDurationMs": 840000,
      "cost": 1250000,
      "routingComplexity": "moderate",
      "routingPath": ["plan", "implement"],
      "backtracks": 0,
      "currentStage": null,
      "origin": "local_cli",
      "performanceMode": "elevated",
      "issueTitle": "Trends decodes a contract the platform does not return",
      "adapterUsage": [{"adapter": "claude", "stageCount": 6}]
    },
    {
      "runId": "00000000-0000-4000-8000-000000000002",
      "issueNumber": 797,
      "repoFullName": "nightgauge/nightgauge",
      "branch": null,
      "status": "failure",
      "outcomeType": "blocked",
      "startedAt": "2026-08-20T11:00:00.000Z",
      "completedAt": null,
      "totalDurationMs": null,
      "cost": null,
      "routingComplexity": null,
      "routingPath": null,
      "backtracks": 2,
      "currentStage": "pr-merge",
      "origin": "local_cli",
      "performanceMode": null,
      "issueTitle": null,
      "adapterUsage": []
    }
  ],
  "nextCursor": "MjAyNi0wOC0yMA"
}`

func runsServer(t *testing.T, body string, gotQuery *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/analytics/runs" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
}

func runsService(base string) *AnalyticsService {
	return &AnalyticsService{client: &Client{base: base, sessionToken: "jwt.test", mode: ModeOnline}}
}

func TestGetAnalyticsRuns_DecodesTheLiveRunsEnvelope(t *testing.T) {
	srv := runsServer(t, runsPageBody, nil)
	defer srv.Close()

	got, err := runsService(srv.URL).GetAnalyticsRuns(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2 — a 200 that decodes to nothing is the bug this test exists for", len(got.Entries))
	}

	first := got.Entries[0]
	if first.IssueNumber != 801 {
		t.Errorf("IssueNumber = %d, want 801", first.IssueNumber)
	}
	if first.Title != "Trends decodes a contract the platform does not return" {
		t.Errorf("Title = %q, want the issueTitle column", first.Title)
	}
	if first.Branch != "fix/801-trends-runs-live-contract" {
		t.Errorf("Branch = %q", first.Branch)
	}
	if first.Outcome != "success" {
		t.Errorf("Outcome = %q, want the status column", first.Outcome)
	}
	if first.DurationMs != 840000 {
		t.Errorf("DurationMs = %d, want 840000", first.DurationMs)
	}
	// cost is INTEGER micro-dollars on the platform: 1_250_000 => $1.25.
	if first.TotalCostUsd != "1.25" {
		t.Errorf("TotalCostUsd = %q, want \"1.25\" (micro-dollars ÷ 1e6)", first.TotalCostUsd)
	}
	if first.StartedAt != "2026-08-21T09:00:00.000Z" {
		t.Errorf("StartedAt = %q", first.StartedAt)
	}

	if !got.HasMore {
		t.Error("HasMore = false; a non-null nextCursor is the endpoint's only 'another page' signal")
	}
	if got.NextCursor != "MjAyNi0wOC0yMA" {
		t.Errorf("NextCursor = %q", got.NextCursor)
	}
}

func TestGetAnalyticsRuns_MapsNullableColumns(t *testing.T) {
	srv := runsServer(t, runsPageBody, nil)
	defer srv.Close()

	got, err := runsService(srv.URL).GetAnalyticsRuns(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	second := got.Entries[1]

	// outcome_type refines a failed run and is the more specific answer, so it
	// must win over status rather than being dropped.
	if second.Outcome != "blocked" {
		t.Errorf("Outcome = %q, want \"blocked\" — outcomeType outranks status", second.Outcome)
	}
	if second.Branch != "" {
		t.Errorf("Branch = %q, want empty for a null column", second.Branch)
	}
	if second.DurationMs != 0 {
		t.Errorf("DurationMs = %d, want 0 for a null column", second.DurationMs)
	}
	if second.TotalCostUsd != "" {
		t.Errorf("TotalCostUsd = %q, want empty for a null cost — the tab renders an em dash, not $0.00", second.TotalCostUsd)
	}
	// A null issueTitle must not render as a blank cell.
	if second.Title != "#797" {
		t.Errorf("Title = %q, want \"#797\" when issueTitle is null", second.Title)
	}
}

// TestGetAnalyticsRuns_SpecShapedBodyStaysEmpty pins the finding that made this
// fix different from the ones before it.
//
// The platform's published OpenAPI document declares this operation's 200 body
// as {items, has_more, next_cursor}. The route does not build that envelope,
// and the platform's own route tests assert `json.runs` / `json.nextCursor`.
// The spec is a hand-authored overlay, so it drifted from the implementation.
//
// Had this client been "fixed" by following the published contract — the rule
// that produced the previous fixes in this class — the Runs tab would have
// stayed exactly as empty as it was. A hand-written spec is a stub too; the
// only authority that cannot drift is what the server actually returns.
func TestGetAnalyticsRuns_SpecShapedBodyStaysEmpty(t *testing.T) {
	specBody := `{
	  "items": [{"id": "00000000-0000-4000-8000-000000000001", "issueNumber": 123,
	             "success": true, "completedAt": "2026-04-26T09:00:00Z"}],
	  "has_more": false,
	  "next_cursor": null
	}`
	srv := runsServer(t, specBody, nil)
	defer srv.Close()

	got, err := runsService(srv.URL).GetAnalyticsRuns(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("Entries = %d; this test documents that the PUBLISHED spec shape "+
			"carries no data for this client — if it ever does, the platform "+
			"changed its route and this file must be re-derived from the new one",
			len(got.Entries))
	}
}

// TestGetAnalyticsRuns_SendsOnlyDocumentedQueryParameters guards the second half
// of the defect: the endpoint declares limit and cursor and nothing else, and
// its query schema silently discards anything further. Four filters
// (startDate/endDate/outcome/branch) were being sent and dropped, so the tab
// presented an unfiltered page as a filtered one.
func TestGetAnalyticsRuns_SendsOnlyDocumentedQueryParameters(t *testing.T) {
	var q url.Values
	srv := runsServer(t, runsPageBody, &q)
	defer srv.Close()

	if _, err := runsService(srv.URL).GetAnalyticsRuns(context.Background(), "cur-42", 20); err != nil {
		t.Fatal(err)
	}

	documented := map[string]bool{"cursor": true, "limit": true}
	for name := range q {
		if !documented[name] {
			t.Errorf("sent undocumented query parameter %q=%v; /v1/analytics/runs "+
				"accepts only limit and cursor and ignores the rest", name, q[name])
		}
	}
	if q.Get("cursor") != "cur-42" {
		t.Errorf("cursor = %q, want cur-42", q.Get("cursor"))
	}
	if q.Get("limit") != "20" {
		t.Errorf("limit = %q, want 20", q.Get("limit"))
	}
}

// --- trends ---

// trendsTokensBody / trendsSuccessRateBody are the two envelopes the route's
// buildEnvelope() emits, one per metric. Note the endpoint buckets by
// (timestamp, repo): two repos on one day are two rows.
const trendsTokensBody = `{
  "granularity": "daily",
  "dateFrom": "2026-07-23T00:00:00.000Z",
  "dateTo": "2026-08-22T00:00:00.000Z",
  "repos": ["nightgauge/nightgauge", "acme/widgets"],
  "data": [
    {"timestamp": "2026-08-20", "repo": "nightgauge/nightgauge",
     "inputTokens": 0, "outputTokens": 184000, "cacheReadTokens": 0},
    {"timestamp": "2026-08-20", "repo": "acme/widgets",
     "inputTokens": 0, "outputTokens": 16000, "cacheReadTokens": 0},
    {"timestamp": "2026-08-21", "repo": "nightgauge/nightgauge",
     "inputTokens": 0, "outputTokens": 90000, "cacheReadTokens": 0}
  ]
}`

const trendsSuccessRateBody = `{
  "granularity": "daily",
  "dateFrom": "2026-07-23T00:00:00.000Z",
  "dateTo": "2026-08-22T00:00:00.000Z",
  "repos": ["nightgauge/nightgauge", "acme/widgets"],
  "targetSuccessRate": 95,
  "data": [
    {"timestamp": "2026-08-20", "repo": "nightgauge/nightgauge",
     "successCount": 9, "failureCount": 1, "cancelledCount": 0, "successRate": 90},
    {"timestamp": "2026-08-20", "repo": "acme/widgets",
     "successCount": 0, "failureCount": 1, "cancelledCount": 0, "successRate": 0},
    {"timestamp": "2026-08-21", "repo": "nightgauge/nightgauge",
     "successCount": 4, "failureCount": 0, "cancelledCount": 0, "successRate": 100}
  ]
}`

func trendsServer(t *testing.T, queries *[]url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/analytics/trends" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if queries != nil {
			*queries = append(*queries, r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		// The endpoint answers exactly one metric per request.
		body := trendsTokensBody
		if r.URL.Query().Get("metric") == "success_rate" {
			body = trendsSuccessRateBody
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
}

func TestGetAnalyticsTrends_MergesBothMetricEnvelopes(t *testing.T) {
	srv := trendsServer(t, nil)
	defer srv.Close()

	got, err := runsService(srv.URL).GetAnalyticsTrends(context.Background(), "30d")
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2 (one per timestamp, summed across repos)", len(got.Entries))
	}
	if got.Entries[0].Date != "2026-08-20" || got.Entries[1].Date != "2026-08-21" {
		t.Fatalf("entries out of order: %q, %q", got.Entries[0].Date, got.Entries[1].Date)
	}

	day := got.Entries[0]
	if day.TotalTokens != 200000 {
		t.Errorf("TotalTokens = %d, want 200000 (184000 + 16000 across both repos)", day.TotalTokens)
	}
	if day.TotalRuns != 11 {
		t.Errorf("TotalRuns = %d, want 11 (9+1 and 0+1)", day.TotalRuns)
	}
	// 9 of 11 succeeded. Averaging the per-repo percentages (90 and 0) would
	// give 45 — weighting a one-run repo the same as a ten-run one.
	if day.SuccessRate != 81.8 {
		t.Errorf("SuccessRate = %v, want 81.8 (run-weighted, not the 45 an average of percentages gives)", day.SuccessRate)
	}

	if got.TargetSuccessRate != 95 {
		t.Errorf("TargetSuccessRate = %v, want 95", got.TargetSuccessRate)
	}
	if got.Granularity != "daily" {
		t.Errorf("Granularity = %q", got.Granularity)
	}
	if got.DateFrom != "2026-07-23T00:00:00.000Z" || got.DateTo != "2026-08-22T00:00:00.000Z" {
		t.Errorf("window = %q..%q, want the bounds the server resolved", got.DateFrom, got.DateTo)
	}
	if len(got.Repos) != 2 {
		t.Errorf("Repos = %v, want both", got.Repos)
	}
}

// TestGetAnalyticsTrends_SendsDocumentedParameters guards the query half of the
// defect. `period` is not a parameter of this endpoint; the route's query
// schema is .passthrough(), so sending it did not 422 — it was accepted,
// ignored, and every request quietly received the server's default 30-day
// window regardless of which range the user picked.
func TestGetAnalyticsTrends_SendsDocumentedParameters(t *testing.T) {
	var queries []url.Values
	srv := trendsServer(t, &queries)
	defer srv.Close()

	if _, err := runsService(srv.URL).GetAnalyticsTrends(context.Background(), "90d"); err != nil {
		t.Fatal(err)
	}

	if len(queries) != 2 {
		t.Fatalf("made %d requests, want 2 (one per metric)", len(queries))
	}

	documented := map[string]bool{
		"metric": true, "granularity": true, "dateFrom": true,
		"dateTo": true, "owner": true, "repo": true,
	}
	metrics := map[string]bool{}
	for _, q := range queries {
		for name := range q {
			if !documented[name] {
				t.Errorf("sent undocumented query parameter %q=%v", name, q[name])
			}
		}
		if q.Has("period") {
			t.Error("still sending `period`; the endpoint has no such parameter and silently ignores it")
		}
		metrics[q.Get("metric")] = true

		from, err := time.Parse(time.RFC3339, q.Get("dateFrom"))
		if err != nil {
			t.Fatalf("dateFrom %q is not RFC 3339: %v", q.Get("dateFrom"), err)
		}
		to, err := time.Parse(time.RFC3339, q.Get("dateTo"))
		if err != nil {
			t.Fatalf("dateTo %q is not RFC 3339: %v", q.Get("dateTo"), err)
		}
		// The selector the user picked must reach the wire, not just the client.
		if days := int(to.Sub(from).Hours() / 24); days != 90 {
			t.Errorf("window = %d days, want 90 for period %q", days, "90d")
		}
	}
	if !metrics["tokens"] || !metrics["success_rate"] {
		t.Errorf("metrics requested = %v, want both tokens and success_rate", metrics)
	}
}

func TestTrendsWindow(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		period string
		days   int
	}{
		{"30d", 30},
		{"90d", 90},
		{"180d", 180},
		{"", 30},
		{"nonsense", 30},
	} {
		from, to := trendsWindow(tc.period, now)
		if got := int(to.Sub(from).Hours() / 24); got != tc.days {
			t.Errorf("trendsWindow(%q) spans %d days, want %d", tc.period, got, tc.days)
		}
		if !to.Equal(now) {
			t.Errorf("trendsWindow(%q) upper bound = %v, want now", tc.period, to)
		}
	}
}

// TestMapAnalyticsTrends_TokenOnlyBucketReportsNoSuccessRate keeps a day that
// appears in the tokens series but not the success-rate series from rendering a
// fabricated 0% success rate — no runs were classified, so there is no rate.
func TestMapAnalyticsTrends_TokenOnlyBucketReportsNoSuccessRate(t *testing.T) {
	var tokens trendsTokensWire
	if err := json.Unmarshal([]byte(trendsTokensBody), &tokens); err != nil {
		t.Fatal(err)
	}
	got := mapAnalyticsTrends(tokens, trendsSuccessRateWire{})

	if len(got.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(got.Entries))
	}
	for _, e := range got.Entries {
		if e.TotalRuns != 0 {
			t.Errorf("%s: TotalRuns = %d, want 0", e.Date, e.TotalRuns)
		}
		if e.SuccessRate != 0 {
			t.Errorf("%s: SuccessRate = %v, want 0", e.Date, e.SuccessRate)
		}
	}
	if got.Entries[0].TotalTokens != 200000 {
		t.Errorf("TotalTokens = %d, want 200000", got.Entries[0].TotalTokens)
	}
	// Window metadata falls back to the tokens envelope when only it was fetched.
	if got.Granularity != "daily" || got.DateFrom == "" {
		t.Errorf("window metadata not carried from the tokens envelope: %+v", got)
	}
}
