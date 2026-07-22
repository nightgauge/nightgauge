package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
// This is the canonical durability primitive for everything under
// .nightgauge/pipeline/. Other packages (state.AtomicWriteFile) call into
// this implementation via the same shape — kept duplicated only because we
// don't want every package to import internal/runstate just for I/O.
func AtomicWriteFile(target string, data []byte, perm os.FileMode) error {
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
