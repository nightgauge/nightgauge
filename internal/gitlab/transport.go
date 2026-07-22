package gitlab

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/config"
)

// BuildTransport constructs an *http.Client for a GitLab forge entry.
// CA sources (in priority order):
//  1. entry.CABundle resolved relative to configDir
//  2. SSL_CERT_FILE env var
//  3. System cert pool (when neither above is set)
//
// Proxy (in priority order):
//  1. entry.Proxy (explicit URL)
//  2. http.ProxyFromEnvironment (HTTPS_PROXY / HTTP_PROXY / NO_PROXY)
//
// InsecureSkipVerify is only set when entry.InsecureSkipTLS is true;
// a warning is emitted to stderr when this path is taken.
func BuildTransport(entry config.ForgeConfigEntry, configDir string) (*http.Client, error) {
	certPool, err := x509.SystemCertPool()
	if err != nil {
		// System cert pool may be unavailable on some platforms (e.g. Windows without certs installed).
		certPool = x509.NewCertPool()
	}

	if entry.CABundle != "" {
		bundlePath := entry.CABundle
		if !filepath.IsAbs(bundlePath) {
			bundlePath = filepath.Join(configDir, bundlePath)
		}
		pem, err := os.ReadFile(bundlePath)
		if err != nil {
			return nil, fmt.Errorf("gitlab: read CA bundle %q: %w", bundlePath, err)
		}
		certPool.AppendCertsFromPEM(pem)
	}

	if sslFile := os.Getenv("SSL_CERT_FILE"); sslFile != "" {
		pem, err := os.ReadFile(sslFile)
		if err != nil {
			return nil, fmt.Errorf("gitlab: read SSL_CERT_FILE %q: %w", sslFile, err)
		}
		certPool.AppendCertsFromPEM(pem)
	}

	tlsCfg := &tls.Config{RootCAs: certPool}
	if entry.InsecureSkipTLS {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 — user-configured opt-in with warning
		fmt.Fprintf(os.Stderr, "WARNING: gitlab: InsecureSkipTLS=true for %q — TLS certificate verification is disabled\n", entry.BaseURL)
	}

	transport := &http.Transport{
		TLSClientConfig: tlsCfg,
	}

	if entry.Proxy != "" {
		proxyURL, err := url.Parse(entry.Proxy)
		if err != nil {
			return nil, fmt.Errorf("gitlab: parse proxy URL %q: %w", entry.Proxy, err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	return &http.Client{
		Transport: transport,
		Timeout:   defaultTimeout,
	}, nil
}
