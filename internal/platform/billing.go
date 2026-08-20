package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// PortalSessionResult holds the URL for a Stripe Customer Portal session.
type PortalSessionResult struct {
	URL string `json:"url"`
}

// BillingService wraps the platform API's billing endpoints.
// POST /v1/billing/portal-session has no operation in the oapi-codegen client
// (api/openapi.yaml is absent from this repo — see api/platform-operations.yaml),
// so it is issued through the operation contract instead: api.OpBillingPortalSession.
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

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:   api.OpBillingPortalSession,
		Body: []byte("{}"),
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create portal request: %w", err)
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
