package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
// A pre-ADR-024 ~/.nightgauge/machine-id is MOVED into place byte for byte on
// first use (layout.StateFile), never regenerated: a regenerated id is a new
// device to the platform and counts against the account's machine limit
// (#1883). So a move that cannot complete — both files present with different
// ids, or an I/O failure — is an error naming `nightgauge doctor --fix`, and no
// id is minted. A new id is minted only when neither file exists, and is
// installed atomically so two processes starting together agree on one id.
func MachineID() (string, error) {
	if v := strings.TrimSpace(os.Getenv(machineIDEnv)); v != "" {
		return v, nil
	}
	path, err := layout.StateFile(machineIDFileName)
	if err != nil {
		return "", fmt.Errorf("machine id: %w", err)
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if id := strings.TrimSpace(string(data)); id != "" {
			// A legacy file was written 0644; the id is private.
			if info, serr := os.Lstat(path); serr == nil && info.Mode().Perm() != machineIDMode {
				_ = os.Chmod(path, machineIDMode)
			}
			return id, nil
		}
		// An empty file (a torn write by an older binary) holds no identity to
		// preserve; replace it.
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return "", fmt.Errorf("machine id: remove empty %s: %w", path, rerr)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("machine id: read %s: %w", path, err)
	}
	got, _, err := layout.WriteStateFileExclusive(path, []byte(uuid.NewString()+"\n"), machineIDMode)
	if err != nil {
		return "", fmt.Errorf("machine id: write %s: %w", path, err)
	}
	id := strings.TrimSpace(string(got))
	if id == "" {
		return "", fmt.Errorf("machine id: %s is empty", path)
	}
	return id, nil
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
