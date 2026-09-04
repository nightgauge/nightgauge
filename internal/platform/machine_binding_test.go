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

func wantHash(key string, m MachineInfo) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(m.Fingerprint()))
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
	if id, _ := got[0]["machineId"].(string); id != wantHash("SESSION-KEY", machine) {
		t.Errorf("machineId = %q, want HMAC-SHA256(licenseKey, fingerprint) = %q", id, wantHash("SESSION-KEY", machine))
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
	if id, _ := got[0]["machineId"].(string); id != wantHash("ENTERED-KEY", machine) {
		t.Errorf("machineId = %q, want the hash keyed by the entered key %q", id, wantHash("ENTERED-KEY", machine))
	}
	if id, _ := got[0]["machineId"].(string); id == wantHash("SESSION-KEY", machine) {
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
	if id, _ := got[1]["machineId"].(string); id != wantHash("SESSION-KEY", machine) {
		t.Errorf("second machineId = %q, want the remembered fingerprint hashed under the session key %q",
			id, wantHash("SESSION-KEY", machine))
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
	if h == m.Fingerprint() || h == m.MachineID {
		t.Error("Hash leaked the raw fingerprint onto the wire")
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
		t.Errorf("Hash with no fingerprint material = %q, want empty", got)
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
