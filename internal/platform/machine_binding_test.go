package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
)

// licenseValidateCapture stands up a platform stub that records every decoded
// /v1/license/validate request body and always answers with a valid pro
// license.
func licenseValidateCapture(t *testing.T) (*httptest.Server, func() []map[string]interface{}) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []map[string]interface{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/validate":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
			jsonResponse(w, map[string]interface{}{
				"valid": true, "status": "active", "tier": "pro",
				"machineBound": true, "machineCount": 1,
			})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]interface{}, len(bodies))
		copy(out, bodies)
		return out
	}
}

func onlineLicenseService(t *testing.T, baseURL, sessionKey string) *LicenseService {
	t.Helper()
	c, err := NewClient(Config{BaseURL: baseURL, LicenseKey: sessionKey})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return NewLicenseService(c)
}

// wantHash re-derives the wire digest from the SPEC — HMAC-SHA256 keyed by the
// license key over the machine id, and nothing else — taking the machine id as
// a plain string. It deliberately does not call into MachineInfo, so a change
// to the production derivation makes these tests fail instead of following
// them; TestMachineInfo_Hash_PinsTheWireDigest pins the bytes themselves
// against a value computed outside Go entirely.
func wantHash(key, machineID string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(machineID))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestLicenseService_Validate_SendsMachineBinding — the session-key path must
// carry the caller's machine identity so the platform can write a
// license_machines row and enforce the per-tier seat limit (#1334).
func TestLicenseService_Validate_SendsMachineBinding(t *testing.T) {
	srv, bodies := licenseValidateCapture(t)
	svc := onlineLicenseService(t, srv.URL, "SESSION-KEY")

	machine := MachineInfo{MachineID: "editor-install-1", Hostname: "build-box", Platform: "darwin"}
	if _, err := svc.Validate(context.Background(), machine); err != nil {
		t.Fatal(err)
	}

	got := bodies()
	if len(got) != 1 {
		t.Fatalf("platform received %d validate requests, want 1", len(got))
	}
	if id, _ := got[0]["machineId"].(string); id != wantHash("SESSION-KEY", machine.MachineID) {
		t.Errorf("machineId = %q, want HMAC-SHA256(licenseKey, fingerprint) = %q", id, wantHash("SESSION-KEY", machine.MachineID))
	}
	if host, _ := got[0]["hostname"].(string); host != "build-box" {
		t.Errorf("hostname = %q, want build-box", host)
	}
	if plat, _ := got[0]["platform"].(string); plat != "darwin" {
		t.Errorf("platform = %q, want darwin", plat)
	}
}

// TestLicenseService_ValidateKey_SendsMachineBinding — the "Activate License"
// path binds the same machine, hashed under the ENTERED key (not the session
// key), because that is the license the seat is being taken on.
func TestLicenseService_ValidateKey_SendsMachineBinding(t *testing.T) {
	srv, bodies := licenseValidateCapture(t)
	svc := onlineLicenseService(t, srv.URL, "SESSION-KEY")

	machine := MachineInfo{MachineID: "editor-install-1", Hostname: "build-box", Platform: "darwin"}
	if _, err := svc.ValidateKey(context.Background(), "ENTERED-KEY", machine); err != nil {
		t.Fatal(err)
	}

	got := bodies()
	if len(got) != 1 {
		t.Fatalf("platform received %d validate requests, want 1", len(got))
	}
	if id, _ := got[0]["machineId"].(string); id != wantHash("ENTERED-KEY", machine.MachineID) {
		t.Errorf("machineId = %q, want the hash keyed by the entered key %q", id, wantHash("ENTERED-KEY", machine.MachineID))
	}
	if id, _ := got[0]["machineId"].(string); id == wantHash("SESSION-KEY", machine.MachineID) {
		t.Error("machineId was keyed by the session key, not the entered key")
	}
}

// TestLicenseService_Validate_ReusesRememberedMachine — a validate that
// arrives without machine context (the params-less platform.license IPC
// method) must bind the machine a previous caller identified, not a
// host-derived second identity that would consume an extra seat.
func TestLicenseService_Validate_ReusesRememberedMachine(t *testing.T) {
	srv, bodies := licenseValidateCapture(t)
	svc := onlineLicenseService(t, srv.URL, "SESSION-KEY")

	machine := MachineInfo{MachineID: "editor-install-1", Hostname: "build-box", Platform: "darwin"}
	if _, err := svc.ValidateKey(context.Background(), "ENTERED-KEY", machine); err != nil {
		t.Fatal(err)
	}
	// Zero MachineInfo, and a cold cache (ValidateKey never writes it).
	if _, err := svc.Validate(context.Background(), MachineInfo{}); err != nil {
		t.Fatal(err)
	}

	got := bodies()
	if len(got) != 2 {
		t.Fatalf("platform received %d validate requests, want 2", len(got))
	}
	if id, _ := got[1]["machineId"].(string); id != wantHash("SESSION-KEY", machine.MachineID) {
		t.Errorf("second machineId = %q, want the remembered fingerprint hashed under the session key %q",
			id, wantHash("SESSION-KEY", machine.MachineID))
	}
}

