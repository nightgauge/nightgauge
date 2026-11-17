package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

const fakeOpenCodeInstaller = "echo installing opencode\n"

// stubOpenCodeInstall points the install verb's seams at a local TLS server
// serving fakeOpenCodeInstaller under a manifest pinning its sha256, and at
// a runner that records instead of executing (#3383).
func stubOpenCodeInstall(t *testing.T) (*adaptercompat.Manifest, *[][]string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fakeOpenCodeInstaller))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte(fakeOpenCodeInstaller))
	m := &adaptercompat.Manifest{MaxTested: "2.2.7"}
	m.Install.Installer = srv.URL + "/v2/install"
	m.Install.InstallerSHA256 = hex.EncodeToString(sum[:])

	var calls [][]string
	prevManifest, prevClient, prevRunner := opencodeInstallManifest, opencodeInstallClient, opencodeInstallRunner
	t.Cleanup(func() {
		opencodeInstallManifest, opencodeInstallClient, opencodeInstallRunner = prevManifest, prevClient, prevRunner
	})
	opencodeInstallManifest = func() (adaptercompat.Manifest, error) { return *m, nil }
	opencodeInstallClient = srv.Client()
	opencodeInstallRunner = func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	return m, &calls
}

func runOpenCodeInstall(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := opencodeCmd()
	out := new(strings.Builder)
	cmd.SetOut(out)
	cmd.SetErr(new(strings.Builder))
	cmd.SetArgs(append([]string{"install"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// TestOpenCodeInstallVerbIsRegistered: the remedy every OpenCode refusal and
// `nightgauge doctor` print names a verb that exists.
func TestOpenCodeInstallVerbIsRegistered(t *testing.T) {
	sub, _, err := opencodeCmd().Find([]string{"install"})
	if err != nil || sub.Name() != "install" {
		t.Fatalf("opencode install is not registered: %v", err)
	}
	if want := "nightgauge opencode install"; adapters.OpenCodeInstallCommand != want {
		t.Errorf("OpenCodeInstallCommand = %q, want %q", adapters.OpenCodeInstallCommand, want)
	}
}

// TestOpenCodeInstallVerbRunsTheVerifiedInstallerAndPrintsThePin.
func TestOpenCodeInstallVerbRunsTheVerifiedInstallerAndPrintsThePin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, calls := stubOpenCodeInstall(t)
	out, err := runOpenCodeInstall(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(*calls))
	}
	got := (*calls)[0]
	if len(got) != 5 || got[0] != "bash" || strings.Join(got[2:], " ") != "--no-modify-path --version 2.2.7" {
		t.Errorf("runner args = %q, want bash <installer> --no-modify-path --version 2.2.7", got)
	}
	pin := filepath.Join(home, ".opencode", "bin", "opencode")
	for _, want := range []string{"Installed opencode 2.2.7", "opencode.binary: " + pin} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// TestOpenCodeInstallVerbPropagatesARefusal: a digest mismatch fails the
// verb, runs nothing and prints no success line.
func TestOpenCodeInstallVerbPropagatesARefusal(t *testing.T) {
	m, calls := stubOpenCodeInstall(t)
	m.Install.InstallerSHA256 = strings.Repeat("0", 64)
	out, err := runOpenCodeInstall(t)
	if err == nil || !strings.Contains(err.Error(), "not run") {
		t.Fatalf("err = %v, want the digest refusal", err)
	}
	if len(*calls) != 0 {
		t.Errorf("runner called on a refused installer: %q", *calls)
	}
	if out != "" {
		t.Errorf("a refused install printed %q", out)
	}
}

// TestOpenCodeInstallVerbTakesNoArguments.
func TestOpenCodeInstallVerbTakesNoArguments(t *testing.T) {
	_, calls := stubOpenCodeInstall(t)
	if _, err := runOpenCodeInstall(t, "2.2.7"); err == nil {
		t.Fatal("opencode install accepted a positional argument")
	}
	if len(*calls) != 0 {
		t.Errorf("runner called after an argument error: %q", *calls)
	}
}
