package gitlab

import (
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// selfSignedPEM is a minimal self-signed CA PEM used only to verify cert-pool
// loading — the cert is intentionally expired/fake and never used for TLS.
const selfSignedPEM = `-----BEGIN CERTIFICATE-----
MIICEzCCAXygAwIBAgIQVBMbmQGGGBLWlRHuOCTjlTANBgkqhkiG9w0BAQsFADAS
MRAwDgYDVQQKEwdBY21lIENvMCAXDTcwMDEwMTAwMDAwMFoYDzIwODQwMTI5MDYK
MDAwMFowEjEQMA4GA1UEChMHQWNtZSBDbzCBnzANBgkqhkiG9w0BAQEFAAOBjQAw
gYkCgYEA2a2rwplBQLEqkMWHupvGrVSp2lh/Ztq1VN2XZsHIXXSbBt2KPMoNZkSo
1GCb88IYnKEqGPbWBOBT3vFu9P3cxjIzPkXWuC7nEwDuHNnFwGRN3lpT8/9DXzGb
nHB7hzGVxQVaVYlVxqCIrEuCxkf5L3Wd7Y0vpjUcaWiW+n0CAwEAAaNmMGQwDgYD
VR0PAQH/BAQDAgGGMBIGA1UdEwEB/wQIMAYBAf8CAQIwHQYDVR0OBBYEFKG7JOKM
EHKFlZvPZJXqUMR1aRmAMB8GA1UdIwQYMBaAFKG7JOKMEHKFlZvPZJXqUMR1aRmA
MA0GCSqGSIb3DQEBCwUAA4GBABrXkQ+dBQnV6sFIXq4yFLhQRjbsBJj8cCnKkXxh
5oYHRYVUTv0gBQmNJM5sD4H9fbbLFo6p3Xy+l3Ub+Y9PL+OADX6MOjA7sDw7WT
OxhVqXI6bFbfKUhSZXnzUTPBIbzS0Z00DL8rCBpMNiSr1UOfhSt7IK4LxRnj3pS
-----END CERTIFICATE-----
`

func writeTempPEM(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ca-*.pem")
	if err != nil {
		t.Fatalf("create temp PEM: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("write PEM: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestBuildTransport_DefaultsTimeout(t *testing.T) {
	entry := config.ForgeConfigEntry{Kind: "gitlab", BaseURL: "https://gitlab.example.com"}
	c, err := BuildTransport(entry, "")
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	if c.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, defaultTimeout)
	}
}

func TestBuildTransport_CABundle(t *testing.T) {
	pemPath := writeTempPEM(t, selfSignedPEM)
	entry := config.ForgeConfigEntry{
		Kind:     "gitlab",
		BaseURL:  "https://gitlab.example.com",
		CABundle: pemPath,
	}
	c, err := BuildTransport(entry, "")
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("expected non-nil TLS cert pool after CA bundle load")
	}
}

func TestBuildTransport_CABundle_RelativePath(t *testing.T) {
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(pemPath, []byte(selfSignedPEM), 0o644); err != nil {
		t.Fatalf("write PEM: %v", err)
	}
	entry := config.ForgeConfigEntry{
		Kind:     "gitlab",
		BaseURL:  "https://gitlab.example.com",
		CABundle: "ca.pem",
	}
	c, err := BuildTransport(entry, dir)
	if err != nil {
		t.Fatalf("BuildTransport with relative CABundle: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("expected non-nil TLS cert pool for relative CA bundle path")
	}
}

func TestBuildTransport_SSLCertFileEnv(t *testing.T) {
	pemPath := writeTempPEM(t, selfSignedPEM)
	t.Setenv("SSL_CERT_FILE", pemPath)

	entry := config.ForgeConfigEntry{Kind: "gitlab", BaseURL: "https://gitlab.example.com"}
	c, err := BuildTransport(entry, "")
	if err != nil {
		t.Fatalf("BuildTransport with SSL_CERT_FILE: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("expected non-nil TLS cert pool from SSL_CERT_FILE")
	}
}

func TestBuildTransport_ProxyURL(t *testing.T) {
	entry := config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "https://gitlab.example.com",
		Proxy:   "http://proxy.corp.example.com:3128",
	}
	c, err := BuildTransport(entry, "")
	if err != nil {
		t.Fatalf("BuildTransport with proxy: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.Proxy == nil {
		t.Error("expected non-nil Proxy func for explicit entry.Proxy")
	}
}

func TestBuildTransport_ProxyEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example.com:8080")

	entry := config.ForgeConfigEntry{Kind: "gitlab", BaseURL: "https://gitlab.example.com"}
	c, err := BuildTransport(entry, "")
	if err != nil {
		t.Fatalf("BuildTransport with HTTPS_PROXY: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	// ProxyFromEnvironment is a non-nil func; we can't compare func values directly
	// but can verify it was set (non-nil).
	if tr.Proxy == nil {
		t.Error("expected non-nil Proxy func for ProxyFromEnvironment fallback")
	}
}

func TestBuildTransport_InsecureSkipVerify(t *testing.T) {
	// Capture stderr to verify the warning is emitted.
	orig := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	entry := config.ForgeConfigEntry{
		Kind:            "gitlab",
		BaseURL:         "https://self-signed.example.com",
		InsecureSkipTLS: true,
	}
	c, err := BuildTransport(entry, "")

	w.Close()
	os.Stderr = orig

	stderrBuf := make([]byte, 512)
	n, _ := r.Read(stderrBuf)
	r.Close()
	warnOutput := string(stderrBuf[:n])

	if err != nil {
		t.Fatalf("BuildTransport InsecureSkipVerify: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("expected non-nil TLSClientConfig")
	}
	if !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify=true when InsecureSkipTLS=true")
	}
	if !containsAny(warnOutput, "InsecureSkipTLS", "WARNING") {
		t.Errorf("expected warning on stderr for InsecureSkipTLS, got: %q", warnOutput)
	}
}

func TestBuildTransport_InvalidCABundle(t *testing.T) {
	entry := config.ForgeConfigEntry{
		Kind:     "gitlab",
		BaseURL:  "https://gitlab.example.com",
		CABundle: "/nonexistent/path/ca.pem",
	}
	_, err := BuildTransport(entry, "")
	if err == nil {
		t.Fatal("expected error for non-existent CA bundle path")
	}
}

func TestBuildTransport_InvalidProxyURL(t *testing.T) {
	entry := config.ForgeConfigEntry{
		Kind:    "gitlab",
		BaseURL: "https://gitlab.example.com",
		Proxy:   "://invalid-proxy",
	}
	_, err := BuildTransport(entry, "")
	if err == nil {
		t.Fatal("expected error for invalid proxy URL")
	}
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

// verifyCertPool is a helper that extracts the TLS config from the returned
// *http.Client and returns its cert pool for assertions.
func verifyCertPool(t *testing.T, c *http.Client) *tls.Config {
	t.Helper()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	return tr.TLSClientConfig
}
