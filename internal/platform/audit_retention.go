package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
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

// IntegrityBrokenLink identifies one audit entry whose stored hash does not
// match the chain. `position` is the entry's index within the verified range.
type IntegrityBrokenLink struct {
	EntryID  string `json:"entryId"`
	Position int    `json:"position"`
}

// IntegrityResult is the result of an audit log integrity verification.
//
// These are the three fields POST /v1/audit/integrity returns, and only those
// (#822). It used to declare `windowDays`, `message` and `checkedAt` — none of
// which the route has ever sent — so the panel rendered a zero, an empty
// string and an empty timestamp on every successful verification that managed
// to reach the route at all.
type IntegrityResult struct {
	Valid        bool                  `json:"valid"`
	CheckedCount int                   `json:"checkedCount"`
	BrokenLinks  []IntegrityBrokenLink `json:"brokenLinks"`
}

// GetRetentionConfig fetches the current audit retention config via GET /v1/audit/retention.
func (s *AuditRetentionService) GetRetentionConfig(ctx context.Context) (*RetentionConfig, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("audit retention not available: platform client offline")
	}

	req, err := s.client.newRequest(ctx, requestSpec{Op: api.OpAuditRetentionGet})
	if err != nil {
		return nil, fmt.Errorf("create get retention config request: %w", err)
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

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:      api.OpAuditRetentionUpdate,
		Body:    bodyBytes,
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("create update retention config request: %w", err)
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

// VerifyIntegrity triggers audit log integrity verification via
// POST /v1/audit/integrity.
//
// windowDays must be 30, 90, or 365; it is the dashboard's vocabulary, not the
// platform's. The route validates `{startDate, endDate}` as RFC 3339 instants
// and rejects anything else with a 422, so the window is expanded here into the
// bounds the route requires rather than sent as a day count the route has never
// accepted (#822).
func (s *AuditRetentionService) VerifyIntegrity(ctx context.Context, windowDays int) (*IntegrityResult, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("audit integrity verification not available: platform client offline")
	}

	startDate, endDate := integrityWindowBounds(time.Now().UTC(), windowDays)
	body := map[string]string{"startDate": startDate, "endDate": endDate}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal verify integrity request: %w", err)
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:      api.OpAuditIntegrityVerify,
		Body:    bodyBytes,
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("create verify integrity request: %w", err)
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
		// Quote the platform's own rejection rather than a bare status (#821).
		return nil, fmt.Errorf("verify integrity: %s", describeErrorResponse(resp))
	}

	var result IntegrityResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode verify integrity response: %w", err)
	}
	if result.BrokenLinks == nil {
		result.BrokenLinks = []IntegrityBrokenLink{}
	}
	return &result, nil
}

// integrityWindowBounds turns a "last N days" button into the closed instant
// range the route validates: the first instant of the day N days before now,
// through the last instant of today. Both bounds go through toRFC3339Bound so
// the window matches what the Compliance tab sends for the same calendar days
// — in particular the end bound covers the whole of the operator's own day
// rather than truncating it to midnight, which would exclude every entry
// written since (#821).
func integrityWindowBounds(now time.Time, windowDays int) (string, string) {
	const day = "2006-01-02"
	start := now.AddDate(0, 0, -windowDays).UTC().Format(day)
	end := now.UTC().Format(day)
	return toRFC3339Bound(start, false), toRFC3339Bound(end, true)
}
