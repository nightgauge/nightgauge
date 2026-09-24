package keychain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/zalando/go-keyring"
)

// No test here may reach the real OS keychain: every store runs on
// go-keyring's in-memory mock provider or on a stub backend.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// machineFile points the machine tier at a temp file for the test.
func machineFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nightgauge", "config.yaml")
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) { return path, nil }))
	return path
}

func writeMachine(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newStore(env map[string]string) *Store {
	return &Store{
		backend: libraryBackend{},
		timeout: time.Second,
		getenv:  func(k string) string { return env[k] },
	}
}

// The entry contract the VS Code extension relies on (through the CLI) and
// that `security find-generic-password -s nightgauge -a platform.license_key`
// finds. Changing either string orphans every stored key.
func TestEntryContractIsPinned(t *testing.T) {
	if Service != "nightgauge" {
		t.Errorf("Service = %q, want nightgauge", Service)
	}
	if AccountLicenseKey != "platform.license_key" {
		t.Errorf("AccountLicenseKey = %q, want platform.license_key", AccountLicenseKey)
	}
	if EnvLicenseKey != "NIGHTGAUGE_LICENSE_KEY" {
		t.Errorf("EnvLicenseKey = %q, want NIGHTGAUGE_LICENSE_KEY", EnvLicenseKey)
	}
}

func TestSetGetDeleteRoundTrip(t *testing.T) {
	keyring.MockInit()
	path := machineFile(t)
	s := newStore(nil)

	res, err := s.Set(AccountLicenseKey, "ib_live_roundtrip")
	if err != nil || res.Source != SourceKeychain {
		t.Fatalf("Set = %+v, %v; want source keychain", res, err)
	}
	got, err := s.Get(AccountLicenseKey)
	if err != nil || got.Value != "ib_live_roundtrip" || got.Source != SourceKeychain {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a keychain write must not create the machine file: %v", err)
	}
	removed, err := s.Delete(AccountLicenseKey)
	if err != nil || !removed.Keychain {
		t.Fatalf("Delete = %+v, %v", removed, err)
	}
	got, err = s.Get(AccountLicenseKey)
	if err != nil || got.Source != SourceNone || got.Value != "" {
		t.Fatalf("Get after Delete = %+v, %v; want source none", got, err)
	}
}

func TestGetFallsBackToMachineFileWhenKeychainUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service on this bus"))
	t.Cleanup(keyring.MockInit)
	path := machineFile(t)
	writeMachine(t, path, "platform:\n  license_key: ib_live_from_file\n")

	got, err := newStore(nil).Get(AccountLicenseKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceMachineFile || got.Value != "ib_live_from_file" || got.Path != path {
		t.Fatalf("Get = %+v; want machine-file value from %s", got, path)
	}
	if got.KeychainErr == nil {
		t.Fatal("KeychainErr = nil; the fallback must say why the keychain was not used")
	}
}

func TestSetFallsBackToPrivateMachineFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service on this bus"))
	t.Cleanup(keyring.MockInit)
	path := machineFile(t)
	writeMachine(t, path, "# keep me\ngithub_user: someone\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := newStore(nil).Set(AccountLicenseKey, "ib_live_fallback")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceMachineFile || res.KeychainErr == nil || res.Path != path {
		t.Fatalf("Set = %+v; want machine-file with a reason", res)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# keep me") || !strings.Contains(string(data), "github_user: someone") {
		t.Errorf("the fallback write dropped other content:\n%s", data)
	}
	value, _, _ := config.ReadMachineString(AccountLicenseKey)
	if value != "ib_live_fallback" {
		t.Errorf("machine file value = %q", value)
	}
}

func TestSetRefusesSymlinkedMachineFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	t.Cleanup(keyring.MockInit)
	path := machineFile(t)
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	writeMachine(t, target, "github_user: someone\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := newStore(nil).Set(AccountLicenseKey, "ib_live_nope"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Set through a symlink: err = %v, want a refusal", err)
	}
	data, _ := os.ReadFile(target)
	if strings.Contains(string(data), "ib_live_nope") {
		t.Fatal("the key was written through the symlink")
	}
}

