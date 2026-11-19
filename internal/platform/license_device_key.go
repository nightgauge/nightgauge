package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// DeviceKeyResult is a license key the platform issued for this device
// (#1883, platform ADR 015). LicenseKey is returned once and never again: the
// caller must store it before doing anything else. camelCase json tags drive
// the generated TS client field names.
type DeviceKeyResult struct {
	LicenseKey  string     `json:"licenseKey"`
	DeviceKeyID string     `json:"deviceKeyId"`
	Tier        string     `json:"tier"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

// DeviceKeyErrorCode classifies a RequestDeviceKey failure so the extension
// can show one message per state instead of a bare "not accepted".
type DeviceKeyErrorCode string

const (
	// DeviceKeyNotFound — 404 LICENSE_NOT_FOUND: the account has no license.
	DeviceKeyNotFound DeviceKeyErrorCode = "LICENSE_NOT_FOUND"
	// DeviceKeyExpired — 402 LICENSE_EXPIRED: the account's license expired.
	DeviceKeyExpired DeviceKeyErrorCode = "LICENSE_EXPIRED"
	// DeviceKeyRevoked — 403 LICENSE_REVOKED: revoked or suspended.
	DeviceKeyRevoked DeviceKeyErrorCode = "LICENSE_REVOKED"
	// DeviceKeyMachineLimit — 403 LICENSE_MACHINE_LIMIT: the account's
	// machine limit is reached.
	DeviceKeyMachineLimit DeviceKeyErrorCode = "LICENSE_MACHINE_LIMIT"
	// DeviceKeyRateLimited — 429: ten requests per minute per account.
	DeviceKeyRateLimited DeviceKeyErrorCode = "RATE_LIMITED"
	// DeviceKeyUnauthorized — no token, or the platform answered 401.
	DeviceKeyUnauthorized DeviceKeyErrorCode = "UNAUTHORIZED"
	// DeviceKeyUnavailable — transport failure, a 404 with no license code
	// (the endpoint is not deployed), or any other unexpected answer.
	DeviceKeyUnavailable DeviceKeyErrorCode = "UNAVAILABLE"
)

// DeviceKeyError is a typed RequestDeviceKey failure. Error() leads with the
// code so it survives the IPC layer, which carries only the message.
type DeviceKeyError struct {
	Code    DeviceKeyErrorCode
	Message string
	// err is the underlying cause, kept for errors.Is (ErrCredentialInsufficient).
	err error
}

func (e *DeviceKeyError) Error() string { return string(e.Code) + ": " + e.Message }

// Unwrap exposes the cause, so a local credential refusal stays detectable.
func (e *DeviceKeyError) Unwrap() error { return e.err }

// deviceKeyMachineIDKey domain-separates the device-key machine id from every
// other digest of the raw machine id.
const deviceKeyMachineIDKey = "nightgauge/device-key/v1"

// DeviceKeyMachineID is the machineId sent to POST /v1/license/device-key: an
// HMAC-SHA256 of the raw machine id under a fixed domain key, hex-encoded.
//
// It cannot be MachineInfo.Hash, which validate sends: that digest is keyed on
// the license key, and this call is made precisely because there is none. It
// must be stable, because the platform revokes a machine's previous device key
// when the same machineId asks again; a digest keyed on the issued key would
// change every time. The raw id still never leaves the process.
func DeviceKeyMachineID(rawMachineID string) string {
	if rawMachineID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(deviceKeyMachineIDKey))
	mac.Write([]byte(rawMachineID))
	return hex.EncodeToString(mac.Sum(nil))
}

// deviceKeyRequestBody is exactly the platform's schema: unknown keys are a 422.
type deviceKeyRequestBody struct {
	MachineID string `json:"machineId"`
	Hostname  string `json:"hostname,omitempty"`
	Platform  string `json:"platform,omitempty"`
}

type deviceKeyResponseBody struct {
	LicenseKey  string  `json:"license_key"`
	DeviceKeyID string  `json:"device_key_id"`
	Tier        string  `json:"tier"`
	ExpiresAt   *string `json:"expires_at"`
}

// deviceKeyResponseBodyLimit caps the device-key response read: the 201 body
// and the error envelope are both a few hundred bytes.
const deviceKeyResponseBodyLimit = 8 << 10

// issuedKeyMaxLen bounds an issued key; real keys are well under it.
const issuedKeyMaxLen = 256

// wellFormedIssuedKey reports whether key has the shape of a license key the
// platform issues: a known prefix, no whitespace or control characters, and a
// sane length.
func wellFormedIssuedKey(key string) bool {
	if key == "" || len(key) > issuedKeyMaxLen || credentialKindOf(key) != credentialLicenseKey {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// platform hostname and platform limits (platform DeviceKeyBodySchema).
const (
	deviceKeyHostnameMax = 255
	deviceKeyPlatformMax = 64
)

// RequestDeviceKey asks the platform to issue a license key for this device
// under the signed-in account's license (#1883). accessToken is the user's
// session JWT, applied to this one request only.
//
// Calling it again for the same machine revokes the machine's previous device
// key, so callers make it only when no working key resolves.
func (s *LicenseService) RequestDeviceKey(ctx context.Context, accessToken string, machine MachineInfo) (*DeviceKeyResult, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, &DeviceKeyError{Code: DeviceKeyUnauthorized, Message: "sign in required to link this machine"}
	}
	machine = s.rememberMachine(machine).Resolve()
	body := deviceKeyRequestBody{
		MachineID: DeviceKeyMachineID(machine.MachineID),
		Hostname:  truncate(machine.Hostname, deviceKeyHostnameMax),
		Platform:  truncate(machine.Platform, deviceKeyPlatformMax),
	}
	if body.MachineID == "" {
		return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: "no machine id could be resolved for this host"}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: fmt.Sprintf("marshal: %v", err), err: err}
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:     api.OpLicenseIssueDeviceKey,
		Body:   data,
		Bearer: accessToken,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
	})
	if err != nil {
		code := DeviceKeyUnavailable
		if errors.Is(err, ErrCredentialInsufficient) {
			code = DeviceKeyUnauthorized
		}
		return nil, &DeviceKeyError{Code: code, Message: err.Error(), err: err}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: fmt.Sprintf("device-key request failed: %v", err), err: err}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, deviceKeyResponseBodyLimit))
	if err != nil {
		return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: fmt.Sprintf("read response: %v", err), err: err}
	}

	if resp.StatusCode == http.StatusCreated {
		var parsed deviceKeyResponseBody
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: fmt.Sprintf("parse response: %v", err), err: err}
		}
		if !wellFormedIssuedKey(parsed.LicenseKey) {
			// Never hand the extension a value to store as this machine's
			// credential unless it has the shape of a key the platform issues.
			return nil, &DeviceKeyError{Code: DeviceKeyUnavailable, Message: "response carried no well-formed license key"}
		}
		result := &DeviceKeyResult{
			LicenseKey:  parsed.LicenseKey,
			DeviceKeyID: parsed.DeviceKeyID,
			Tier:        parsed.Tier,
		}
		if parsed.ExpiresAt != nil && *parsed.ExpiresAt != "" {
			// The key is shown once, so an unreadable expiry must not lose it;
			// say so instead of reporting a key that never expires.
			t, err := time.Parse(time.RFC3339, *parsed.ExpiresAt)
			if err != nil {
				log.Printf("platform: device key expires_at %q is not RFC 3339; expiry unknown", *parsed.ExpiresAt)
			} else {
				result.ExpiresAt = &t
			}
		}
		return result, nil
	}
	return nil, deviceKeyErrorFor(resp.StatusCode, respBody)
}

// deviceKeyErrorFor maps a non-201 answer to a typed error. The platform's
// error code decides when it names a license state; the status decides
// otherwise. A 404 without LICENSE_NOT_FOUND is a missing route, not a
// missing license, and must not tell the user their account has none.
func deviceKeyErrorFor(status int, body []byte) *DeviceKeyError {
	var parsed licenseErrorBody
	_ = json.Unmarshal(body, &parsed)
	switch DeviceKeyErrorCode(parsed.Error.Code) {
	case DeviceKeyNotFound:
		return &DeviceKeyError{Code: DeviceKeyNotFound, Message: "this account has no license"}
	case DeviceKeyExpired:
		return &DeviceKeyError{Code: DeviceKeyExpired, Message: "the account's license has expired"}
	case DeviceKeyRevoked:
		return &DeviceKeyError{Code: DeviceKeyRevoked, Message: "the account's license is revoked or suspended"}
	case DeviceKeyMachineLimit:
		return &DeviceKeyError{Code: DeviceKeyMachineLimit, Message: "the account's machine limit is reached"}
	}
	switch status {
	case http.StatusUnauthorized:
		return &DeviceKeyError{Code: DeviceKeyUnauthorized, Message: "your session has expired — sign in again"}
	case http.StatusTooManyRequests:
		return &DeviceKeyError{Code: DeviceKeyRateLimited, Message: "too many device-key requests — try again in a minute"}
	default:
		return &DeviceKeyError{Code: DeviceKeyUnavailable, Message: fmt.Sprintf("device-key request returned HTTP %d", status)}
	}
}
