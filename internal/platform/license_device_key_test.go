package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const deviceKeyTestJWT = "header.payload.signature"

type deviceKeyCapture struct {
	method, path, auth string
	body               map[string]any
}

func newDeviceKeyServer(t *testing.T, status int, respBody string) (*LicenseService, *deviceKeyCapture) {
	t.Helper()
	got := &deviceKeyCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)
	return NewLicenseService(c), got
}

func TestRequestDeviceKey_IssuesKeyWithPerCallJWT(t *testing.T) {
	svc, got := newDeviceKeyServer(t, http.StatusCreated,
		`{"license_key":"ng_live_issued","device_key_id":"dk-1","tier":"pro","expires_at":"2027-01-02T03:04:05Z"}`)

	res, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT,
		MachineInfo{MachineID: "raw-machine", Hostname: "mac", Platform: "darwin"})
	if err != nil {
		t.Fatalf("RequestDeviceKey: %v", err)
	}
	if res.LicenseKey != "ng_live_issued" || res.DeviceKeyID != "dk-1" || res.Tier != "pro" {
		t.Fatalf("result = %+v", res)
	}
	if res.ExpiresAt == nil || res.ExpiresAt.Year() != 2027 {
		t.Fatalf("expiresAt = %v", res.ExpiresAt)
	}
	if got.method != http.MethodPost || got.path != "/v1/license/device-key" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+deviceKeyTestJWT {
		t.Fatalf("Authorization = %q, want the per-call JWT", got.auth)
	}
	// Exactly the platform schema: unknown keys are a 422.
	if len(got.body) != 3 {
		t.Fatalf("body keys = %v, want machineId/hostname/platform only", got.body)
	}
	if got.body["machineId"] != DeviceKeyMachineID("raw-machine") {
		t.Fatalf("machineId = %v, want the domain-keyed digest", got.body["machineId"])
	}
	if strings.Contains(got.body["machineId"].(string), "raw-machine") {
		t.Fatal("the raw machine id left the process")
	}
	if got.body["hostname"] != "mac" || got.body["platform"] != "darwin" {
		t.Fatalf("body = %v", got.body)
	}
}

func TestRequestDeviceKey_NullExpiry(t *testing.T) {
	svc, _ := newDeviceKeyServer(t, http.StatusCreated,
		`{"license_key":"ng_live_x","device_key_id":"dk","tier":"team","expires_at":null}`)
	res, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT, MachineInfo{MachineID: "m"})
	if err != nil || res.ExpiresAt != nil {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestRequestDeviceKey_ErrorCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   DeviceKeyErrorCode
	}{
		{"no license", 404, `{"error":{"code":"LICENSE_NOT_FOUND"}}`, DeviceKeyNotFound},
		{"expired", 402, `{"error":{"code":"LICENSE_EXPIRED"}}`, DeviceKeyExpired},
		{"revoked", 403, `{"error":{"code":"LICENSE_REVOKED"}}`, DeviceKeyRevoked},
		{"machine limit", 403, `{"error":{"code":"LICENSE_MACHINE_LIMIT"}}`, DeviceKeyMachineLimit},
		{"rate limited", 429, `{"error":{"code":"RATE_LIMITED"}}`, DeviceKeyRateLimited},
		{"stale jwt", 401, `{"error":{"code":"UNAUTHORIZED"}}`, DeviceKeyUnauthorized},
		// A 404 that is not LICENSE_NOT_FOUND is a missing route, never "no license".
		{"route not deployed", 404, `not found`, DeviceKeyUnavailable},
		{"server error", 500, `{}`, DeviceKeyUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newDeviceKeyServer(t, tc.status, tc.body)
			_, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT, MachineInfo{MachineID: "m"})
			var dkErr *DeviceKeyError
			if !errors.As(err, &dkErr) {
				t.Fatalf("err = %v, want *DeviceKeyError", err)
			}
			if dkErr.Code != tc.want {
				t.Fatalf("code = %s, want %s", dkErr.Code, tc.want)
			}
			// The IPC layer carries only the message, so it must lead with the code.
			if !strings.HasPrefix(err.Error(), string(tc.want)+":") {
				t.Fatalf("message %q does not lead with the code", err.Error())
			}
		})
	}
}

func TestRequestDeviceKey_RefusesWithoutTokenBeforeTheWire(t *testing.T) {
	svc, got := newDeviceKeyServer(t, http.StatusCreated, `{}`)
	_, err := svc.RequestDeviceKey(context.Background(), "  ", MachineInfo{MachineID: "m"})
	var dkErr *DeviceKeyError
	if !errors.As(err, &dkErr) || dkErr.Code != DeviceKeyUnauthorized {
		t.Fatalf("err = %v, want UNAUTHORIZED", err)
	}
	if got.method != "" {
		t.Fatal("a request without a token reached the platform")
	}
}

