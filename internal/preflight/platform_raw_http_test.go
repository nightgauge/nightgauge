package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlatformFixture lays out <root>/internal/platform with the given files
// and returns the root.
func writePlatformFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "platform")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// sanctionedRequestGo is a stand-in for the real request.go: it is the one file
// allowed to touch http.NewRequest* and Client.base.
const sanctionedRequestGo = `package platform

import (
	"context"
	"net/http"
)

func (c *Client) newRequest(ctx context.Context, path string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
}
`

func TestPlatformRawHTTP_CleanPackagePasses(t *testing.T) {
	root := writePlatformFixture(t, map[string]string{
		"request.go": sanctionedRequestGo,
		"analytics.go": `package platform

import (
	"context"
	"net/http"
)

func (s *AnalyticsService) GetHealth(ctx context.Context) error {
	req, err := s.client.newRequest(ctx, "/v1/analytics/health")
	if err != nil {
		return err
	}
	_, err = http.DefaultClient.Do(req)
	return err
}
`,
	})

	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", res.Findings)
	}
	if res.FilesChecked != 2 {
		t.Errorf("files_checked = %d, want 2", res.FilesChecked)
	}
	if res.V != 1 {
		t.Errorf("v = %d, want 1", res.V)
	}
}

// TestPlatformRawHTTP_CatchesReintroducedRawCall is the regression this gate
// exists for: the exact shape of the pre-#750 call sites.
func TestPlatformRawHTTP_CatchesReintroducedRawCall(t *testing.T) {
	root := writePlatformFixture(t, map[string]string{
		"request.go": sanctionedRequestGo,
		"analytics.go": `package platform

import (
	"context"
	"net/http"
)

func (s *AnalyticsService) GetCost(ctx context.Context) error {
	url := s.client.base + "/v1/analytics/cost"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearer := s.client.bearer(); bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	_, err = http.DefaultClient.Do(req)
	return err
}
`,
	})

	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d (%+v), want 2 (one raw_request, one base_url)", len(res.Findings), res.Findings)
	}

	kinds := map[string]PlatformRawHTTPFinding{}
	for _, f := range res.Findings {
		kinds[f.Kind] = f
		if f.File != "internal/platform/analytics.go" {
			t.Errorf("file = %q, want internal/platform/analytics.go", f.File)
		}
	}

	raw, ok := kinds[PlatformRawHTTPKindRawRequest]
	if !ok {
		t.Fatalf("no raw_request finding in %+v", res.Findings)
	}
	if raw.Match != "http.NewRequestWithContext" {
		t.Errorf("raw match = %q, want http.NewRequestWithContext", raw.Match)
	}
	if raw.Line != 10 {
		t.Errorf("raw line = %d, want 10", raw.Line)
	}

	base, ok := kinds[PlatformRawHTTPKindBaseURL]
	if !ok {
		t.Fatalf("no base_url finding in %+v", res.Findings)
	}
	if base.Match != "s.client.base" {
		t.Errorf("base match = %q, want s.client.base", base.Match)
	}
	if base.Line != 9 {
		t.Errorf("base line = %d, want 9", base.Line)
	}
}

// TestPlatformRawHTTP_CatchesSplitConstruction covers the evasion where the URL
// is built in one function and the request in another: the base_url finding
// fires on its own.
func TestPlatformRawHTTP_CatchesSplitConstruction(t *testing.T) {
	root := writePlatformFixture(t, map[string]string{
		"request.go": sanctionedRequestGo,
		"billing.go": `package platform

func (s *BillingService) portalURL() string {
	return s.client.base + "/v1/billing/portal-session"
}
`,
	})

	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Kind != PlatformRawHTTPKindBaseURL {
		t.Fatalf("findings = %+v, want one base_url", res.Findings)
	}
}

// TestPlatformRawHTTP_IgnoresTestFiles keeps the gate off test scaffolding,
// which legitimately drives httptest servers with hand-built requests.
func TestPlatformRawHTTP_IgnoresTestFiles(t *testing.T) {
	root := writePlatformFixture(t, map[string]string{
		"request.go": sanctionedRequestGo,
		"analytics_test.go": `package platform

import (
	"context"
	"net/http"
)

func helper(ctx context.Context, c *Client) {
	_, _ = http.NewRequestWithContext(ctx, "GET", c.base+"/v1/x", nil)
}
`,
	})

	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %+v, want none (test files are out of scope)", res.Findings)
	}
	if res.FilesChecked != 1 {
		t.Errorf("files_checked = %d, want 1", res.FilesChecked)
	}
}

// TestPlatformRawHTTP_MissingDirIsHardError: a scan that silently finds nothing
// is indistinguishable from a clean one, which would turn this gate off without
// anyone noticing.
func TestPlatformRawHTTP_MissingDirIsHardError(t *testing.T) {
	root := t.TempDir()
	if _, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root}); err == nil {
		t.Fatal("want error for missing internal/platform, got nil")
	}
}

func TestPlatformRawHTTP_ParseErrorIsWarningNotSilence(t *testing.T) {
	root := writePlatformFixture(t, map[string]string{
		"broken.go": "package platform\n\nfunc (\n",
	})

	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "broken.go") {
		t.Fatalf("warnings = %+v, want one mentioning broken.go", res.Warnings)
	}
}

// TestPlatformRawHTTP_RealPackageIsClean runs the gate against the actual
// repository tree. This is the check CI relies on; if it fails, a hand-rolled
// platform call has been reintroduced.
func TestPlatformRawHTTP_RealPackageIsClean(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	res, err := RunPlatformRawHTTPCheck(context.Background(), PlatformRawHTTPOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range res.Findings {
		t.Errorf("%s:%d [%s] %s — %s", f.File, f.Line, f.Kind, f.Match, f.Detail)
	}
	if res.FilesChecked == 0 {
		t.Fatal("files_checked = 0; the gate scanned nothing")
	}
}
