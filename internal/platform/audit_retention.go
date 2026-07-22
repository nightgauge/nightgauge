package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AuditRetentionService wraps the platform API audit retention + integrity endpoints.
type AuditRetentionService struct {
	client *Client
}

// NewAuditRetentionService creates a new AuditRetentionService backed by the given platform client.
func NewAuditRetentionService(client *Client) *AuditRetentionService {
	return &AuditRetentionService{client: client}
}

// RetentionConfig is the current audit log retention configuration.
type RetentionConfig struct {
	RetentionDays int    `json:"retentionDays"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// IntegrityResult is the result of an audit log integrity verification.
type IntegrityResult struct {
	Valid        bool   `json:"valid"`
	CheckedCount int    `json:"checkedCount"`
	WindowDays   int    `json:"windowDays"`
	Message      string `json:"message"`
	CheckedAt    string `json:"checkedAt"`
}

// GetRetentionConfig fetches the current audit retention config via GET /v1/audit/retention.
func (s *AuditRetentionService) GetRetentionConfig(ctx context.Context) (*RetentionConfig, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("audit retention not available: platform client offline")
	}

	url := s.client.base + "/v1/audit/retention"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create get retention config request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get retention config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("enterprise only: audit retention requires an enterprise plan")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get retention config: server returned %d", resp.StatusCode)
	}

	var result RetentionConfig
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode get retention config response: %w", err)
	}
	return &result, nil
}

// UpdateRetentionConfig sets the audit retention period via PUT /v1/audit/retention.
func (s *AuditRetentionService) UpdateRetentionConfig(ctx context.Context, retentionDays int) (*RetentionConfig, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("audit retention not available: platform client offline")
	}

	body := map[string]int{"retentionDays": retentionDays}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal update retention config request: %w", err)
	}

	url := s.client.base + "/v1/audit/retention"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create update retention config request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update retention config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("enterprise only: audit retention requires an enterprise plan")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("update retention config: server returned %d", resp.StatusCode)
	}

	var result RetentionConfig
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode update retention config response: %w", err)
	}
	return &result, nil
}

// VerifyIntegrity triggers audit log integrity verification via POST /v1/audit/integrity/verify.
// windowDays must be 30, 90, or 365.
func (s *AuditRetentionService) VerifyIntegrity(ctx context.Context, windowDays int) (*IntegrityResult, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("audit integrity verification not available: platform client offline")
	}

	body := map[string]int{"windowDays": windowDays}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal verify integrity request: %w", err)
	}

	url := s.client.base + "/v1/audit/integrity/verify"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create verify integrity request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verify integrity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("enterprise only: audit integrity verification requires an enterprise plan")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("verify integrity: server returned %d", resp.StatusCode)
	}

	var result IntegrityResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode verify integrity response: %w", err)
	}
	return &result, nil
}
