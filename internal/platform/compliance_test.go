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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// bodyRecordingServer serves one response and keeps the request body it was
// sent. complianceServer's `*seen = *r` cannot do this: the handler has
// returned by the time the test reads it, and r.Body is closed.
func bodyRecordingServer(t *testing.T, body string, status int, sent *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		*sent = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write body: %v", err)
		}
	}))
}

// generatedReportBody is the route's 201: `{...toSummary(report), reportData}`.
const generatedReportBody = `{
  "reportId": "rpt-9",
  "status": "pending",
  "reportType": "SOC2",
  "format": "json",
  "schedule": null,
  "generatedAt": null,
  "startDate": "2026-04-01T00:00:00.000Z",
  "endDate": "2026-04-30T23:59:59.999Z",
  "errorMessage": null,
  "s3Key": null,
  "createdAt": "2026-05-20T10:00:00.000Z",
  "reportData": null
}`

// TestComplianceService_GenerateReport_SendsRFC3339Bounds asserts the OUTGOING
// body against the route's own validator (#821).
//
// `GenerateReportBody` declares startDate and endDate as
// `z.string().datetime()`, and the dashboard's date inputs produce a bare
// calendar date — so the body this client used to send was rejected 422 before
// a report was ever created. Revert either bound to a passthrough and the
// assertions below reproduce exactly what the route sees when it rejects.
func TestComplianceService_GenerateReport_SendsRFC3339Bounds(t *testing.T) {
	var sent []byte
	srv := bodyRecordingServer(t, generatedReportBody, http.StatusCreated, &sent)
	defer srv.Close()

	if _, err := complianceSvc(t, srv.URL).GenerateReport(
		context.Background(), "SOC2", "2026-04-01", "2026-04-30", "json"); err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	var body map[string]string
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("decode sent body: %v (%s)", err, sent)
	}

	// The route parses these with `new Date(...)`; a bare "2026-04-01" fails
	// z.string().datetime() outright, which is the whole defect.
	if got, want := body["startDate"], "2026-04-01T00:00:00Z"; got != want {
		t.Errorf("startDate = %q, want %q — z.string().datetime() rejects a bare calendar date", got, want)
	}
	// The end bound covers the operator's chosen day rather than collapsing it
	// to that day's first instant, which would exclude the day they picked.
	if got, want := body["endDate"], "2026-04-30T23:59:59.999999999Z"; got != want {
		t.Errorf("endDate = %q, want %q — the chosen end day must stay inside the window", got, want)
	}
	// reportType and format go out verbatim: the route's enum is the only
	// vocabulary, and a second one here would hide the next mismatch.
	if got, want := body["reportType"], "SOC2"; got != want {
		t.Errorf("reportType = %q, want %q — z.enum(['SOC2','ISO27001']) is case-sensitive", got, want)
	}
	if got, want := body["format"], "json"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
}

// A bound that already carries a time is the platform's contract already met —
// widening it would move a window the caller stated precisely.
func TestComplianceService_GenerateReport_PassesThroughDateTimeBounds(t *testing.T) {
	var sent []byte
	srv := bodyRecordingServer(t, generatedReportBody, http.StatusCreated, &sent)
	defer srv.Close()

	if _, err := complianceSvc(t, srv.URL).GenerateReport(
		context.Background(), "ISO27001",
		"2026-04-01T09:30:00Z", "2026-04-30T17:00:00Z", "pdf"); err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	var body map[string]string
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("decode sent body: %v (%s)", err, sent)
	}
	if body["startDate"] != "2026-04-01T09:30:00Z" || body["endDate"] != "2026-04-30T17:00:00Z" {
		t.Errorf("bounds = %q..%q, want them untouched", body["startDate"], body["endDate"])
	}
}

// TestComplianceService_GenerateReport_SurfacesValidationDetail — the 422 body
// is a literal from the route: `makeErrorBody('VALIDATION_ERROR', 'Invalid
// request body', requestId, {fields: parsed.error.issues})`, with the Zod
// issues a lowercase reportType and a bare startDate actually produce.
//
// Reporting the status alone is what the Compliance tab did before #821: the
// operator saw "The platform rejected this request (HTTP 422)" while the route
// had already named every field it refused.
func TestComplianceService_GenerateReport_SurfacesValidationDetail(t *testing.T) {
	body := `{
	  "error": {
	    "code": "VALIDATION_ERROR",
	    "message": "Invalid request body",
	    "details": {
	      "fields": [
	        {
	          "received": "soc2",
	          "code": "invalid_enum_value",
	          "options": ["SOC2", "ISO27001"],
	          "path": ["reportType"],
	          "message": "Invalid enum value. Expected 'SOC2' | 'ISO27001', received 'soc2'"
	        },
	        {
	          "code": "invalid_string",
	          "validation": "datetime",
	          "path": ["startDate"],
	          "message": "Invalid datetime"
	        }
	      ]
	    },
	    "request_id": "req-1"
	  }
	}`
	var sent []byte
	srv := bodyRecordingServer(t, body, http.StatusUnprocessableEntity, &sent)
	defer srv.Close()

	_, err := complianceSvc(t, srv.URL).GenerateReport(
		context.Background(), "soc2", "2026-04-01T00:00:00Z", "2026-04-30T00:00:00Z", "json")
	if err == nil {
		t.Fatal("GenerateReport: want an error on 422")
	}
	msg := err.Error()
	// "server returned 422" is the shape platformResult.ts's STATUS_RE parses
	// into a bad_request failure; losing it downgrades the tab's banner to a
	// retry invitation for a request that can never succeed.
	for _, want := range []string{
		"server returned 422",
		"VALIDATION_ERROR",
		"Invalid request body",
		"reportType: Invalid enum value",
		"startDate: Invalid datetime",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// A non-envelope error body must not be quoted back: an HTML page from a proxy
// or an empty 502 says nothing about the request, and pasting bytes of unknown
// provenance into an error is how a diagnostic becomes noise.
func TestDescribeErrorResponse_NonEnvelopeBodyDegradesToStatus(t *testing.T) {
	for name, body := range map[string]string{
		"html":  "<html><body>502 Bad Gateway</body></html>",
		"empty": "",
		"json":  `{"unexpected": "shape"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var sent []byte
			srv := bodyRecordingServer(t, body, http.StatusBadGateway, &sent)
			defer srv.Close()

			_, err := complianceSvc(t, srv.URL).GenerateReport(
				context.Background(), "SOC2", "2026-04-01", "2026-04-30", "json")
			if err == nil {
				t.Fatal("want an error on 502")
			}
			if got, want := err.Error(), "generate compliance report: server returned 502"; got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
}
