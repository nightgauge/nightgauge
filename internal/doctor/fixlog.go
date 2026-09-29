package doctor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// The local fix log (#2093): one JSON line per remedy the engine attempted,
// at <state dir>/doctor/fix-log.jsonl. `nightgauge doctor --history` prints
// it. It carries no evidence: only the code, check, fingerprint, remedy,
// verb, outcome and a redacted detail line, so nothing the redactor would
// mask can reach it.

// FixLogEntry is one line of the fix log.
type FixLogEntry struct {
	Time        time.Time `json:"time"`
	Code        string    `json:"code"`
	Check       string    `json:"check"`
	Fingerprint string    `json:"fingerprint"`
	Remedy      string    `json:"remedy"`
	Verb        string    `json:"verb"`
	Outcome     Outcome   `json:"outcome"`
	Detail      string    `json:"detail,omitempty"`
}

// FixLog appends to and reads one fix-log file.
type FixLog struct {
	Path string
	mu   sync.Mutex
}

// fixLogDirName and fixLogName place the log under the machine-state root.
const (
	fixLogDirName = "doctor"
	fixLogName    = "fix-log.jsonl"
)

// DefaultFixLogPath is <state dir>/doctor/fix-log.jsonl (ADR-024 § 8). It
// creates nothing beyond the state root itself.
func DefaultFixLogPath() (string, error) {
	root, err := layout.StateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, fixLogDirName, fixLogName), nil
}

// Append writes one entry. The directory is created 0700 and the file 0600;
// a symlink in place of either is refused, so the write stays inside the
// state directory.
func (l *FixLog) Append(e FixLogEntry) error {
	if l == nil || l.Path == "" {
		return errors.New("fix log: no path")
	}
	e.Detail = redact(e.Detail)
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("fix log: encode: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	dir := filepath.Dir(l.Path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(l.Path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("fix log: refusing %s: not a regular file", l.Path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fix log: %w", err)
	}
	f, err := os.OpenFile(l.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("fix log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("fix log: write: %w", err)
	}
	return f.Close()
}

// ensurePrivateDir creates dir 0700, or accepts an existing real directory.
// A symlink is refused: it could point the log outside the state directory.
func ensurePrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("fix log: create %s: %w", dir, err)
		}
		return os.Chmod(dir, 0o700)
	case err != nil:
		return fmt.Errorf("fix log: %w", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("fix log: refusing %s: the directory is a symlink", dir)
	case !info.IsDir():
		return fmt.Errorf("fix log: refusing %s: not a directory", dir)
	}
	return nil
}

// ReadFixLog returns every entry in the log, oldest first. A missing log is
// an empty history. A line that does not parse is skipped and counted.
func ReadFixLog(path string) (entries []FixLogEntry, malformed int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("fix log: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e FixLogEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			malformed++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return entries, malformed, fmt.Errorf("fix log: read: %w", err)
	}
	return entries, malformed, nil
}
