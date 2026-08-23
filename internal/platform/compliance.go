package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// ComplianceService wraps the platform API compliance report endpoints.
//
// Every result type below is an IPC-facing projection of a `*Wire` struct that
// decodes what the ROUTE returns (`packages/api/src/routes/audit-reports.ts`
// and its route tests). The two are kept apart deliberately: the wire shape is
// the platform's to change, the projection is the dashboard's contract, and
// before #803 there was no separation — the client decoded a shape nobody
// served, so a 200 rendered an empty Compliance tab.
type ComplianceService struct {
	client *Client
}

// NewComplianceService creates a new ComplianceService backed by the given platform client.
func NewComplianceService(client *Client) *ComplianceService {
	return &ComplianceService{client: client}
}

// ComplianceReportResult is the result of a compliance report generation request.
type ComplianceReportResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ReportType string `json:"reportType"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	Format     string `json:"format"`
	CreatedAt  string `json:"createdAt"`
}

// ComplianceReportEntry is a single compliance report in a list result.
//
// Status is the platform's own vocabulary — "pending" | "complete" | "failed"
// (`compliance_reports.status`). The client used to declare a "ready" state
// that the platform has never written, so the download affordance it gated was
// unreachable.
type ComplianceReportEntry struct {
	ID           string `json:"id"`
	ReportType   string `json:"reportType"`
	Status       string `json:"status"`
	StartDate    string `json:"startDate"`
	EndDate      string `json:"endDate"`
	Format       string `json:"format"`
	GeneratedAt  string `json:"generatedAt,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// ComplianceReportsResult is the account's compliance reports.
//
// It carries no cursor and no has-more flag because the endpoint has neither:
// `listReports(accountId)` takes no cursor or limit and returns the newest 50
// rows. The previous ComplianceReportsPage advertised pagination the server
// cannot honour, and the tab rendered controls from it (#803).
type ComplianceReportsResult struct {
	Reports []ComplianceReportEntry `json:"reports"`
}

// ComplianceReportDetail is the detail of a single compliance report.
type ComplianceReportDetail struct {
	ID           string `json:"id"`
	ReportType   string `json:"reportType"`
	Status       string `json:"status"`
	StartDate    string `json:"startDate"`
	EndDate      string `json:"endDate"`
	Format       string `json:"format"`
	GeneratedAt  string `json:"generatedAt,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// ComplianceReportDownload is the outcome of asking for a report's artifact.
//
// The endpoint answers in three shapes and the dashboard must tell them apart:
// a signed object-storage URL, the rendered JSON payload inline (the fallback
// whenever the report was generated in the default `json` format, which stores
// no artifact), or a 202 saying generation is still running.
type ComplianceReportDownload struct {
	URL       string          `json:"url,omitempty"`
	ExpiresIn int             `json:"expiresIn,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Pending   bool            `json:"pending"`
}

// complianceSummaryWire is the platform's compliance-report summary — the
// object `toSummary()` builds, shared by the generate, list and detail
// responses. Nullable columns are pointers so a partially-populated row
// decodes rather than failing the whole body.
type complianceSummaryWire struct {
	ReportID     string  `json:"reportId"`
	Status       string  `json:"status"`
	ReportType   string  `json:"reportType"`
	Format       string  `json:"format"`
	Schedule     *string `json:"schedule"`
	GeneratedAt  *string `json:"generatedAt"`
	StartDate    string  `json:"startDate"`
	EndDate      string  `json:"endDate"`
	ErrorMessage *string `json:"errorMessage"`
	S3Key        *string `json:"s3Key"`
	CreatedAt    string  `json:"createdAt"`
}

// complianceReportsWire is the live GET /v1/audit/reports body: a single-key
// envelope, `return c.json({ items: reports.map(toSummary) })`. The client
// decoded {reports, nextCursor, hasMore} — three keys, none of which the route
// emits — so every account read as having no reports at all (#803).
type complianceReportsWire struct {
	Items []complianceSummaryWire `json:"items"`
}

// complianceDetailWire is GET /v1/audit/reports/{id}: the summary plus the
// report payload, its failure reason and its retention expiry.
type complianceDetailWire struct {
	complianceSummaryWire
	ExpiresAt *string `json:"expiresAt"`
}

