package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

const fakeInstaller = "echo installing\n"

func installServer(t *testing.T) (*httptest.Server, adaptercompat.Manifest) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeInstaller))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte(fakeInstaller))
	var m adaptercompat.Manifest
	m.MaxTested = "2.2.7"
	m.Install.Installer = srv.URL + "/v2/install"
	m.Install.InstallerSHA256 = hex.EncodeToString(sum[:])
	return srv, m
}

type recordingRunner struct {
	calls [][]string
	body  string
}

func (r *recordingRunner) run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	data, _ := os.ReadFile(args[0])
	r.body = string(data)
	return nil
}

func TestInstallManagedOpenCodeRunsTheVerifiedInstaller(t *testing.T) {
	srv, m := installServer(t)
	var r recordingRunner
	if err := InstallManagedOpenCode(context.Background(), srv.Client(), m, r.run); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(r.calls))
	}
	got := r.calls[0]
	want := []string{"bash", got[1], "--no-modify-path", "--version", "2.2.7"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("runner args = %q, want %q", got, want)
	}
	if r.body != fakeInstaller {
		t.Errorf("runner saw %q, want the downloaded installer", r.body)
	}
	if _, err := os.Stat(got[1]); !os.IsNotExist(err) {
		t.Errorf("installer temp file %s left behind", got[1])
	}
}

func TestInstallManagedOpenCodeRefusesADigestMismatch(t *testing.T) {
	srv, m := installServer(t)
	m.Install.InstallerSHA256 = strings.Repeat("0", 64)
	var r recordingRunner
	err := InstallManagedOpenCode(context.Background(), srv.Client(), m, r.run)
	if err == nil || !strings.Contains(err.Error(), m.Install.InstallerSHA256) || !strings.Contains(err.Error(), "not run") {
		t.Fatalf("err = %v, want a digest refusal naming the pin", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("runner called on a mismatched installer: %q", r.calls)
	}
	sum := sha256.Sum256([]byte(fakeInstaller))
	if !strings.Contains(err.Error(), hex.EncodeToString(sum[:])) {
		t.Errorf("err %v does not name the downloaded digest", err)
	}
}

// TestInstallManagedOpenCodeRemovesARefusedInstaller: the downloaded file a
// digest mismatch refuses is not left in the temporary directory.
func TestInstallManagedOpenCodeRemovesARefusedInstaller(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv, m := installServer(t)
	m.Install.InstallerSHA256 = strings.Repeat("0", 64)
	var r recordingRunner
	if err := InstallManagedOpenCode(context.Background(), srv.Client(), m, r.run); err == nil {
		t.Fatal("a mismatched installer was accepted")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("refused installer left behind: %s", e.Name())
	}
}

func TestInstallManagedOpenCodeRefusesNonHTTPS(t *testing.T) {
	_, m := installServer(t)
	m.Install.Installer = "http://example.invalid/install"
	var r recordingRunner
	if err := InstallManagedOpenCode(context.Background(), nil, m, r.run); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v, want an https refusal", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("runner called for a non-https installer")
	}
}

func TestInstallManagedOpenCodeShippedManifest(t *testing.T) {
	m := openCodeManifestForTest(t)
	if !strings.HasPrefix(m.Install.Installer, "https://") || len(m.Install.InstallerSHA256) != 64 {
		t.Fatalf("shipped manifest installer %q / %q", m.Install.Installer, m.Install.InstallerSHA256)
	}
}

func TestInstallManagedOpenCodeRefusals(t *testing.T) {
	big := strings.Repeat("x", openCodeInstallerMaxBytes+10)
	cases := map[string]struct {
		handler http.HandlerFunc
		edit    func(*adaptercompat.Manifest)
		want    string
	}{
		"http error": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, nil, "HTTP 404"},
		"oversize":   {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) }, nil, "over"},
		"redirect to http": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.invalid/install", http.StatusFound)
		}, nil, "refusing a redirect"},
		"no pin":        {nil, func(m *adaptercompat.Manifest) { m.Install.InstallerSHA256 = "" }, "sha256"},
		"no max tested": {nil, func(m *adaptercompat.Manifest) { m.MaxTested = "" }, "max-tested"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, m := installServer(t)
			if tc.handler != nil {
				srv.Config.Handler = tc.handler
			}
			if tc.edit != nil {
				tc.edit(&m)
			}
			var r recordingRunner
			err := InstallManagedOpenCode(context.Background(), srv.Client(), m, r.run)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(r.calls) != 0 {
				t.Fatal("runner called on a refused install")
			}
		})
	}
}

// TestInstallManagedOpenCodeWrapsAnInstallerFailure: a verified installer
// that exits non-zero fails the install, and its temp file is still removed.
func TestInstallManagedOpenCodeWrapsAnInstallerFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv, m := installServer(t)
	failing := func(context.Context, string, ...string) error { return errors.New("exit status 3") }
	err := InstallManagedOpenCode(context.Background(), srv.Client(), m, failing)
	if err == nil || !strings.Contains(err.Error(), "running the verified opencode installer") ||
		!strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v, want the wrapped installer failure", err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("installer temp file left behind: %v", entries)
	}
}

// TestInstallManagedOpenCodeKeepsTheCallersRedirectPolicy: a client that
// sets its own CheckRedirect keeps it.
func TestInstallManagedOpenCodeKeepsTheCallersRedirectPolicy(t *testing.T) {
	srv, m := installServer(t)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	client := srv.Client()
	called := false
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		called = true
		return errors.New("caller refused the redirect")
	}
	var r recordingRunner
	err := InstallManagedOpenCode(context.Background(), client, m, r.run)
	if !called || err == nil || !strings.Contains(err.Error(), "caller refused the redirect") {
		t.Fatalf("called=%v err=%v: the caller's CheckRedirect was not used", called, err)
	}
	if len(r.calls) != 0 {
		t.Fatal("runner called after a refused redirect")
	}
}
