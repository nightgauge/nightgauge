package contractrollout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// FileResult is what one file copy did.
type FileResult struct {
	// Path is the file's path in the target repository.
	Path string `json:"path"`
	// Changed is true when the target's bytes or executable bit differed
	// from the source's and were (or, planning, would be) replaced.
	Changed bool `json:"changed"`
	// SHA256 is the source's digest, which the target now has.
	SHA256 string `json:"sha256"`
}

// CopyFiles copies every file from srcRoot into dstRoot byte for byte,
// keeping the source's executable bit, and verifies each by reading it back.
// A file already identical is left untouched. With write false nothing is
// written and Changed says what writing would do.
func CopyFiles(srcRoot, dstRoot string, files []File, write bool) ([]FileResult, error) {
	out := make([]FileResult, 0, len(files))
	for _, f := range files {
		if err := checkRelPath(f.Path); err != nil {
			return out, err
		}
		if err := checkRelPath(f.TargetPath()); err != nil {
			return out, err
		}
		src := filepath.Join(srcRoot, filepath.FromSlash(f.Path))
		info, err := os.Stat(src)
		if err != nil {
			return out, fmt.Errorf("source %s: %w", f.Path, err)
		}
		if !info.Mode().IsRegular() {
			return out, fmt.Errorf("source %s is not a regular file", f.Path)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return out, fmt.Errorf("source %s: %w", f.Path, err)
		}
		sum := sha256.Sum256(data)
		res := FileResult{Path: f.TargetPath(), SHA256: hex.EncodeToString(sum[:])}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}

		dst := filepath.Join(dstRoot, filepath.FromSlash(f.TargetPath()))
		if err := refuseSymlinkPath(dstRoot, f.TargetPath()); err != nil {
			return out, err
		}
		cur, err := os.ReadFile(dst)
		switch {
		case err == nil:
			dinfo, statErr := os.Stat(dst)
			if statErr != nil {
				return out, statErr
			}
			res.Changed = !bytes.Equal(cur, data) || (dinfo.Mode()&0o111 != 0) != (mode&0o111 != 0)
		case os.IsNotExist(err):
			res.Changed = true
		default:
			return out, fmt.Errorf("target %s: %w", f.TargetPath(), err)
		}

		if write && res.Changed {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return out, err
			}
			if err := writeFileAtomic(dst, data, mode); err != nil {
				return out, fmt.Errorf("target %s: %w", f.TargetPath(), err)
			}
			back, err := os.ReadFile(dst)
			if err != nil || !bytes.Equal(back, data) {
				return out, fmt.Errorf("target %s does not read back byte-identical to its source", f.TargetPath())
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// refuseSymlinkPath refuses a target path any of whose existing components
// under root is a symlink, so a copy can never write outside the repository.
func refuseSymlinkPath(root, rel string) error {
	cur := root
	for _, part := range splitPath(rel) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target %s passes through a symlink (%s)", rel, cur)
		}
	}
	return nil
}

func splitPath(rel string) []string {
	return strings.Split(path.Clean(rel), "/")
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory and a rename, with mode applied.
func writeFileAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".contract-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, dst)
}