// TestMachineInfo_Resolve_FillsFromHost — with nothing remembered and nothing
// supplied, a headless daemon still binds one identifiable seat.
func TestMachineInfo_Resolve_FillsFromHost(t *testing.T) {
	// Isolate the persisted machine-id so the test mints one in a temp home
	// rather than in the developer's own ~/.nightgauge.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home resolution
	t.Setenv(machineIDEnv, "")

	got := MachineInfo{}.Resolve()
	if got.MachineID == "" {
		t.Error("Resolve left MachineID empty — a headless daemon would bind no seat")
	}
	if got.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", got.Platform, runtime.GOOS)
	}
	if got.Hostname == "" {
		t.Error("Resolve left Hostname empty")
	}
}

// TestMachineInfo_Hash_KeyedAndStable — the wire value is the contract hash:
// never the raw fingerprint, stable for one (key, machine) pair, and different
// across licenses so a fingerprint cannot be correlated between accounts.
func TestMachineInfo_Hash_KeyedAndStable(t *testing.T) {
	m := MachineInfo{MachineID: "editor-install-1", Hostname: "build-box", Platform: "darwin"}

	h := m.Hash("KEY-A")
	if h == "" {
		t.Fatal("Hash returned empty for a fully-populated machine")
	}
	if len(h) != 64 {
		t.Errorf("Hash length = %d, want 64 hex chars", len(h))
	}
	if h == m.MachineID {
		t.Error("Hash leaked the raw machine id onto the wire")
	}
	if m.Hash("KEY-A") != h {
		t.Error("Hash is not stable for the same key and machine — each run would take a new seat")
	}
	if m.Hash("KEY-B") == h {
		t.Error("Hash is not keyed by the license key")
	}
	if got := m.Hash(""); got != "" {
		t.Errorf("Hash with no license key = %q, want empty (nothing to bind)", got)
	}
	if got := (MachineInfo{}).Hash("KEY-A"); got != "" {
		t.Errorf("Hash with no machine id = %q, want empty", got)
	}
	// Context-only: nothing identifies the installation, so there is nothing
	// to bind. A hash here would be a hash of hostname alone, which every
	// machine on that host name would share.
	if got := (MachineInfo{Hostname: "build-box", Platform: "darwin"}).Hash("KEY-A"); got != "" {
		t.Errorf("Hash with context but no machine id = %q, want empty", got)
	}
	if other := (MachineInfo{MachineID: "editor-install-2", Hostname: "build-box", Platform: "darwin"}).Hash("KEY-A"); other == h {
		t.Error("two installations hashed to one seat — the platform cannot count seats")
	}
}

// TestMachineInfo_Hash_PinsTheWireDigest pins the exact wire bytes for a fixed
// (license key, machine id) pair. The expected values were computed outside Go
// entirely:
//
//	printf '%s' 'editor-install-1' | openssl dgst -sha256 -hmac 'KEY-A' -hex
//
// so no change to the derivation in machine_binding.go can move them and stay
// green. That matters because this digest is the primary key of a
// license_machines row: re-keying it re-binds every already-bound machine,
// each installation takes a fresh seat, and a 3-seat pro license fills with
// duplicates of the same laptops and then rejects its own owner as
// MACHINE_LIMIT. Before #1334's review this was derived from the production
// function under test, so reversing the hashed field order left both the
// platform and ipc suites green.
func TestMachineInfo_Hash_PinsTheWireDigest(t *testing.T) {
	m := MachineInfo{MachineID: "editor-install-1", Hostname: "build-box", Platform: "darwin"}

	const (
		underKeyA       = "938e44a9b13926f9da3b21203557da335705a46b13fd3d39fd0b3a745935dfe0"
		underSessionKey = "ff41728112fa8992d3dcb21dfcbe66bf552359da6804b1a65d72f9af7f833743"
	)
	if got := m.Hash("KEY-A"); got != underKeyA {
		t.Errorf("Hash(KEY-A) = %q, want %q — the wire identity derivation changed;\n"+
			"if that is intentional, every already-bound machine re-binds as a new seat",
			got, underKeyA)
	}
	if got := m.Hash("SESSION-KEY"); got != underSessionKey {
		t.Errorf("Hash(SESSION-KEY) = %q, want %q", got, underSessionKey)
	}
}

