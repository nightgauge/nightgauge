package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"

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
	// MachineID is the raw per-installation fingerprint and the ONLY field the
	// seat identity is derived from. It is never sent to the platform as-is —
	// Hash() derives the wire value from it.
	MachineID string
	// Hostname and Platform are binding CONTEXT, not identity: the platform
	// stores them in the clear so an account owner can recognise which of
	// their seats is which. They are deliberately outside the hashed identity
	// (see Hash) — a machine that renames itself must keep its seat.
	Hostname string
	Platform string
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

// Hash is the value the platform stores as the machine identity:
// HMAC-SHA256(licenseKey, machineID), hex-encoded — the extension contract's
// machineHash. Keying with the license key means the same machine hashes
// differently under different licenses, so an identifier leaked from one
// account cannot be correlated with another, and the raw machine id never
// leaves this process.
//
// The digest covers the machine id and NOTHING else. That is the whole point
// of the seat identity: it must be exactly as stable as the installation it
// names. MachineID is stable by construction — vscode.env.machineId is a UUID
// that survives restarts and updates, and the daemon's own fallback is a UUID
// persisted under the home directory. Hostname is not: macOS appends and
// rewrites .local names when the network changes, every devcontainer or
// Codespaces rebuild mints a fresh random one, and corporate re-imaging
// renames en masse. Folding a volatile value into the identity would re-bind
// one installation as a new machine on each change, so a pro license would
// burn its three seats on one laptop and then lock its owner out. Platform is
// stable per install but adds nothing to a UUID's uniqueness while adding a
// second way to drift (process.platform says "win32" where runtime.GOOS says
// "windows"), so it stays out too. Both travel as cleartext context instead —
// see applyTo.
//
// Because this digest is the primary key of a license_machines row, its
// derivation is a wire contract: changing it re-binds every already-bound
// machine, each installation takes a fresh seat, and a full license rejects
// its own owner as MACHINE_LIMIT. TestMachineInfo_Hash_PinsTheWireDigest
// pins the exact bytes so that can never happen silently.
//
// Returns "" when there is nothing to bind — no license key, or no machine id
// — so callers omit the field rather than sending a hash of emptiness that
// every unidentifiable machine would share.
func (m MachineInfo) Hash(licenseKey string) string {
	if licenseKey == "" || m.MachineID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(licenseKey))
	mac.Write([]byte(m.MachineID))
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
