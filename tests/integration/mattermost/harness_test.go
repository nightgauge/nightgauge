//go:build integration

// Package mattermost_test hosts the Dockerized Mattermost integration
// tests for issue #3381. All tests in this directory build with the
// `integration` tag and require a running Mattermost container — they
// self-skip when MATTERMOST_E2E_URL is unset.
//
// The suite lives in its own package, separate from tests/integration,
// so it gets an independent TestMain gated on MATTERMOST_E2E_URL rather
// than sharing the GitLab harness's GITLAB_E2E_URL gate. See ADR-001 in
// the issue knowledge base.
package mattermost_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	mmfixtures "github.com/nightgauge/nightgauge/tests/integration/mattermost-fixtures"
)

const (
	// envMMURL gates the entire suite. Unset → tests print a skip line
	// and exit 0, so the CI matrix renders the job as "skipped" rather
	// than "failed".
	envMMURL = "MATTERMOST_E2E_URL"
	// envMMAdminUser / envMMAdminPass override the system-admin
	// credentials the fixture seeder bootstraps and logs in with.
	envMMAdminUser = "MATTERMOST_ADMIN_USER"
	envMMAdminPass = "MATTERMOST_ADMIN_PASSWORD"

	defaultAdminUser = "admin"
	defaultAdminPass = "Nightgauge-Test-1"

	// pingTimeout bounds how long TestMain waits for Mattermost to
	// answer /api/v4/system/ping before failing the suite.
	pingTimeout = 90 * time.Second
)

var (
	// mmURL is the base URL of the Mattermost instance under test,
	// populated by TestMain from envMMURL.
	mmURL string
	// fixtures is the seeded fixture set shared by every test. Nil only
	// when the suite is skipped.
	fixtures *mmfixtures.Fixtures
)

// TestMain waits for Mattermost to become healthy, runs the fixture
// seeder, and only then dispatches t.Run. When MATTERMOST_E2E_URL is
// unset the suite skips entirely.
func TestMain(m *testing.M) {
	mmURL = strings.TrimRight(os.Getenv(envMMURL), "/")
	if mmURL == "" {
		fmt.Fprintf(os.Stderr, "%s not set — skipping Mattermost integration tests\n", envMMURL)
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := waitForPing(ctx, mmURL, pingTimeout); err != nil {
		fmt.Fprintf(os.Stderr,
			"Mattermost did not become healthy within %s — check container logs: docker logs mattermost-ce\n%v\n",
			pingTimeout, err)
		os.Exit(1)
	}

	adminUser := os.Getenv(envMMAdminUser)
	if adminUser == "" {
		adminUser = defaultAdminUser
	}
	adminPass := os.Getenv(envMMAdminPass)
	if adminPass == "" {
		adminPass = defaultAdminPass
	}

	s := mmfixtures.NewSeeder(mmURL, adminUser, adminPass)
	var err error
	fixtures, err = s.Seed(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mattermost fixture seeder failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// waitForPing polls baseURL/api/v4/system/ping every 3s until it returns
// 200 OK or the timeout elapses.
func waitForPing(parent context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v4/system/ping", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waitForPing: timed out after %s waiting for %s/api/v4/system/ping", timeout, baseURL)
		case <-time.After(3 * time.Second):
		}
	}
}

// TestHarness_Fixtures verifies the seeder produced the expected shape.
// Cheap sanity check that the E2E tests assume.
func TestHarness_Fixtures(t *testing.T) {
	if fixtures == nil {
		t.Fatal("fixtures not set — TestMain seeder failed")
	}
	if fixtures.TeamID == "" {
		t.Error("expected non-empty team id")
	}
	if fixtures.ChannelID == "" || fixtures.ChannelName == "" {
		t.Error("expected channel id and name to be populated")
	}
	if fixtures.BotUserID == "" {
		t.Error("expected bot user id to be populated")
	}
	if fixtures.IncomingWebhookURL == "" {
		t.Error("expected incoming webhook URL to be populated")
	}
	if fixtures.OutgoingWebhookToken == "" {
		t.Error("expected outgoing webhook token to be populated")
	}

	// JSON round-trip: confirms the Fixtures struct is consumer-ready.
	b, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back mmfixtures.Fixtures
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ChannelID != fixtures.ChannelID {
		t.Errorf("round-trip mismatch: %q != %q", back.ChannelID, fixtures.ChannelID)
	}
}

// TestHarness_WaitForPing_Timeout is a unit-style check on the timeout
// path of waitForPing. It deliberately points at a closed port.
func TestHarness_WaitForPing_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := waitForPing(ctx, "http://127.0.0.1:1", 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
