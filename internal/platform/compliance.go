package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ComplianceService wraps the platform API compliance report endpoints.
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
type ComplianceReportEntry struct {
	ID          string `json:"id"`
	ReportType  string `json:"reportType"`
	Status      string `json:"status"`
	StartDate   string `json:"startDate"`
	EndDate     string `json:"endDate"`
	Format      string `json:"format"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

// ComplianceReportsPage is a paginated list of compliance reports.
type ComplianceReportsPage struct {
	Reports    []ComplianceReportEntry `json:"reports"`
	NextCursor string                  `json:"nextCursor,omitempty"`
	HasMore    bool                    `json:"hasMore"`
}

// ComplianceReportDetail is the detail of a single compliance report including download URL.
type ComplianceReportDetail struct {
	ID          string `json:"id"`
	ReportType  string `json:"reportType"`
	Status      string `json:"status"`
	StartDate   string `json:"startDate"`
	EndDate     string `json:"endDate"`
	Format      string `json:"format"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

// GenerateReport requests generation of a new compliance report via POST /v1/audit/reports.
func (s *ComplianceService) GenerateReport(ctx context.Context, reportType, startDate, endDate, format string) (*ComplianceReportResult, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("compliance reports not yet available: platform client offline")
	}

	body := map[string]string{
		"reportType": reportType,
		"startDate":  startDate,
		"endDate":    endDate,
		"format":     format,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal generate report request: %w", err)
	}

	url := s.client.base + "/v1/audit/reports"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create generate report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generate compliance report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("generate compliance report: server returned %d", resp.StatusCode)
	}

	var result ComplianceReportResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode generate report response: %w", err)
	}
	return &result, nil
}

// ListReports fetches a paginated list of compliance reports via GET /v1/audit/reports.
func (s *ComplianceService) ListReports(ctx context.Context, cursor string, limit int) (*ComplianceReportsPage, error) {
	if !s.client.IsOnline() {
		return &ComplianceReportsPage{Reports: []ComplianceReportEntry{}}, nil
	}

	baseURL := s.client.base + "/v1/audit/reports"
	params := make(map[string]string)
	if cursor != "" {
		params["cursor"] = cursor
	}
	if limit > 0 {
		params["limit"] = fmt.Sprintf("%d", limit)
	}
	listURL := buildURL(baseURL, params)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create list reports request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list compliance reports: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list compliance reports: server returned %d", resp.StatusCode)
	}

	var result ComplianceReportsPage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode list reports response: %w", err)
	}
	if result.Reports == nil {
		result.Reports = []ComplianceReportEntry{}
	}
	return &result, nil
}

// GetReport fetches a single compliance report by ID via GET /v1/audit/reports/:id.
func (s *ComplianceService) GetReport(ctx context.Context, reportID string) (*ComplianceReportDetail, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("compliance reports not yet available: platform client offline")
	}

	reportURL := s.client.base + "/v1/audit/reports/" + url.PathEscape(reportID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reportURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create get report request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get compliance report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get compliance report: server returned %d", resp.StatusCode)
	}

	var result ComplianceReportDetail
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode get report response: %w", err)
	}
	return &result, nil
}

// buildURL constructs a URL with the given base and query params, safely escaping values.
func buildURL(base string, params map[string]string) string {
	if len(params) == 0 {
		return base
	}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return base + "?" + q.Encode()
}
