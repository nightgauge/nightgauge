package stages

import (
	"os"
	"path/filepath"
)

// osReadFile is the production file reader. Indirected through `readFile` in
// prmerge.go so tests can swap to a fake without touching the filesystem.
func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// osMkdirAll wraps os.MkdirAll. Indirected through `mkdirAll` in prcreate.go.
func osMkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// osWriteFileAtomic writes data to target via temp+rename so a partial write
// is never visible to readers. Indirected through `writeFileAtomic` in
// prcreate.go.
func osWriteFileAtomic(target string, data []byte) error {
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
