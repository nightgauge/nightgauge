package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

// InstallRunner runs the verified installer: name is the program, args its
// arguments. RunInstaller is the default.
type InstallRunner func(ctx context.Context, name string, args ...string) error

// RunInstaller runs name with the caller's stdio.
func RunInstaller(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// openCodeInstallTimeout bounds the installer download.
const openCodeInstallTimeout = 2 * time.Minute

// openCodeInstallMaxRedirects bounds the redirects the download follows.
const openCodeInstallMaxRedirects = 10

// openCodeInstallerMaxBytes caps the download; the 2.x installer is ~15 KB.
const openCodeInstallerMaxBytes = 4 << 20

// httpsOnlyRedirects refuses a redirect off https.
func httpsOnlyRedirects(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect of the opencode installer to %s", req.URL)
	}
	if len(via) >= openCodeInstallMaxRedirects {
		return fmt.Errorf("too many redirects")
	}
	return nil
}

// InstallManagedOpenCode installs the manifest's max-tested opencode through
// the vendor's 2.x installer (#3383, keeping #3300's guarantees): it
// downloads the https installer to a private temporary file while hashing
// it, refuses it unless its sha256 is the manifest's pin, and only then runs
// it with `--no-modify-path --version <max_tested>`. Nothing runs before the
// check, and a refused file is removed. client nil uses a client with a
// download timeout; run nil uses RunInstaller.
func InstallManagedOpenCode(ctx context.Context, client *http.Client, m adaptercompat.Manifest, run InstallRunner) error {
	u, err := url.Parse(m.Install.Installer)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("refusing the opencode installer %q: it must be an https URL", m.Install.Installer)
	}
	if m.Install.InstallerSHA256 == "" || m.MaxTested == "" {
		return fmt.Errorf("the opencode manifest names no installer sha256 or max-tested version")
	}
	if client == nil {
		client = &http.Client{Timeout: openCodeInstallTimeout}
	}
	if client.CheckRedirect == nil {
		c := *client
		c.CheckRedirect = httpsOnlyRedirects
		client = &c
	}
	if run == nil {
		run = RunInstaller
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("building the opencode installer request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the opencode installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the opencode installer %s: HTTP %d", u, resp.StatusCode)
	}

	f, err := os.CreateTemp("", "nightgauge-opencode-install-*")
	if err != nil {
		return fmt.Errorf("creating the opencode installer temp file: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, openCodeInstallerMaxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("downloading the opencode installer: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("writing the opencode installer temp file: %w", closeErr)
	}
	if n > openCodeInstallerMaxBytes {
		return fmt.Errorf("the opencode installer at %s is over %d bytes; it was not run", u, openCodeInstallerMaxBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.Install.InstallerSHA256 {
		return fmt.Errorf("the opencode installer at %s has sha256 %s, not the pinned %s; it was not run",
			u, got, m.Install.InstallerSHA256)
	}
	if err := run(ctx, "bash", path, "--no-modify-path", "--version", m.MaxTested); err != nil {
		return fmt.Errorf("running the verified opencode installer: %w", err)
	}
	return nil
}
