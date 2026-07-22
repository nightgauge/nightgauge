package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComplianceService_GenerateReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/audit/reports" {
			jsonResponse(w, ComplianceReportResult{
				ID:         "rpt-1",
				Status:     "pending",
				ReportType: "soc2",
				StartDate:  "2026-01-01",
				EndDate:    "2026-03-31",
				Format:     "pdf",
				CreatedAt:  "2026-05-13T00:00:00Z",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewComplianceService(c)
	result, err := svc.GenerateReport(context.Background(), "soc2", "2026-01-01", "2026-03-31", "pdf")
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if result.ID != "rpt-1" {
		t.Errorf("ID = %q, want %q", result.ID, "rpt-1")
	}
	if result.Status != "pending" {
		t.Errorf("Status = %q, want %q", result.Status, "pending")
	}
}

func TestComplianceService_ListReports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/audit/reports" {
			jsonResponse(w, ComplianceReportsPage{
				Reports: []ComplianceReportEntry{
					{
						ID:         "rpt-2",
						ReportType: "iso27001",
						Status:     "ready",
						StartDate:  "2026-01-01",
						EndDate:    "2026-03-31",
						Format:     "pdf",
						DownloadURL: "https://example.com/rpt-2.pdf",
						CreatedAt:  "2026-05-01T00:00:00Z",
					},
				},
				HasMore: false,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewComplianceService(c)
	page, err := svc.ListReports(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(page.Reports) != 1 {
		t.Fatalf("len(Reports) = %d, want 1", len(page.Reports))
	}
	if page.Reports[0].Status != "ready" {
		t.Errorf("Status = %q, want %q", page.Reports[0].Status, "ready")
	}
}

func TestComplianceService_ListReports_Offline(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewComplianceService(c)
	page, err := svc.ListReports(context.Background(), "", 10)
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

func TestComplianceService_GetReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/audit/reports/rpt-3" {
			jsonResponse(w, ComplianceReportDetail{
				ID:          "rpt-3",
				ReportType:  "soc2",
				Status:      "ready",
				DownloadURL: "https://example.com/rpt-3.pdf",
				CreatedAt:   "2026-05-10T00:00:00Z",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewComplianceService(c)
	detail, err := svc.GetReport(context.Background(), "rpt-3")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if detail.DownloadURL == "" {
		t.Error("expected non-empty DownloadURL")
	}
	if _, err := json.Marshal(detail); err != nil {
		t.Errorf("detail not serializable: %v", err)
	}
}
