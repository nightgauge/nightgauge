// Package history provides shared append-only JSONL writer primitives used by
// the pipeline run-record writer (internal/state) and the knowledge telemetry
// emitter (internal/knowledge/telemetry). Centralizing the append path keeps
// every JSONL stream byte-equivalent in on-disk format and prevents drift
// between subsystems that need atomic single-line appends.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// mu serializes appends inside a single process. Cross-process atomicity is
// provided by the kernel's O_APPEND semantics on POSIX; the mutex is required
// only to keep concurrent goroutines inside one binary from interleaving
// partial line writes when telemetry and run-record paths fire together.
var mu sync.Mutex

// AppendJSONL appends a single JSON-encoded record followed by '\n' to the
// file at path. The parent directory is created with 0755 permissions if it
// does not already exist. Each call opens, writes, and closes the file so the
// append is atomic per call on POSIX filesystems.
//
// Returns wrapped errors for missing-dir creation, marshal failures, file
// open failures, and short writes. Callers that must not fail user-facing
// operations (e.g., the telemetry emitter) are expected to log and swallow
// the error themselves.
func AppendJSONL(path string, record any) error {
	if path == "" {
		return fmt.Errorf("history: AppendJSONL requires a non-empty path")
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("history: marshal record: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("history: create parent dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("history: open file %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("history: write entry: %w", err)
	}
	return nil
}
