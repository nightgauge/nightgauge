package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OpenCodeEndpointSlots is the scheduler's published view of its endpoint
// slot ledger (#1679): how many dispatches each declared endpoint is serving
// right now. The scheduler owns the ledger; this file only lets another
// process, `nightgauge doctor --adapters`, show slots in use. It holds
// endpoint ids and counts, never a base_url.
type OpenCodeEndpointSlots struct {
	// PID is the scheduler process that wrote the file. A reader treats the
	// file as stale when that process is gone.
	PID int `json:"pid"`
	// UpdatedAt is when the ledger last changed.
	UpdatedAt time.Time `json:"updated_at"`
	// InUse is the slots in use per endpoint id.
	InUse map[string]int `json:"in_use"`
	// Waiting is how many stages are queued for a slot per endpoint id of
	// the model they asked for.
	Waiting map[string]int `json:"waiting,omitempty"`
}

// OpenCodeEndpointSlotsPath is where the scheduler publishes its endpoint
// slot ledger: STATE/opencode/endpoint-slots.json under the machine-state
// root state (layout.StateHome).
func OpenCodeEndpointSlotsPath(state string) string {
	return filepath.Join(OpenCodeStateDir(state), "endpoint-slots.json")
}

// WriteOpenCodeEndpointSlots publishes slots at path, atomically: a temporary
// file of mode 0600 in the same directory, renamed into place. The directory
// is created 0700 and is refused when it is a symbolic link, so the ledger is
// never written through a link.
func WriteOpenCodeEndpointSlots(path string, slots OpenCodeEndpointSlots) error {
	data, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("opencode endpoint slots: %s is not a directory (a symbolic link is refused)", dir)
	}
	tmp, err := os.CreateTemp(dir, ".endpoint-slots-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Chmod(0o600), tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// ReadOpenCodeEndpointSlots reads the published ledger at path.
func ReadOpenCodeEndpointSlots(path string) (OpenCodeEndpointSlots, error) {
	var slots OpenCodeEndpointSlots
	data, err := os.ReadFile(path)
	if err != nil {
		return slots, err
	}
	err = json.Unmarshal(data, &slots)
	return slots, err
}
