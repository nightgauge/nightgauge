package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// PortalSessionResult holds the URL for a Stripe Customer Portal session.
type PortalSessionResult struct {
	URL string `json:"url"`
}

// BillingService wraps the platform API's billing endpoints.
// POST /v1/billing/portal-session is not in the OpenAPI spec, so this service
// makes a raw HTTP request with the platform client's auth headers.
type BillingService struct {
	client *Client
}

// NewBillingService creates a billing service.
func NewBillingService(client *Client) *BillingService {
	return &BillingService{client: client}
}

// CreatePortalSession creates a Stripe Customer Portal session.
// Returns an error if the platform is offline (portal requires live API).
func (s *BillingService) CreatePortalSession(ctx context.Context) (*PortalSessionResult, error) {
	if !s.client.IsOnline() {
		return nil, fmt.Errorf("billing portal requires online platform connectivity")
	}

	url := s.client.base + "/v1/billing/portal-session"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create portal request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Inject auth header using the same pattern as the generated client.
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("portal session request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read portal response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("portal session failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result PortalSessionResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse portal response: %w", err)
	}

	return &result, nil
}