// complianceDownloadWire is GET /v1/audit/reports/{id}/download. Exactly one
// of the three groups is populated per response.
type complianceDownloadWire struct {
	URL       *string         `json:"url"`
	ExpiresIn *int            `json:"expiresIn"`
	Format    *string         `json:"format"`
	Data      json.RawMessage `json:"data"`
	Status    *string         `json:"status"`
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func mapComplianceSummary(wire complianceSummaryWire) ComplianceReportEntry {
	return ComplianceReportEntry{
		ID:           wire.ReportID,
		ReportType:   wire.ReportType,
		Status:       wire.Status,
		StartDate:    wire.StartDate,
		EndDate:      wire.EndDate,
		Format:       wire.Format,
		GeneratedAt:  derefString(wire.GeneratedAt),
		ErrorMessage: derefString(wire.ErrorMessage),
		CreatedAt:    wire.CreatedAt,
	}
}

// GenerateReport requests generation of a new compliance report via POST /v1/audit/reports.
//
// The window bounds are normalised to RFC 3339 the same way the cost window is
// (toRFC3339Bound): the route validates both with `z.string().datetime()`, and
// the dashboard's `<input type="date">` yields a bare calendar date, so every
// generate attempt used to 422 before a report was ever created (#821). The
// end bound widens to the last instant of its day, keeping the operator's
// chosen end date inside the window rather than truncating it away.
//
// reportType and format are passed through verbatim. The route's enums
// (`SOC2` | `ISO27001`, `pdf` | `json` | `both`) are the vocabulary the caller
// is expected to speak; translating a second casing here would put the
// contract in two places and hide the next mismatch instead of reporting it.
func (s *ComplianceService) GenerateReport(ctx context.Context, reportType, startDate, endDate, format string) (*ComplianceReportResult, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("compliance reports not yet available: platform client offline")
	}

	body := map[string]string{
		"reportType": reportType,
		"startDate":  toRFC3339Bound(startDate, false),
		"endDate":    toRFC3339Bound(endDate, true),
		"format":     format,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal generate report request: %w", err)
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:      api.OpAuditReportsGenerate,
		Body:    bodyBytes,
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("create generate report request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generate compliance report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// Quote the platform's own rejection — for a 422 that is
		// VALIDATION_ERROR plus the field each Zod issue names, which is the
		// difference between a legible bug report and "HTTP 422" (#821).
		return nil, fmt.Errorf("generate compliance report: %s", describeErrorResponse(resp))
	}

	var wire complianceSummaryWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode generate report response: %w", err)
	}
	entry := mapComplianceSummary(wire)
	return &ComplianceReportResult{
		ID:         entry.ID,
		Status:     entry.Status,
		ReportType: entry.ReportType,
		StartDate:  entry.StartDate,
		EndDate:    entry.EndDate,
		Format:     entry.Format,
		CreatedAt:  entry.CreatedAt,
	}, nil
}

// ListReports fetches the account's compliance reports via GET /v1/audit/reports.
//
// The endpoint takes no parameters: it returns the newest 50 rows for the
// account. The cursor and limit this method used to send were discarded by the
// server, so they are not sent (#803).
func (s *ComplianceService) ListReports(ctx context.Context) (*ComplianceReportsResult, error) {
	if !s.client.IsOnline() {
		return &ComplianceReportsResult{Reports: []ComplianceReportEntry{}}, nil
	}

	req, err := s.client.newRequest(ctx, requestSpec{Op: api.OpAuditReportsList})
	if err != nil {
		return nil, fmt.Errorf("create list reports request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list compliance reports: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list compliance reports: server returned %d", resp.StatusCode)
	}

	var wire complianceReportsWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode list reports response: %w", err)
	}

	reports := make([]ComplianceReportEntry, 0, len(wire.Items))
	for _, item := range wire.Items {
		reports = append(reports, mapComplianceSummary(item))
	}
	return &ComplianceReportsResult{Reports: reports}, nil
}

// GetReport fetches a single compliance report by ID via GET /v1/audit/reports/{id}.
func (s *ComplianceService) GetReport(ctx context.Context, reportID string) (*ComplianceReportDetail, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("compliance reports not yet available: platform client offline")
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:       api.OpAuditReportsGet,
		PathArgs: []string{reportID},
	})
	if err != nil {
		return nil, fmt.Errorf("create get report request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get compliance report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get compliance report: server returned %d", resp.StatusCode)
	}

	var wire complianceDetailWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode get report response: %w", err)
	}
	entry := mapComplianceSummary(wire.complianceSummaryWire)
	return &ComplianceReportDetail{
		ID:           entry.ID,
		ReportType:   entry.ReportType,
		Status:       entry.Status,
		StartDate:    entry.StartDate,
		EndDate:      entry.EndDate,
		Format:       entry.Format,
		GeneratedAt:  entry.GeneratedAt,
		ErrorMessage: entry.ErrorMessage,
		ExpiresAt:    derefString(wire.ExpiresAt),
		CreatedAt:    entry.CreatedAt,
	}, nil
}

// DownloadReport resolves a report's artifact via GET /v1/audit/reports/{id}/download.
//
// The detail endpoint carries no download URL and never has — the client's old
// `downloadUrl` field decoded nothing, so the Download button reported "not yet
// available" for every finished report (#803). The artifact lives behind this
// operation, which answers with a signed URL, the JSON payload inline, or 202
// while generation is still running.
func (s *ComplianceService) DownloadReport(ctx context.Context, reportID string) (*ComplianceReportDownload, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("compliance reports not yet available: platform client offline")
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:       api.OpAuditReportsDownload,
		PathArgs: []string{reportID},
	})
	if err != nil {
		return nil, fmt.Errorf("create download report request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download compliance report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download compliance report: server returned %d", resp.StatusCode)
	}

	var wire complianceDownloadWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode download report response: %w", err)
	}

	result := &ComplianceReportDownload{Pending: resp.StatusCode == http.StatusAccepted}
	if wire.URL != nil {
		result.URL = *wire.URL
	}
	if wire.ExpiresIn != nil {
		result.ExpiresIn = *wire.ExpiresIn
	}
	if len(wire.Data) > 0 {
		result.Data = wire.Data
	}
	return result, nil
}
