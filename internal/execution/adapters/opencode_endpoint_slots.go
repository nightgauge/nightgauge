package adapters

import (
	"encoding/json"
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
// slot ledger for home.
func OpenCodeEndpointSlotsPath(home string) string {
	return filepath.Join(home, ".nightgauge", "opencode", "endpoint-slots.json")
}

// WriteOpenCodeEndpointSlots publishes slots at path, atomically.
func WriteOpenCodeEndpointSlots(path string, slots OpenCodeEndpointSlots) error {
	data, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
