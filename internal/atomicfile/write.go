// Package atomicfile provides the repository's durable atomic file install.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data using write-temp -> fsync(file) -> rename ->
// best-effort fsync(parent directory). The unique sibling temp is removed on
// every ordinary error path.
func Write(target string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.tmp")
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
