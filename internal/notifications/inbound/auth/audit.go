package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	auditMaxBytes  = 10 * 1024 * 1024 // 10 MB
	auditKeepFiles = 5
	auditFilename  = "audit.jsonl"
)

// AuditEntry is one line in the JSONL audit log.
type AuditEntry struct {
	Timestamp        string `json:"timestamp"`
	MattermostUserID string `json:"mattermost_user_id"`
	MappedIdentity   string `json:"mapped_identity,omitempty"` // "github:login" or "unmapped"
	ChannelID        string `json:"channel_id"`
	Command          string `json:"command"`
	Args             string `json:"args,omitempty"`
	Result           string `json:"result"` // "allowed" | "denied" | "error"
}

// AuditWriter appends JSONL records to a rotating audit log file.
// Rotation triggers when the current file exceeds auditMaxBytes; the writer
// keeps at most auditKeepFiles rotated copies (numbered .1 through .5).
type AuditWriter struct {
	mu  sync.Mutex
	dir string
	f   *os.File
}

// NewAuditWriter returns an AuditWriter that writes to dir/audit.jsonl.
// The directory is created on first Append if it does not exist.
func NewAuditWriter(dir string) *AuditWriter {
	return &AuditWriter{dir: dir}
}

// Append encodes entry as a JSON line and appends it to the audit log,
// rotating the file first if it has reached auditMaxBytes.
func (w *AuditWriter) Append(entry AuditEntry) error {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit marshal: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpen(); err != nil {
		return err
	}

	// Rotate if the current file exceeds the size limit.
	info, err := w.f.Stat()
	if err != nil {
		return fmt.Errorf("audit stat: %w", err)
	}
	if info.Size() >= auditMaxBytes {
		if err := w.rotate(); err != nil {
			return fmt.Errorf("audit rotate: %w", err)
		}
	}

	_, err = fmt.Fprintf(w.f, "%s\n", line)
	return err
}

// ensureOpen opens (or creates) the audit file. Caller must hold w.mu.
func (w *AuditWriter) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	if err := os.MkdirAll(w.dir, 0o750); err != nil {
		return fmt.Errorf("audit mkdir: %w", err)
	}
	path := filepath.Join(w.dir, auditFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("audit open: %w", err)
	}
	w.f = f
	return nil
}

// rotate closes the current file, shifts existing rotated copies up by one
// (dropping the oldest when the count exceeds auditKeepFiles), then opens
// a fresh audit.jsonl. Caller must hold w.mu.
func (w *AuditWriter) rotate() error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}

	base := filepath.Join(w.dir, auditFilename)

	// Shift: .4→.5, .3→.4, ..., .1→.2, base→.1
	for i := auditKeepFiles - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", base, i)
		dst := fmt.Sprintf("%s.%d", base, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(base, base+".1")

	return w.ensureOpen()
}
