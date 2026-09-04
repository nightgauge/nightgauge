package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"strings"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// MachineInfo identifies the machine a license is being validated from, so the
// platform can bind a seat to it and enforce the per-tier machine limit
// (community 1 / pro 3 / team+enterprise unlimited).
//
// The three fields mirror, in order, the arguments the extension already sends
// over IPC on every pipeline preflight (packages/nightgauge-vscode/src/platform/
// LicensePreflight.ts: MachineFingerprint.getMachineId(), os.hostname(),
// process.platform). Before #1334 the IPC layer unmarshalled them and dropped
// them on the floor: every validate request reached the platform carrying only
// the key, so no license_machines row was ever written and the seat limits were
// structurally unenforceable.
type MachineInfo struct {
	// MachineID is the raw per-installation fingerprint. It is NEVER sent to the
	// platform as-is — Hash() derives the wire value from it.
	MachineID string
	Hostname  string
	Platform  string
}

// Resolve fills any field the caller left empty from this host, so a validate
// call that arrives without machine context (the params-less "platform.license"
// IPC method, or a headless CLI daemon) still binds a seat instead of silently
// binding none. A caller-supplied value always wins: it is the identity of the
// editor installation that owns the seat, and the host-derived machine id
// cannot reproduce it.
func (m MachineInfo) Resolve() MachineInfo {
	if m.MachineID == "" {
		m.MachineID = ResolveMachineID()
	}
	if m.Hostname == "" {
		if host, err := os.Hostname(); err == nil {
			m.Hostname = host
		}
	}
	if m.Platform == "" {
		m.Platform = runtime.GOOS
	}
	return m
}

// IsZero reports whether the caller supplied no machine context at all.
func (m MachineInfo) IsZero() bool {
	return m.MachineID == "" && m.Hostname == "" && m.Platform == ""
}

// Fingerprint is the stable per-machine string the wire hash is computed over.
// The separator and field order are part of the contract: two validate calls
// from the same installation must produce the same fingerprint, or one machine
// consumes two seats.
func (m MachineInfo) Fingerprint() string {
	return strings.Join([]string{m.MachineID, m.Hostname, m.Platform}, "|")
}

// Hash is the value the platform stores as the machine identity:
// HMAC-SHA256(licenseKey, fingerprint), hex-encoded — the extension contract's
// machineHash. Keying with the license key means the same machine hashes
// differently under different licenses, so a fingerprint leaked from one
// account cannot be correlated with another, and the raw fingerprint (which
// includes the hostname) never leaves this process.
//
// Returns "" when there is nothing to bind — no license key, or no fingerprint
// material at all — so callers omit the field rather than sending a hash of
// emptiness that every unidentifiable machine would share.
func (m MachineInfo) Hash(licenseKey string) string {
	fingerprint := m.Fingerprint()
	if licenseKey == "" || strings.Trim(fingerprint, "|") == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(licenseKey))
	mac.Write([]byte(fingerprint))
	return hex.EncodeToString(mac.Sum(nil))
}

// applyTo populates the machine-binding fields of a validate request body.
// hostname and platform go over the wire in the clear (the platform shows them
// as binding context in the account UI); the machine id goes over as the hash.
func (m MachineInfo) applyTo(body *api.LicenseValidateJSONRequestBody) {
	resolved := m.Resolve()
	if hash := resolved.Hash(body.Key); hash != "" {
		body.MachineId = &hash
	}
	if resolved.Hostname != "" {
		body.Hostname = &resolved.Hostname
	}
	if resolved.Platform != "" {
		body.Platform = &resolved.Platform
	}
}
