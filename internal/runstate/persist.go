package runstate

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
)

// Load reads run-state.json from the given base dir. Returns (nil, nil) when
// the file does not exist — callers treat that as the "fresh" case.
//
// Returns a typed error for major-version skew (the caller surfaces this as
// SchemaVersionMismatch on the SDK side).
func Load(baseDir string) (*RunState, error) {
	path := Path(baseDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read run-state: %w", err)
	}
	var rs RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse run-state: %w", err)
	}
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	return &rs, nil
}

// Save persists rs to baseDir/run-state.json using the atomic+fsync write
// contract: write-temp → fsync(file) → rename → fsync(parent dir).
//
// The full path is created with 0755; the file is written 0644.
func Save(baseDir string, rs *RunState) error {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}
	if rs.SchemaVersion == "" {
		rs.SchemaVersion = SchemaVersion
	}
	data, err := jsonMarshalIndent(rs)
	if err != nil {
		return fmt.Errorf("marshal run-state: %w", err)
	}
	// Trailing newline is friendly to tools like `jq .` and `git diff`.
	data = append(data, '\n')
	return AtomicWriteFile(Path(baseDir), data, 0644)
}

// AtomicWriteFile writes data to target using write-temp → fsync(file) →
// rename → fsync(parent dir). Directory fsync is best-effort: macOS treats
// it as a no-op and certain filesystems / Windows disallow opening a
// directory as a file. Those cases are not failures.
//
// The temp file's name is unique per write (os.CreateTemp), not a fixed
// `target + ".tmp"`. A fixed name is a collision the moment two writers touch
// one target — and this primitive now backs a record that two processes
// genuinely race for, the serve daemon's machine-global claim (#388): both
// would open the SAME temp path, interleave their bytes into it, and each
// rename it into place, so the atomicity this function's whole contract rests
// on would be gone exactly when it is needed. Residue is still removed on
// every error path; only a hard kill mid-write can leave one behind, and a
// `*.tmp` name matches no reader in the tree.
//
// This runstate-facing wrapper delegates to the neutral atomicfile package so
// model and run-state writers share the same unique-temp durability contract.
// Other packages may retain specialized writers when their recovery protocol
// depends on a stable temp pathname.
func AtomicWriteFile(target string, data []byte, perm os.FileMode) error {
	return atomicfile.Write(target, data, perm)
}
