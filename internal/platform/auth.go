package platform

import (
	"context"
	"encoding/json"
	"fmt"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// AuthService handles authentication flows against the platform API.
type AuthService struct {
	client *Client
}

// NewAuthService creates an authentication service.
func NewAuthService(client *Client) *AuthService {
	return &AuthService{client: client}
}

// ExchangeGitHubToken exchanges a GitHub OAuth token for a platform JWT.
func (s *AuthService) ExchangeGitHubToken(ctx context.Context, githubToken string) (*api.AuthTokenResponse, error) {
	resp, err := s.client.api.AuthGithubWithResponse(ctx, api.AuthGithubJSONRequestBody{
		GithubAccessToken: githubToken,
	})
	if err != nil {
		return nil, fmt.Errorf("exchange github token: %w", err)
	}
	if resp.JSON401 != nil {
		return nil, fmt.Errorf("unauthorized: invalid GitHub token")
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("exchange github token: unexpected response %d", resp.StatusCode())
	}
	return resp.JSON200, nil
}

// StartDeviceFlow begins the RFC 8628 device authorization flow.
func (s *AuthService) StartDeviceFlow(ctx context.Context) (*api.AuthDeviceCodeResult, error) {
	resp, err := s.client.api.AuthDeviceCodeWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("start device flow: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("start device flow: unexpected response %d", resp.StatusCode())
	}
	return resp.JSON200, nil
}

// PollDeviceToken polls for device flow completion.
// Returns (tokenResp, nil, nil) on success, (nil, pendingResp, nil) if still pending,
// (nil, nil, err) on error.
func (s *AuthService) PollDeviceToken(ctx context.Context, deviceCode string) (*api.AuthTokenResponse, *api.AuthPendingResponse, error) {
	resp, err := s.client.api.AuthDeviceTokenWithResponse(ctx, api.AuthDeviceTokenJSONRequestBody{
		DeviceCode: deviceCode,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("poll device token: %w", err)
	}
	if resp.JSON403 != nil {
		return nil, nil, fmt.Errorf("device flow forbidden")
	}
	if resp.JSON400 != nil {
		return nil, nil, fmt.Errorf("device flow bad request")
	}

	// JSON200 is a union type — discriminate by unmarshalling the raw body
	if resp.StatusCode() == 200 {
		// Try to determine the type from the status field
		var probe struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(resp.Body, &probe); err != nil {
			return nil, nil, fmt.Errorf("poll device token: unmarshal status: %w", err)
		}

		switch probe.Status {
		case "authorized":
			var tokenResp api.AuthTokenResponse
			if err := json.Unmarshal(resp.Body, &tokenResp); err != nil {
				return nil, nil, fmt.Errorf("poll device token: unmarshal token: %w", err)
			}
			return &tokenResp, nil, nil
		case "authorization_pending", "slow_down":
			var pendingResp api.AuthPendingResponse
			if err := json.Unmarshal(resp.Body, &pendingResp); err != nil {
				return nil, nil, fmt.Errorf("poll device token: unmarshal pending: %w", err)
			}
			return nil, &pendingResp, nil
		default:
			return nil, nil, fmt.Errorf("poll device token: unknown status %q", probe.Status)
		}
	}

	return nil, nil, fmt.Errorf("poll device token: unexpected response %d", resp.StatusCode())
}

// RefreshToken rotates a refresh token for a new JWT pair.
func (s *AuthService) RefreshToken(ctx context.Context, refreshToken string) (*api.AuthTokenResponse, error) {
	resp, err := s.client.api.AuthRefreshWithResponse(ctx, api.AuthRefreshJSONRequestBody{
		RefreshToken: refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("refresh token: unexpected response %d", resp.StatusCode())
	}
	return resp.JSON200, nil
}
