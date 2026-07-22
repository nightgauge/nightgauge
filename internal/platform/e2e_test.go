//go:build integration

package platform_test

import (
	"context"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// E2E tests require a running platform instance.
// Run with: make test-integration
// Or: go test -tags integration ./internal/platform/...
//
// Setup:
//   cd ../acme-platform
//   docker compose up -d
//   npm run -w @acme-platform/db migrate
//   export PLATFORM_E2E_URL=http://localhost:3000
//   export PLATFORM_E2E_GITHUB_TOKEN=<PAT with read:user user:email>

func platformE2EClient(t *testing.T) *platform.Client {
	t.Helper()
	url := os.Getenv("PLATFORM_E2E_URL")
	if url == "" {
		t.Skip("PLATFORM_E2E_URL not set — skipping E2E test")
	}
	cfg := platform.Config{
		BaseURL:    url,
		APIKey:     os.Getenv("PLATFORM_E2E_API_KEY"),
		LicenseKey: os.Getenv("PLATFORM_E2E_LICENSE_KEY"),
	}
	c, err := platform.NewClient(cfg)
	if err != nil {
		t.Fatalf("create platform client: %v", err)
	}

	ctx := context.Background()
	c.StartHealthPolling(ctx)
	t.Cleanup(c.StopHealthPolling)

	return c
}

func TestE2E_Health_Check(t *testing.T) {
	c := platformE2EClient(t)
	resp, err := c.API().GetHealthWithResponse(context.Background())
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("unexpected health response: %d", resp.StatusCode())
	}
	if resp.JSON200.Status != "ok" {
		t.Errorf("status = %s, want ok", resp.JSON200.Status)
	}
}

func TestE2E_Auth_GitHubTokenExchange(t *testing.T) {
	c := platformE2EClient(t)
	ghToken := os.Getenv("PLATFORM_E2E_GITHUB_TOKEN")
	if ghToken == "" {
		t.Skip("PLATFORM_E2E_GITHUB_TOKEN not set")
	}

	svc := platform.NewAuthService(c)
	resp, err := svc.ExchangeGitHubToken(context.Background(), ghToken)
	if err != nil {
		t.Fatalf("exchange github token: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("access_token is empty")
	}
	if resp.RefreshToken == "" {
		t.Error("refresh_token is empty")
	}
}

func TestE2E_Auth_DeviceFlow(t *testing.T) {
	c := platformE2EClient(t)
	svc := platform.NewAuthService(c)

	result, err := svc.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("start device flow: %v", err)
	}
	if result.DeviceCode == "" {
		t.Error("device_code is empty")
	}
	if result.UserCode == "" {
		t.Error("user_code is empty")
	}
	if result.VerificationUri == "" {
		t.Error("verification_uri is empty")
	}

	// Poll once — should be pending (no user has authorized)
	_, pendingResp, err := svc.PollDeviceToken(context.Background(), result.DeviceCode)
	if err != nil {
		t.Fatalf("poll device token: %v", err)
	}
	if pendingResp == nil {
		t.Log("warning: device flow returned authorized on first poll (unexpected in E2E)")
	}
}

func TestE2E_Auth_TokenRefresh(t *testing.T) {
	c := platformE2EClient(t)
	ghToken := os.Getenv("PLATFORM_E2E_GITHUB_TOKEN")
	if ghToken == "" {
		t.Skip("PLATFORM_E2E_GITHUB_TOKEN not set")
	}

	svc := platform.NewAuthService(c)

	// First get a token pair via GitHub exchange
	initial, err := svc.ExchangeGitHubToken(context.Background(), ghToken)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	// Then refresh it
	refreshed, err := svc.RefreshToken(context.Background(), initial.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.AccessToken == "" {
		t.Error("refreshed access_token is empty")
	}
}

func TestE2E_License_Validate(t *testing.T) {
	c := platformE2EClient(t)
	licenseKey := os.Getenv("PLATFORM_E2E_LICENSE_KEY")
	if licenseKey == "" {
		t.Skip("PLATFORM_E2E_LICENSE_KEY not set")
	}

	svc := platform.NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatalf("validate license: %v", err)
	}
	if !info.Valid {
		t.Error("license not valid")
	}
	// (#4159) Assert REAL validation occurred, not the community fallback that a
	// contract mismatch would silently trigger. A seeded paid license resolves to
	// a non-community tier with a confirmed active status. (Per #134 the local
	// pipeline no longer carries per-tier feature caps, so tier + status are the
	// signals that distinguish a real validation from a silent community fallback.)
	if info.Tier == "community" || info.Tier == "" {
		t.Errorf("expected a real paid tier from live validation, got %q (silent fallback?)", info.Tier)
	}
	if info.Status != platform.LicenseStatusActive {
		t.Errorf("expected active status from live validation, got %q (silent fallback?)", info.Status)
	}
}

func TestE2E_Skill_Resolve(t *testing.T) {
	// SKIPPED: the platform does not implement resolve-by-stage — it serves only
	// GET /v1/skills/{uuid} and a list endpoint, with no POST /v1/skills/resolve
	// (the richer stage+context contract the client expects). This is a
	// platform-side gap, not a client bug — tracked under the contract-alignment
	// epic (nightgauge/nightgauge#4159 / platform). Re-enable once the platform
	// implements stage resolution.
	t.Skip("platform lacks POST /v1/skills/resolve (resolve-by-stage) — see #4159; platform-side work")

	c := platformE2EClient(t)
	svc := platform.NewSkillService(c)
	skill, err := svc.Resolve(context.Background(), "feature-dev", nil)
	if err != nil {
		t.Fatalf("resolve skill: %v", err)
	}
	if skill.Content == "" {
		t.Error("skill content is empty")
	}
}

func TestE2E_OfflineFallback(t *testing.T) {
	// Create a client pointing to a dead URL
	cfg := platform.Config{
		BaseURL: "http://127.0.0.1:1", // will fail to connect
	}
	c, err := platform.NewClient(cfg)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	// Without health polling, mode stays offline
	if c.Mode() != platform.ModeOffline {
		t.Errorf("mode = %s, want offline", c.Mode())
	}

	// License should fall back to community
	svc := platform.NewLicenseService(c)
	info, err := svc.Validate(context.Background())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if info.Tier != "community" {
		t.Errorf("tier = %s, want community", info.Tier)
	}
}
