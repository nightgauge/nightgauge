package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/keychain"
	"github.com/zalando/go-keyring"
)

// licenseFixture isolates the machine tier and the (mock) keychain.
func licenseFixture(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) { return path, nil }))
	t.Setenv(keychain.EnvLicenseKey, "")
	t.Setenv("NIGHTGAUGE_PLATFORM_URL", "")
	return path
}

func runLicenseCmd(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := authLicenseCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestLicenseKeyResolutionOrder pins the order the CLI and the daemon share:
// env, then keychain, then the machine-tier file. Each step removes the source
// above it, so reordering the chain fails the step it demotes. It drives the
// backfill resolver and `auth license status`, the two CLI consumers.
func TestLicenseKeyResolutionOrder(t *testing.T) {
	path := licenseFixture(t)
	if err := os.WriteFile(path, []byte("platform:\n  license_key: ib_live_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Set(keychain.Service, keychain.AccountLicenseKey, "ib_live_keychain"); err != nil {
		t.Fatal(err)
	}

	check := func(wantKey string, wantSource keychain.Source) {
		t.Helper()
		creds, err := resolvePlatformCreds()
		if wantKey == "" {
			if err == nil || !strings.Contains(err.Error(), "nightgauge auth license set") {
				t.Fatalf("resolvePlatformCreds err = %v; want the auth license set remedy", err)
			}
		} else if err != nil || creds.licenseKey != wantKey {
			t.Fatalf("resolvePlatformCreds = %q, %v; want %q", creds.licenseKey, err, wantKey)
		}
		out, _, err := runLicenseCmd(t, "", "status")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "source: "+string(wantSource)+"\n") {
			t.Fatalf("status = %q; want source %s", out, wantSource)
		}
		if wantKey != "" && strings.Contains(out, wantKey) {
			t.Fatal("status printed the key")
		}
	}

	t.Setenv(keychain.EnvLicenseKey, "ib_live_env")
	check("ib_live_env", keychain.SourceEnv)
	t.Setenv(keychain.EnvLicenseKey, "")
	check("ib_live_keychain", keychain.SourceKeychain)
	if err := keyring.Delete(keychain.Service, keychain.AccountLicenseKey); err != nil {
		t.Fatal(err)
	}
	check("ib_live_file", keychain.SourceMachineFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("", keychain.SourceNone)
}

func TestAuthLicenseSetReadsStdinAndNeverEchoes(t *testing.T) {
	licenseFixture(t)
	const key = "ib_live_from_stdin"
	out, errOut, err := runLicenseCmd(t, key+"\n", "set")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+errOut, key) {
		t.Fatalf("set echoed the key: %q %q", out, errOut)
	}
	got, err := keyring.Get(keychain.Service, keychain.AccountLicenseKey)
	if err != nil || got != key {
		t.Fatalf("keychain entry = %q, %v", got, err)
	}

	out, _, err = runLicenseCmd(t, "", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, key) {
		t.Fatal("status --json carried the key")
	}
	var st licenseStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Source != keychain.SourceKeychain || !st.KeychainAvailable {
		t.Fatalf("status --json = %q (%v)", out, err)
	}
}

// `set --json` is how the VS Code extension learns whether the key reached the
// keychain or only the machine-tier file.
func TestAuthLicenseSetJSONReportsTheStore(t *testing.T) {
	path := licenseFixture(t)
	const key = "ib_live_json"
	out, _, err := runLicenseCmd(t, key, "set", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r licenseSetResult
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Source != keychain.SourceKeychain ||
		r.Fingerprint != keychain.Fingerprint(key) {
		t.Fatalf("set --json = %q (%v)", out, err)
	}
	keyring.MockInitWithError(errors.New("no secret service"))
	out, _, err = runLicenseCmd(t, key, "set", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, key) {
		t.Fatal("set --json carried the key")
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Source != keychain.SourceMachineFile || r.Path != path || r.KeychainError == "" {
		t.Fatalf("set --json without a keychain = %q (%v)", out, err)
	}
}

func TestAuthLicenseSetRejectsArgv(t *testing.T) {
	licenseFixture(t)
	const key = "ib_live_in_argv"
	_, errOut, err := runLicenseCmd(t, "", "set", key)
	if err == nil {
		t.Fatal("set accepted the key as an argument")
	}
	if strings.Contains(err.Error(), key) || strings.Contains(errOut, key) {
		t.Fatal("the refusal quoted the key back")
	}
	if _, err := keyring.Get(keychain.Service, keychain.AccountLicenseKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("an argv key was stored: %v", err)
	}
}

func TestAuthLicenseSetFallsBackWithoutKeychain(t *testing.T) {
	path := licenseFixture(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	out, errOut, err := runLicenseCmd(t, "ib_live_headless", "set")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "No OS keychain is available") || !strings.Contains(out, path) {
		t.Fatalf("set without a keychain: out=%q err=%q", out, errOut)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("machine file: %v, %v", info, err)
	}
	out, _, err = runLicenseCmd(t, "", "status")
	if err != nil || !strings.Contains(out, "source: machine-file") || !strings.Contains(out, "keychain: unavailable") {
		t.Fatalf("status = %q, %v", out, err)
	}
}

func TestAuthLicenseClearRemovesEveryStoredCopy(t *testing.T) {
	path := licenseFixture(t)
	if err := os.WriteFile(path, []byte("platform:\n  license_key: ib_live_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runLicenseCmd(t, "ib_live_keychain", "set"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runLicenseCmd(t, "", "clear"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runLicenseCmd(t, "", "status")
	if err != nil || !strings.Contains(out, "source: none") {
		t.Fatalf("status after clear = %q, %v", out, err)
	}
}

// contract is testdata/auth-license-contract.json, the fixture the VS Code
// extension's bridge test reads too.
type contract struct {
	Keychain    struct{ Service, Account string }
	Command     []string
	Subcommands []string
	Fingerprint struct{ Key, Value string }
	Outputs     map[string]map[string]any
}

func loadContract(t *testing.T) contract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "auth-license-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c contract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// jsonKeys marshals v and returns its top-level field names.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fixtureKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestAuthLicenseContractMatchesFixture fails when a subcommand or a JSON field
// the extension parses is renamed on the Go side without the fixture (and so
// the extension) following.
func TestAuthLicenseContractMatchesFixture(t *testing.T) {
	c := loadContract(t)
	if c.Keychain.Service != keychain.Service || c.Keychain.Account != keychain.AccountLicenseKey {
		t.Errorf("keychain entry = %+v, want %s/%s", c.Keychain, keychain.Service, keychain.AccountLicenseKey)
	}
	if got := keychain.Fingerprint(c.Fingerprint.Key); got != c.Fingerprint.Value {
		t.Errorf("Fingerprint(%q) = %q, fixture says %q", c.Fingerprint.Key, got, c.Fingerprint.Value)
	}
	if strings.Join(c.Command, " ") != "auth license" || authCmd().Use != "auth" || authLicenseCmd().Use != "license" {
		t.Errorf("command path drifted: fixture %v", c.Command)
	}
	var subs []string
	for _, sub := range authLicenseCmd().Commands() {
		subs = append(subs, sub.Use)
	}
	sort.Strings(subs)
	want := append([]string(nil), c.Subcommands...)
	sort.Strings(want)
	if strings.Join(subs, ",") != strings.Join(want, ",") {
		t.Errorf("subcommands = %v, fixture %v", subs, want)
	}
	for _, sub := range c.Subcommands {
		for _, cmd := range authLicenseCmd().Commands() {
			if cmd.Use == sub && cmd.Flags().Lookup("json") == nil {
				t.Errorf("auth license %s has no --json flag", sub)
			}
		}
	}
	full := map[string]any{
		"set": licenseSetResult{Source: "keychain", Path: "p", KeychainError: "e", Fingerprint: "f", RemovedFileCopy: true},
		"status": licenseStatus{Source: "keychain", Path: "p", KeychainAvailable: true, KeychainError: "e",
			Fingerprint: "f"},
		"clear": licenseClearResult{KeychainCleared: true, FileCleared: true, Path: "p", LocalCleared: true,
			LocalPath: "l", KeychainError: "e", NoKeychain: true},
	}
	for name, v := range full {
		got, want := jsonKeys(t, v), fixtureKeys(c.Outputs[name])
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s --json fields = %v, fixture %v", name, got, want)
		}
	}
}

// A keychain that failed in a way that may leave the entry behind (here an
// unexplained `security` exit) makes clear exit non-zero.
func TestAuthLicenseClearFailsWhenKeychainErrs(t *testing.T) {
	path := licenseFixture(t)
	if err := os.WriteFile(path, []byte("platform:\n  license_key: ib_live_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("exit status 36"))
	out, _, err := runLicenseCmd(t, "", "clear", "--json")
	if err == nil {
		t.Fatal("clear exited zero although the keychain entry may remain")
	}
	var r licenseClearResult
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || r.KeychainCleared || !r.FileCleared ||
		r.KeychainError == "" || r.NoKeychain {
		t.Fatalf("clear --json = %q (%v)", out, jerr)
	}
}

// On a host with no keychain service (the condition under which set falls
// back to the file), clearing the file copy is complete: exit zero.
func TestAuthLicenseClearSucceedsWithoutAKeychain(t *testing.T) {
	path := licenseFixture(t)
	if err := os.WriteFile(path, []byte("platform:\n  license_key: ib_live_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("dbus: couldn't determine address of session bus"))
	out, _, err := runLicenseCmd(t, "", "clear", "--json")
	if err != nil {
		t.Fatalf("clear without a keychain: %v", err)
	}
	var r licenseClearResult
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || !r.FileCleared || !r.NoKeychain {
		t.Fatalf("clear --json = %q (%v)", out, jerr)
	}
}

func TestAuthLicenseClearRemovesLocalTierKey(t *testing.T) {
	licenseFixture(t)
	wd := t.TempDir()
	t.Chdir(wd)
	local := config.LocalConfigPath(wd)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("platform:\n  license_key: ib_live_local\nlog_level: debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runLicenseCmd(t, "", "clear", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r licenseClearResult
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil || !r.LocalCleared {
		t.Fatalf("clear --json = %q (%v)", out, jerr)
	}
	data, _ := os.ReadFile(local)
	if strings.Contains(string(data), "ib_live_local") || !strings.Contains(string(data), "log_level") {
		t.Fatalf("local tier after clear:\n%s", data)
	}
}
