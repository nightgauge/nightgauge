// Tests for the compliance-report client.
//
// Every body below is a LITERAL taken from the platform route
// (`packages/api/src/routes/audit-reports.ts`) and its route tests, never from
// the client's own structs. The tests these replace encoded the same struct
// they decoded — `jsonResponse(w, ComplianceReportsPage{...})` — so they stayed
// green for the entire life of the bug in #803: a server that serves the
// client's imagination proves only that the client is self-consistent.
package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// listReportsBody is the live GET /v1/audit/reports 200 body: a single `items`
// envelope of `toSummary()` rows. Copied from the route's own test, which
// asserts body.items[0].reportId and body.items[0].errorMessage.
const listReportsBody = `{
  "items": [
    {
      "reportId": "rpt-1",
      "status": "complete",
      "reportType": "SOC2",
      "format": "json",
      "schedule": null,
      "generatedAt": "2026-03-11T10:00:00.000Z",
      "startDate": "2026-03-01T00:00:00.000Z",
      "endDate": "2026-03-10T00:00:00.000Z",
      "errorMessage": null,
      "s3Key": null,
      "createdAt": "2026-03-11T09:00:00.000Z"
    },
    {
      "reportId": "rpt-2",
      "status": "failed",
      "reportType": "ISO27001",
      "format": "pdf",
      "schedule": "monthly",
      "generatedAt": null,
      "startDate": "2026-02-01T00:00:00.000Z",
      "endDate": "2026-02-28T00:00:00.000Z",
      "errorMessage": "render timed out",
      "s3Key": null,
      "createdAt": "2026-03-01T09:00:00.000Z"
    }
  ]
}`

// complianceServer serves one path with one body, recording the request it saw.
func complianceServer(t *testing.T, method, path, body string, status int, seen *http.Request) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if seen != nil {
			*seen = *r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
}

