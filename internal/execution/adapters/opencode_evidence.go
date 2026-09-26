package adapters

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// OpenCodeEvidenceDir is where a failed run's OpenCode evidence is kept
// (#2171), beside the runs directory and never inside any repository.
func OpenCodeEvidenceDir(home string) string {
	return filepath.Join(home, ".nightgauge", "opencode", "evidence")
}

// PreserveOpenCodeRunEvidence copies a failed run's OpenCode session database
// (data/opencode/opencode.db*) and log directory (data/opencode/log/) out of
// its per-run root into OpenCodeEvidenceDir/<id>, before the root is deleted
// (#2171). On failure they are the only record of what the model did.
//
// Nothing else from the root is kept: not home/, not config/, not auth.json
// or any other credential. Only regular files are copied and no symbolic link
// is followed. Directories are 0700 and files 0600. Evidence older than
// OpenCodeOrphanMaxAge is swept on every call, the run roots' own bound.
//
// Returns the evidence directory, or "" when the root held nothing to keep
// (including a root that does not exist).
func PreserveOpenCodeRunEvidence(home, id string, now time.Time) (string, error) {
	root, err := OpenCodeRunRoot(home, id)
	if err != nil {
		return "", err
	}
	// Sweep first, so a failure below still bounds the directory.
	_, sweepErr := SweepOpenCodeRunEvidence(home, OpenCodeOrphanMaxAge, now)

	src := filepath.Join(root, "data", "opencode")
	var files []string // relative to src
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return "", sweepErr
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("opencode run evidence: %w", err), sweepErr)
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "opencode.db") {
			files = append(files, e.Name())
		}
	}
	logDir := filepath.Join(src, "log")
	if fi, lerr := os.Lstat(logDir); lerr == nil && fi.IsDir() {
		walkErr := filepath.WalkDir(logDir, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.Type().IsRegular() {
				rel, rerr := filepath.Rel(src, path)
				if rerr != nil {
					return rerr
				}
				files = append(files, rel)
			}
			return nil
		})
		if walkErr != nil {
			return "", errors.Join(fmt.Errorf("opencode run evidence: %w", walkErr), sweepErr)
		}
	}
	if len(files) == 0 {
		return "", sweepErr
	}

	dst := filepath.Join(OpenCodeEvidenceDir(home), id)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", fmt.Errorf("opencode run evidence: %w", err)
	}
	for _, dir := range []string{filepath.Dir(OpenCodeEvidenceDir(home)), OpenCodeEvidenceDir(home), dst} {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("opencode run evidence: %w", err)
		}
	}
	for _, rel := range files {
		if err := copyEvidenceFile(filepath.Join(src, rel), filepath.Join(dst, rel)); err != nil {
			return "", errors.Join(err, sweepErr)
		}
	}
	return dst, sweepErr
}

func copyEvidenceFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("opencode run evidence: %w", err)
	}
	return nil
}

// SweepOpenCodeRunEvidence deletes every run's evidence directory older than
// maxAge and returns the ids it deleted. Only a real directory named by a run
// identity is touched.
func SweepOpenCodeRunEvidence(home string, maxAge time.Duration, now time.Time) ([]string, error) {
	dir := OpenCodeEvidenceDir(home)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opencode run evidence sweep: %w", err)
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		id := e.Name()
		if !runstate.IsIdentity(id) {
			continue
		}
		path := filepath.Join(dir, id)
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("opencode run evidence sweep: %w", err))
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}