// TestMachineInfo_Hash_IgnoresHostnameAndPlatform is the seat-stability
// invariant: one installation keeps ONE seat for its whole life. The identity
// is the machine id alone, so a rename — macOS appending .local on a network
// join, a devcontainer rebuild minting a fresh random hostname, a corporate
// re-image — must not mint a seat. Without this, turning enforcement on (the
// point of #1334) locks a paying pro user out after three hostname changes.
func TestMachineInfo_Hash_IgnoresHostnameAndPlatform(t *testing.T) {
	const key = "PRO-KEY"
	base := MachineInfo{MachineID: "editor-install-uuid", Hostname: "laptop", Platform: "darwin"}
	want := base.Hash(key)

	renames := []MachineInfo{
		{MachineID: "editor-install-uuid", Hostname: "laptop.local", Platform: "darwin"}, // macOS .local rename
		{MachineID: "editor-install-uuid", Hostname: "a3f91c2b7d04", Platform: "darwin"}, // container rebuild
		{MachineID: "editor-install-uuid", Hostname: "", Platform: "darwin"},             // hostname unavailable
		{MachineID: "editor-install-uuid", Hostname: "laptop", Platform: "win32"},        // process.platform vs runtime.GOOS drift
		{MachineID: "editor-install-uuid", Hostname: "laptop", Platform: ""},             // platform omitted by the caller
	}
	for _, m := range renames {
		if got := m.Hash(key); got != want {
			t.Errorf("hostname=%q platform=%q hashed to a NEW seat (%s, want %s) — one installation would consume several of its license's seats",
				m.Hostname, m.Platform, got, want)
		}
	}
}

// TestLicenseService_MachineLimit_FourthMachineRejected is the client half of
// the seat-limit story (#1334). A stub platform binds up to three distinct
// machine hashes to a pro key and answers MACHINE_LIMIT for a fourth: the test
// asserts each machine produces a DISTINCT hash (so the platform can count
// seats at all) and that the 4th machine's rejection surfaces as an invalid
// license rather than degrading to community.
//
// The real per-tier enforcement lives on the platform and cannot be exercised
// from this repository; what is verified here is that the daemon sends what
// that enforcement needs and honors its verdict.
func TestLicenseService_MachineLimit_FourthMachineRejected(t *testing.T) {
	const key = "PRO-KEY"
	var (
		mu    sync.Mutex
		bound = map[string]bool{}
	)
	seatCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(bound)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case "/v1/license/validate":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id, _ := body["machineId"].(string)
			mu.Lock()
			defer mu.Unlock()
			if id == "" {
				// Exactly the pre-#1334 wire shape: no seat can be counted.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":"machineId required"}}`))
				return
			}
			if !bound[id] && len(bound) >= 3 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"MACHINE_LIMIT","message":"machine limit reached"}}`))
				return
			}
			bound[id] = true
			jsonResponse(w, map[string]interface{}{
				"valid": true, "status": "active", "tier": "pro",
				"machineBound": true, "machineCount": len(bound),
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	machines := []MachineInfo{
		{MachineID: "install-1", Hostname: "host-1", Platform: "darwin"},
		{MachineID: "install-2", Hostname: "host-2", Platform: "linux"},
		{MachineID: "install-3", Hostname: "host-3", Platform: "win32"},
	}
	for i, m := range machines {
		svc := onlineLicenseService(t, srv.URL, key)
		info, err := svc.Validate(context.Background(), m)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Valid {
			t.Fatalf("machine %d: license rejected, want accepted within the 3-seat limit", i+1)
		}
		if info.MachineCount != i+1 {
			t.Errorf("machine %d: machineCount = %d, want %d — the seats did not hash distinctly",
				i+1, info.MachineCount, i+1)
		}
	}

	fourth := MachineInfo{MachineID: "install-4", Hostname: "host-4", Platform: "linux"}
	svc := onlineLicenseService(t, srv.URL, key)
	info, err := svc.Validate(context.Background(), fourth)
	if err != nil {
		t.Fatal(err)
	}
	if info.Valid {
		t.Error("4th machine on a 3-seat pro license was accepted, want MACHINE_LIMIT rejection")
	}
	if n := seatCount(); n != 3 {
		t.Errorf("platform bound %d machines, want 3", n)
	}
}