func complianceSvc(t *testing.T, baseURL string) *ComplianceService {
	t.Helper()
	c, err := NewClient(Config{BaseURL: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return NewComplianceService(c)
}

func TestComplianceService_ListReports_LiveShape(t *testing.T) {
	var seen http.Request
	srv := complianceServer(t, http.MethodGet, "/v1/audit/reports", listReportsBody, http.StatusOK, &seen)
	defer srv.Close()

	page, err := complianceSvc(t, srv.URL).ListReports(context.Background())
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(page.Reports) != 2 {
		t.Fatalf("len(Reports) = %d, want 2 — the Compliance tab renders this "+
			"list, and decoding the wrong envelope shows an account with "+
			"reports as having none (#803)", len(page.Reports))
	}

	first := page.Reports[0]
	want := ComplianceReportEntry{
		ID:          "rpt-1",
		ReportType:  "SOC2",
		Status:      "complete",
		StartDate:   "2026-03-01T00:00:00.000Z",
		EndDate:     "2026-03-10T00:00:00.000Z",
		Format:      "json",
		GeneratedAt: "2026-03-11T10:00:00.000Z",
		CreatedAt:   "2026-03-11T09:00:00.000Z",
	}
	if first != want {
		t.Errorf("Reports[0] = %+v, want %+v", first, want)
	}

	// A failed row must carry its reason: the route puts errorMessage on the
	// LIST specifically so the grid renders it without a per-row detail fetch.
	if page.Reports[1].ErrorMessage != "render timed out" {
		t.Errorf("Reports[1].ErrorMessage = %q, want %q",
			page.Reports[1].ErrorMessage, "render timed out")
	}
	// A null column must not become the string "null" or panic the decode.
	if page.Reports[1].GeneratedAt != "" {
		t.Errorf("Reports[1].GeneratedAt = %q, want empty for a null column",
			page.Reports[1].GeneratedAt)
	}
}

// TestComplianceService_ListReports_SendsNoQueryParameters guards the second
// half of #803. `listReports(accountId)` takes no cursor and no limit, and the
// response carries neither nextCursor nor hasMore — so the parameters this
// client used to send were discarded while the tab rendered pagination
// controls driven by a flag that was permanently false.
func TestComplianceService_ListReports_SendsNoQueryParameters(t *testing.T) {
	var seen http.Request
	srv := complianceServer(t, http.MethodGet, "/v1/audit/reports", listReportsBody, http.StatusOK, &seen)
	defer srv.Close()

	if _, err := complianceSvc(t, srv.URL).ListReports(context.Background()); err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if got := seen.URL.RawQuery; got != "" {
		t.Errorf("query = %q, want empty: the endpoint declares no parameters, "+
			"so anything sent is silently dropped and any UI built on it lies", got)
	}
}

// TestComplianceService_ListReports_PreFixShapeCarriesNothing pins the bug
// itself. This is the body the client used to decode — and no server has ever
// sent it. If a future edit makes this body produce rows again, the decode has
// drifted back off the route.
func TestComplianceService_ListReports_PreFixShapeCarriesNothing(t *testing.T) {
	preFixBody := `{
	  "reports": [{"id": "rpt-1", "reportType": "soc2", "status": "ready",
	               "startDate": "2026-03-01", "endDate": "2026-03-10",
	               "format": "pdf", "downloadUrl": "https://example.test/r.pdf",
	               "createdAt": "2026-03-11T09:00:00.000Z"}],
	  "nextCursor": null,
	  "hasMore": false
	}`
	srv := complianceServer(t, http.MethodGet, "/v1/audit/reports", preFixBody, http.StatusOK, nil)
	defer srv.Close()

	page, err := complianceSvc(t, srv.URL).ListReports(context.Background())
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(page.Reports) != 0 {
		t.Fatalf("Reports = %d, want 0 — this documents that the shape the "+
			"client invented carries no data from any real response",
			len(page.Reports))
	}
}

func TestComplianceService_ListReports_Offline(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://unreachable:9999"})
	if err != nil {
		t.Fatal(err)
	}

	page, err := NewComplianceService(c).ListReports(context.Background())
	if err != nil {
		t.Fatalf("ListReports offline: %v", err)
	}
	if page == nil {
		t.Fatal("expected empty page, got nil")
	}
	if len(page.Reports) != 0 {
		t.Errorf("Reports = %d, want 0", len(page.Reports))
	}
}

// TestComplianceService_GenerateReport_LiveShape — the 201 body is the same
// summary object, so the report ID arrives as `reportId`. Decoding `id` left
// it empty, and the dashboard polls generation status by that ID.
func TestComplianceService_GenerateReport_LiveShape(t *testing.T) {
	body := `{
	  "reportId": "rpt-9",
	  "status": "complete",
	  "reportType": "SOC2",
	  "format": "json",
	  "schedule": null,
	  "generatedAt": "2026-05-20T10:00:00.000Z",
	  "startDate": "2026-04-01T00:00:00.000Z",
	  "endDate": "2026-04-30T00:00:00.000Z",
	  "errorMessage": null,
	  "s3Key": null,
	  "createdAt": "2026-05-20T10:00:00.000Z",
	  "reportData": {}
	}`
	srv := complianceServer(t, http.MethodPost, "/v1/audit/reports", body, http.StatusCreated, nil)
	defer srv.Close()

	result, err := complianceSvc(t, srv.URL).GenerateReport(
		context.Background(), "SOC2", "2026-04-01T00:00:00Z", "2026-04-30T00:00:00Z", "json")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if result.ID != "rpt-9" {
		t.Errorf("ID = %q, want %q — an empty ID is a poll loop against no report",
			result.ID, "rpt-9")
	}
	if result.Status != "complete" {
		t.Errorf("Status = %q, want %q", result.Status, "complete")
	}
}

func TestComplianceService_GetReport_LiveShape(t *testing.T) {
	body := `{
	  "reportId": "rpt-3",
	  "status": "failed",
	  "reportType": "ISO27001",
	  "format": "pdf",
	  "schedule": null,
	  "generatedAt": null,
	  "startDate": "2026-04-01T00:00:00.000Z",
	  "endDate": "2026-04-30T00:00:00.000Z",
	  "errorMessage": "render timed out",
	  "s3Key": null,
	  "createdAt": "2026-05-20T10:00:00.000Z",
	  "expiresAt": "2026-08-18T10:00:00.000Z",
	  "reportData": null
	}`
	srv := complianceServer(t, http.MethodGet, "/v1/audit/reports/rpt-3", body, http.StatusOK, nil)
	defer srv.Close()

	detail, err := complianceSvc(t, srv.URL).GetReport(context.Background(), "rpt-3")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if detail.ID != "rpt-3" {
		t.Errorf("ID = %q, want %q", detail.ID, "rpt-3")
	}
	if detail.Status != "failed" {
		t.Errorf("Status = %q, want %q", detail.Status, "failed")
	}
	if detail.ErrorMessage != "render timed out" {
		t.Errorf("ErrorMessage = %q, want the route's reason", detail.ErrorMessage)
	}
	if detail.ExpiresAt != "2026-08-18T10:00:00.000Z" {
		t.Errorf("ExpiresAt = %q, want the retention expiry", detail.ExpiresAt)
	}
	if _, err := json.Marshal(detail); err != nil {
		t.Errorf("detail not serializable: %v", err)
	}
}

// The download endpoint answers in three shapes and the dashboard renders a
// different outcome for each, so each is decoded distinctly.
func TestComplianceService_DownloadReport(t *testing.T) {
	t.Run("signed URL", func(t *testing.T) {
		body := `{"url": "https://storage.test/signed", "expiresIn": 3600}`
		srv := complianceServer(t, http.MethodGet, "/v1/audit/reports/rpt-1/download", body, http.StatusOK, nil)
		defer srv.Close()

		got, err := complianceSvc(t, srv.URL).DownloadReport(context.Background(), "rpt-1")
		if err != nil {
			t.Fatalf("DownloadReport: %v", err)
		}
		if got.URL != "https://storage.test/signed" || got.ExpiresIn != 3600 {
			t.Errorf("got %+v, want the signed URL and its TTL", got)
		}
		if got.Pending {
			t.Error("Pending = true on a 200 carrying an artifact")
		}
	})

	t.Run("inline JSON payload", func(t *testing.T) {
		body := `{"format": "json", "data": {"controls": 12}}`
		srv := complianceServer(t, http.MethodGet, "/v1/audit/reports/rpt-2/download", body, http.StatusOK, nil)
		defer srv.Close()

		got, err := complianceSvc(t, srv.URL).DownloadReport(context.Background(), "rpt-2")
		if err != nil {
			t.Fatalf("DownloadReport: %v", err)
		}
		if got.URL != "" {
			t.Errorf("URL = %q, want empty for the inline-payload response", got.URL)
		}
		if string(got.Data) != `{"controls": 12}` {
			t.Errorf("Data = %s, want the payload verbatim", got.Data)
		}
	})

	t.Run("202 while generating", func(t *testing.T) {
		body := `{"message": "Report generation in progress", "status": "pending"}`
		srv := complianceServer(t, http.MethodGet, "/v1/audit/reports/rpt-3/download", body, http.StatusAccepted, nil)
		defer srv.Close()

		got, err := complianceSvc(t, srv.URL).DownloadReport(context.Background(), "rpt-3")
		if err != nil {
			t.Fatalf("DownloadReport: %v", err)
		}
		if !got.Pending {
			t.Error("Pending = false on a 202 — the operator would be told the " +
				"report has no artifact rather than that it is still being built")
		}
	})
}