func TestRequestDeviceKey_RefusesALicenseKeyAsBearer(t *testing.T) {
	svc, got := newDeviceKeyServer(t, http.StatusCreated, `{}`)
	_, err := svc.RequestDeviceKey(context.Background(), "ng_live_notajwt", MachineInfo{MachineID: "m"})
	if !errors.Is(err, ErrCredentialInsufficient) {
		t.Fatalf("err = %v, want ErrCredentialInsufficient", err)
	}
	var dkErr *DeviceKeyError
	if !errors.As(err, &dkErr) || dkErr.Code != DeviceKeyUnauthorized ||
		!strings.HasPrefix(err.Error(), "UNAUTHORIZED:") {
		t.Fatalf("err = %v, want an UNAUTHORIZED DeviceKeyError", err)
	}
	if got.method != "" {
		t.Fatal("a license key was presented to a JWT-only route")
	}
}

func TestDeviceKeyMachineID_StableAndDistinctFromValidateHash(t *testing.T) {
	a, b := DeviceKeyMachineID("raw"), DeviceKeyMachineID("raw")
	if a == "" || a != b {
		t.Fatalf("digest not stable: %q vs %q", a, b)
	}
	if DeviceKeyMachineID("other") == a {
		t.Fatal("different machines share a digest")
	}
	if DeviceKeyMachineID("") != "" {
		t.Fatal("empty machine id must not yield a digest every unidentifiable machine shares")
	}
	// Pin the wire bytes: the platform revokes the previous key of the same
	// machineId, so a changed derivation would strand every issued key.
	const want = "91bc0b400102005dd1285e7ae0f07cf538db1df422867ee1d9b7a636f98fd1e0"
	if got := DeviceKeyMachineID("machine-1"); got != want {
		t.Fatalf("DeviceKeyMachineID(machine-1) = %s, want %s", got, want)
	}
}

// A 201 the extension would store as this machine's credential must carry a
// well-formed key; anything else is UNAVAILABLE and never a result.
func TestRequestDeviceKey_RejectsAMalformedIssuedKey(t *testing.T) {
	for name, body := range map[string]string{
		"empty key":      `{"license_key":"","device_key_id":"dk","tier":"pro"}`,
		"unknown prefix": `{"license_key":"sk_live_x","device_key_id":"dk","tier":"pro"}`,
		"whitespace":     `{"license_key":"ng_live_a b","device_key_id":"dk","tier":"pro"}`,
		"not json":       `{`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newDeviceKeyServer(t, http.StatusCreated, body)
			res, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT, MachineInfo{MachineID: "m"})
			var dkErr *DeviceKeyError
			if res != nil || !errors.As(err, &dkErr) || dkErr.Code != DeviceKeyUnavailable {
				t.Fatalf("res = %+v err = %v, want UNAVAILABLE and no result", res, err)
			}
		})
	}
}

func TestRequestDeviceKey_UnreadableExpiryKeepsTheKey(t *testing.T) {
	svc, _ := newDeviceKeyServer(t, http.StatusCreated,
		`{"license_key":"ng_live_x","device_key_id":"dk","tier":"pro","expires_at":"next tuesday"}`)
	res, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT, MachineInfo{MachineID: "m"})
	if err != nil || res.LicenseKey != "ng_live_x" || res.ExpiresAt != nil {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestRequestDeviceKey_TruncatesContextToThePlatformLimits(t *testing.T) {
	svc, got := newDeviceKeyServer(t, http.StatusCreated,
		`{"license_key":"ng_live_x","device_key_id":"dk","tier":"pro"}`)
	_, err := svc.RequestDeviceKey(context.Background(), deviceKeyTestJWT, MachineInfo{
		MachineID: "m", Hostname: strings.Repeat("h", 300), Platform: strings.Repeat("p", 80),
	})
	if err != nil {
		t.Fatalf("RequestDeviceKey: %v", err)
	}
	if len(got.body["hostname"].(string)) != 255 || len(got.body["platform"].(string)) != 64 {
		t.Fatalf("hostname/platform not truncated: %d/%d",
			len(got.body["hostname"].(string)), len(got.body["platform"].(string)))
	}
}

func TestAdoptLicenseKey(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.AdoptLicenseKey("   ") || c.LicenseKey() != "" {
		t.Fatal("a whitespace-only key was adopted")
	}
	if !c.AdoptLicenseKey("  ng_live_a  ") || c.LicenseKey() != "ng_live_a" {
		t.Fatalf("key = %q, want the trimmed key adopted", c.LicenseKey())
	}
	if c.AdoptLicenseKey("ng_live_b") || c.LicenseKey() != "ng_live_a" {
		t.Fatal("a held key was replaced")
	}
	if c.ReplaceLicenseKey("ng_live_a") || c.ReplaceLicenseKey("  ") {
		t.Fatal("an unchanged or empty key reported a replacement")
	}
	if !c.ReplaceLicenseKey("ng_live_b") || c.LicenseKey() != "ng_live_b" {
		t.Fatalf("key = %q, want the replacement", c.LicenseKey())
	}
}
