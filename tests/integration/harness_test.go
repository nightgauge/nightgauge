//go:build integration

// Package integration_test hosts the GitLab CE harness tests for Wave 5-2
// of the forge-abstraction epic (#3349). All tests in this directory build
// with the `integration` tag and require a running GitLab CE container —
// they self-skip when GITLAB_E2E_URL is unset.
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/tests/integration/seed"
)

var (
	fixtures   *seed.Fixtures
	gitlabURL  string
	rootToken  string
	skipReason string
)

// envGITLABURL gates the entire suite. Unset → tests print a skip line and
// exit 0 (CI matrix renders as "skipped" rather than "failed").
const (
	envGITLABURL = "GITLAB_E2E_URL"
	envRootToken = "GITLAB_ROOT_TOKEN"
	envPort      = "GITLAB_E2E_PORT"
)

// TestMain waits for GitLab to become healthy, runs the fixture seeder, and
// only then dispatches t.Run. When GITLAB_E2E_URL is unset the suite skips
// entirely — this is the behavior the skills-smoke matrix relies on.
func TestMain(m *testing.M) {
	gitlabURL = strings.TrimRight(os.Getenv(envGITLABURL), "/")
	if gitlabURL == "" {
		fmt.Fprintf(os.Stderr, "%s not set — skipping GitLab integration tests\n", envGITLABURL)
		os.Exit(0)
	}
	rootToken = os.Getenv(envRootToken)
	if rootToken == "" {
		fmt.Fprintf(os.Stderr, "%s not set — GitLab integration tests require a root-scope PAT\n", envRootToken)
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	if err := waitForHealth(ctx, gitlabURL, 5*time.Minute); err != nil {
		fmt.Fprintf(os.Stderr, "GitLab CE did not become healthy within 5 minutes — check container logs: docker logs gitlab-ce\n%v\n", err)
		os.Exit(1)
	}

	s := seed.NewSeeder(gitlabURL, rootToken)
	var err error
	fixtures, err = s.Seed(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture seeder failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// waitForHealth polls baseURL/-/health every 5s until it returns 200 OK or
// the timeout elapses.
func waitForHealth(parent context.Context, baseURL string, timeout time.Duration) error {
	if _, err := url.Parse(baseURL); err != nil {
		return fmt.Errorf("bad GITLAB_E2E_URL: %w", err)
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/-/health", nil)
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
			return fmt.Errorf("waitForHealth: timed out after %s waiting for %s/-/health", timeout, baseURL)
		case <-time.After(5 * time.Second):
		}
	}
}

// gitlabReachableHost returns the host:port that the GitLab container can use
// to reach a service running on the test machine. On macOS that's
// host.docker.internal; on Linux the docker bridge IP.
func gitlabReachableHost(port int) string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("host.docker.internal:%d", port)
	default:
		return fmt.Sprintf("172.17.0.1:%d", port)
	}
}

// TestHarness_WaitForHealth_Timeout is a unit-style check on the timeout
// path of waitForHealth. It deliberately points at an unreachable URL.
func TestHarness_WaitForHealth_Timeout(t *testing.T) {
	if gitlabURL == "" {
		t.Skip("skipped — gitlabURL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 127.0.0.1 on a port we know is closed.
	err := waitForHealth(ctx, "http://127.0.0.1:1", 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// TestHarness_Fixtures verifies the seeder produced the expected shape.
// Cheap sanity check that downstream tests assume.
func TestHarness_Fixtures(t *testing.T) {
	if fixtures == nil {
		t.Fatal("fixtures not set — TestMain seeder failed")
	}
	if fixtures.ProjectID == 0 {
		t.Error("expected non-zero project id")
	}
	if fixtures.PAT == "" {
		t.Error("expected PAT to be populated")
	}
	if len(fixtures.IssueIIDs) < 1 {
		t.Errorf("expected ≥1 seeded issues, got %d", len(fixtures.IssueIIDs))
	}
	if fixtures.MRIID == 0 {
		t.Error("expected non-zero MR iid")
	}

	// JSON round-trip: confirms the Fixtures struct is consumer-ready.
	b, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back seed.Fixtures
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ProjectID != fixtures.ProjectID {
		t.Errorf("round-trip mismatch: %d != %d", back.ProjectID, fixtures.ProjectID)
	}
}
