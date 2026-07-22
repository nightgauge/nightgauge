package platform

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// machineIDEnv overrides the resolved machine identifier. Set it on cloud-hosted
// runners (or CI) so a machine's queue snapshot is scoped to a stable id even
// when the home directory is ephemeral.
const machineIDEnv = "NIGHTGAUGE_AGENT_ID"

// machineIDFile is the per-machine identity persisted under the user's home
// config dir (NOT the per-workspace .nightgauge/ — the id must be stable
// across every workspace the pipeline runs from on this machine).
var machineIDFile = filepath.Join(".nightgauge", "machine-id")

// ResolveMachineID returns a stable identifier for this machine, used as the
// queue-sync scope key and the platform agent id. Resolution order:
//
//  1. NIGHTGAUGE_AGENT_ID env override (cloud/CI), if non-empty.
//  2. A UUID persisted at ~/.nightgauge/machine-id — generated once and
//     reused so the same machine always replaces its own cloud snapshot.
//  3. The hostname, as a last-resort fallback when the home dir is unwritable.
//
// Never returns an error: callers treat the worst case (empty string) as
// "machine id unavailable" and skip queue sync. In practice (2) or (3) always
// yields a value.
func ResolveMachineID() string {
	if v := strings.TrimSpace(os.Getenv(machineIDEnv)); v != "" {
		return v
	}

	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		path := filepath.Join(home, machineIDFile)
		if data, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(data)); id != "" {
				return id
			}
		}
		// No usable id yet — mint one and persist it best-effort.
		id := uuid.NewString()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err == nil {
				return id
			}
		}
		// Persist failed (read-only home) — fall through to hostname so the id
		// is at least stable for the life of the process.
	}

	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return ""
}