// A keychain that never answers (a Linux host whose D-Bus stalls) must not
// hang the CLI: the call times out and the store falls back.
type stallingBackend struct{ release chan struct{} }

func (b stallingBackend) Get(string, string) (string, error) { <-b.release; return "", nil }
func (b stallingBackend) Set(string, string, string) error   { <-b.release; return nil }
func (b stallingBackend) Delete(string, string) error        { <-b.release; return nil }

func TestStalledKeychainTimesOutAndFallsBack(t *testing.T) {
	path := machineFile(t)
	writeMachine(t, path, "platform:\n  license_key: ib_live_after_timeout\n")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := &Store{backend: stallingBackend{release}, timeout: 20 * time.Millisecond, getenv: func(string) string { return "" }}

	got, err := s.Get(AccountLicenseKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceMachineFile || !errors.Is(got.KeychainErr, ErrTimeout) {
		t.Fatalf("Get = %+v; want machine-file after a timeout", got)
	}
}

// The resolution order is env > keychain > machine-file. Each row removes the
// higher source, so reordering the chain fails the row it demotes.
func TestResolveLicenseKeyOrder(t *testing.T) {
	keyring.MockInit()
	path := machineFile(t)
	writeMachine(t, path, "platform:\n  license_key: ib_live_file\n")
	if err := keyring.Set(Service, AccountLicenseKey, "ib_live_keychain"); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{EnvLicenseKey: "ib_live_env"}
	check := func(want Source, wantValue string) {
		t.Helper()
		got, err := newStore(env).ResolveLicenseKey()
		if err != nil || got.Source != want || got.Value != wantValue {
			t.Fatalf("ResolveLicenseKey = %+v, %v; want %s/%s", got, err, want, wantValue)
		}
	}
	check(SourceEnv, "ib_live_env")
	delete(env, EnvLicenseKey)
	check(SourceKeychain, "ib_live_keychain")
	if err := keyring.Delete(Service, AccountLicenseKey); err != nil {
		t.Fatal(err)
	}
	check(SourceMachineFile, "ib_live_file")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check(SourceNone, "")
}

func TestDeleteRemovesBothStores(t *testing.T) {
	keyring.MockInit()
	path := machineFile(t)
	writeMachine(t, path, "platform:\n  api_url: https://example.test\n  license_key: ib_live_file\n")
	if err := keyring.Set(Service, AccountLicenseKey, "ib_live_keychain"); err != nil {
		t.Fatal(err)
	}
	removed, err := newStore(nil).Delete(AccountLicenseKey)
	if err != nil || !removed.Keychain || !removed.MachineFile {
		t.Fatalf("Delete = %+v, %v; want both removed", removed, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "license_key") || !strings.Contains(string(data), "api_url") {
		t.Fatalf("machine file after Delete:\n%s", data)
	}
}

func TestNormalizeLicenseKey(t *testing.T) {
	if got, err := NormalizeLicenseKey("  ib_live_abc\n"); err != nil || got != "ib_live_abc" {
		t.Fatalf("NormalizeLicenseKey = %q, %v", got, err)
	}
	for _, bad := range []string{"", "  \n", "ib_live a", "ib_live\x00a", "two\nlines"} {
		_, err := NormalizeLicenseKey(bad)
		if err == nil {
			t.Errorf("NormalizeLicenseKey(%q) accepted", bad)
			continue
		}
		if bad != "" && strings.Contains(err.Error(), strings.TrimSpace(bad)) && strings.TrimSpace(bad) != "" {
			t.Errorf("error %q quotes the input", err)
		}
	}
}

// A write the keychain does not answer in time may still land after an unlock
// prompt, so it must not also be written to the plaintext file.
func TestStalledKeychainSetDoesNotFallBackToTheFile(t *testing.T) {
	path := machineFile(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := &Store{backend: stallingBackend{release}, timeout: 20 * time.Millisecond, getenv: func(string) string { return "" }}

	res, err := s.Set(AccountLicenseKey, "ib_live_slow")
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Set = %+v, %v; want a timeout error", res, err)
	}
	if strings.Contains(err.Error(), "ib_live_slow") {
		t.Fatal("the error quoted the key")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a timed-out keychain write fell back to %s", path)
	}
}

// Rotating the key into the keychain removes the old plaintext copy.
func TestKeychainSetRemovesPlaintextCopy(t *testing.T) {
	keyring.MockInit()
	path := machineFile(t)
	writeMachine(t, path, "platform:\n  api_url: https://example.test\n  license_key: ib_live_old\n")

	res, err := newStore(nil).Set(AccountLicenseKey, "ib_live_new")
	if err != nil || res.Source != SourceKeychain || !res.RemovedFileCopy || res.Path != path {
		t.Fatalf("Set = %+v, %v; want keychain with the file copy removed", res, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "ib_live_old") || !strings.Contains(string(data), "api_url") {
		t.Fatalf("machine file after Set:\n%s", data)
	}
}

func TestFingerprintIsStableAndShort(t *testing.T) {
	// scrypt("ib_live_abc", fingerprintSalt, 32768, 8, 1, 32)[:12 hex] — the
	// vector cmd/nightgauge/testdata/auth-license-contract.json pins, which
	// the TypeScript side asserts too.
	if got := Fingerprint("ib_live_abc"); got != "61ecedb83dd6" || got == Fingerprint("ib_live_abd") {
		t.Fatalf("Fingerprint = %q", got)
	}
	if Fingerprint("") != "" {
		t.Fatal("empty key has a fingerprint")
	}
}

func TestIsNoKeychain(t *testing.T) {
	for _, err := range []error{
		errors.New("dbus: couldn't determine address of session bus"),
		errors.New("The name org.freedesktop.secrets was not provided by any .service files"),
		&os.PathError{Op: "exec", Path: "/usr/bin/security", Err: os.ErrNotExist},
	} {
		if !IsNoKeychain(err) {
			t.Errorf("IsNoKeychain(%v) = false", err)
		}
	}
	for _, err := range []error{nil, ErrTimeout, errors.New("exit status 36")} {
		if IsNoKeychain(err) {
			t.Errorf("IsNoKeychain(%v) = true", err)
		}
	}
}

// On a CI host nothing is written: not the keychain, not the machine file
// (ADR-024 § 5). A shared runner would otherwise carry the key to the next job.
func TestSetInCIWritesNothing(t *testing.T) {
	for _, ci := range []string{"true", "TRUE", "1"} {
		t.Run(ci, func(t *testing.T) {
			keyring.MockInit()
			path := machineFile(t)
			s := newStore(map[string]string{"CI": ci})

			res, err := s.Set(AccountLicenseKey, "ib_live_ci")
			if !errors.Is(err, config.ErrCredentialWriteInCI) {
				t.Fatalf("Set in CI = %+v, %v; want ErrCredentialWriteInCI", res, err)
			}
			if !strings.Contains(err.Error(), EnvLicenseKey) {
				t.Errorf("error %q does not name %s", err, EnvLicenseKey)
			}
			if strings.Contains(err.Error(), "ib_live_ci") {
				t.Fatal("the error quotes the credential")
			}
			if v, gerr := keyring.Get(Service, AccountLicenseKey); gerr == nil {
				t.Errorf("the keychain holds %q after a CI write", v)
			}
			if _, serr := os.Stat(path); !errors.Is(serr, os.ErrNotExist) {
				t.Errorf("the machine file was written in CI: %v", serr)
			}
		})
	}
}

// Environment first, on every host and so on a CI host: a key left in the
// keychain by an earlier job never shadows the job's own.
func TestResolveLicenseKeyInCIPrefersEnvironment(t *testing.T) {
	keyring.MockInit()
	machineFile(t)
	if err := keyring.Set(Service, AccountLicenseKey, "ib_live_stale"); err != nil {
		t.Fatal(err)
	}
	s := newStore(map[string]string{"CI": "true", EnvLicenseKey: "ib_live_job"})
	got, err := s.ResolveLicenseKey()
	if err != nil || got.Value != "ib_live_job" || got.Source != SourceEnv {
		t.Fatalf("ResolveLicenseKey in CI = %+v, %v; want the environment's key", got, err)
	}
}
