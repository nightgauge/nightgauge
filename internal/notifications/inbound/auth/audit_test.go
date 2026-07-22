package auth

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditWriter_Append(t *testing.T) {
	dir := t.TempDir()
	w := NewAuditWriter(dir)

	entry := AuditEntry{
		MattermostUserID: "U123",
		MappedIdentity:   "github:alice",
		ChannelID:        "C001",
		Command:          "run",
		Result:           "allowed",
	}
	if err := w.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read file and verify JSONL.
	path := filepath.Join(dir, auditFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	line := strings.TrimSpace(string(data))
	var got AuditEntry
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal audit line: %v", err)
	}
	if got.MattermostUserID != "U123" {
		t.Errorf("expected mattermost_user_id=U123, got %q", got.MattermostUserID)
	}
	if got.Result != "allowed" {
		t.Errorf("expected result=allowed, got %q", got.Result)
	}
	if got.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestAuditWriter_MultipleAppend(t *testing.T) {
	dir := t.TempDir()
	w := NewAuditWriter(dir)

	for i := 0; i < 5; i++ {
		if err := w.Append(AuditEntry{
			MattermostUserID: "U" + string(rune('0'+i)),
			Command:          "status",
			Result:           "allowed",
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	path := filepath.Join(dir, auditFilename)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		if scanner.Text() != "" {
			count++
		}
	}
	if count != 5 {
		t.Errorf("expected 5 lines, got %d", count)
	}
}

func TestAuditWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	w := NewAuditWriter(dir)

	// Open the file and write an entry to initialize it.
	if err := w.Append(AuditEntry{Command: "run", Result: "allowed"}); err != nil {
		t.Fatalf("initial append: %v", err)
	}

	// Manually set the file size above the threshold to trigger rotation.
	w.mu.Lock()
	// Pad the file to exceed the rotation limit.
	padding := make([]byte, auditMaxBytes)
	for i := range padding {
		padding[i] = '\n'
	}
	if _, err := w.f.Write(padding); err != nil {
		w.mu.Unlock()
		t.Fatalf("padding write: %v", err)
	}
	w.mu.Unlock()

	// This append should trigger rotation.
	if err := w.Append(AuditEntry{Command: "stop", Result: "denied"}); err != nil {
		t.Fatalf("post-rotation append: %v", err)
	}

	// audit.jsonl.1 should now exist.
	if _, err := os.Stat(filepath.Join(dir, auditFilename+".1")); err != nil {
		t.Errorf("expected rotated file audit.jsonl.1 to exist: %v", err)
	}

	// The current audit.jsonl should exist and contain the new entry.
	if _, err := os.Stat(filepath.Join(dir, auditFilename)); err != nil {
		t.Errorf("expected audit.jsonl to exist after rotation: %v", err)
	}
}
