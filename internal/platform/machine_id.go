package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// machineIDEnv overrides the resolved machine identifier. Set it on cloud-hosted
// runners (or CI) so a machine's queue snapshot is scoped to a stable id even
// when the home directory is ephemeral.
const machineIDEnv = "NIGHTGAUGE_AGENT_ID"

// machineIDFileName is the per-machine identity, persisted under the
// machine-state root (layout.StateHome, ADR-024 § 8) — NOT the per-workspace
// .nightgauge/, because the id must be stable across every workspace the
// pipeline runs from on this machine.
const machineIDFileName = "machine-id"

// machineIDMode is the id file's mode: it identifies this device to the
// platform, so it is private to the user (ADR-024 § 17).
const machineIDMode fs.FileMode = 0o600

// MachineID returns a stable identifier for this machine, used as the
// queue-sync scope key and the platform agent id. Resolution order:
//
//  1. NIGHTGAUGE_AGENT_ID env override (cloud/CI), if non-empty.
//  2. A UUID persisted at <STATE>/machine-id, generated once and reused so
//     the same machine always replaces its own cloud snapshot.
//
// A pre-ADR-024 ~/.nightgauge/machine-id is COPIED into place byte for byte
// on first use and the legacy file is kept, mode 0600, as a compatibility copy
// (layout.CopyLegacyStateFile): an older binary still running on this machine
// reads it, and would otherwise mint a new id and bind a new seat against the
// account's machine limit (#1883). #2040's migrator removes the legacy copy.
// The new location is authoritative: when both exist and differ, the new
// file's id is used and a warning is logged. That is never an error, because
// an unavailable id silently stops queue sync and agent registration.
//
// An id is minted only when neither file exists; it is installed atomically,
// so two processes starting together agree on one id, and a compatibility
// copy is written to ~/.nightgauge when that directory already exists. An
// existing but empty or unreadable file is an error, never a reason to mint.
func MachineID() (string, error) {
	if v := strings.TrimSpace(os.Getenv(machineIDEnv)); v != "" {
		return v, nil
	}
	root, err := layout.StateHome()
	if err != nil {
		return "", fmt.Errorf("machine id: %w", err)
	}
	path := filepath.Join(root, machineIDFileName)
	diverged, err := layout.CopyLegacyStateFile(machineIDFileName, root)
	if err != nil {
		return "", fmt.Errorf("machine id: %w", err)
	}
	if diverged {
		log.Printf("warning: machine id: %s differs from the legacy %s; using %s (the authoritative copy). "+
			"An older nightgauge binary may have rewritten the legacy file",
			path, layout.LegacyStatePath(machineIDFileName), path)
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", fmt.Errorf("machine id: %s is empty; delete it to mint a new id (the platform will see a new device)", path)
		}
		// The id is private; a copy written by an older binary was 0644.
		if info, serr := os.Lstat(path); serr == nil && info.Mode().Perm() != machineIDMode {
			_ = os.Chmod(path, machineIDMode)
		}
		return id, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("machine id: read %s: %w", path, err)
	}
	minted := []byte(uuid.NewString() + "\n")
	got, won, err := layout.WriteStateFileExclusive(path, minted, machineIDMode)
	if err != nil {
		return "", fmt.Errorf("machine id: write %s: %w", path, err)
	}
	id := strings.TrimSpace(string(got))
	if id == "" {
		return "", fmt.Errorf("machine id: %s is empty", path)
	}
	if won {
		writeLegacyMachineIDCopy(got)
	}
	return id, nil
}

// writeLegacyMachineIDCopy writes the compatibility copy an older binary
// reads, only when ~/.nightgauge already exists (an older release ran here)
// and holds no machine-id. It never overwrites.
func writeLegacyMachineIDCopy(data []byte) {
	legacy := layout.LegacyStatePath(machineIDFileName)
	if legacy == "" {
		return
	}
	if info, err := os.Lstat(filepath.Dir(legacy)); err != nil || !info.IsDir() {
		return
	}
	_, _, _ = layout.WriteStateFileExclusive(legacy, data, machineIDMode)
}

// ResolveMachineID is MachineID for callers that treat the id as optional:
// on any error it returns "" ("machine id unavailable", so queue sync is
// skipped), never a substitute identity such as the hostname, which the
// platform would count as another device. Callers that can report the error
// use MachineID.
func ResolveMachineID() string {
	id, err := MachineID()
	if err != nil {
		return ""
	}
	return id
}
