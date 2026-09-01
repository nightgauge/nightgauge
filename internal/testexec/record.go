package testexec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecordSchemaVersion is the execution-record schema version. Readers must
// tolerate an unknown version by ignoring the record rather than failing — a
// record it cannot read is not evidence, and absent evidence is already the
// blocking case.
const RecordSchemaVersion = 1

// Outcome values for an execution record. Only OutcomePass satisfies the gate:
// "we ran it and it failed" is honest, and still not a validated suite.
const (
	OutcomePass = "pass"
	OutcomeFail = "fail"
)

// Record is one observation that a specific test file was actually executed.
//
// It records the command as run, not the command that should have been run.
// The whole failure this package exists to prevent is a claim about execution
// that nobody checked against an execution, so a record that carries only an
// assertion would reproduce it one layer up.
type Record struct {
	V          int    `json:"v"`
	File       string `json:"file"`
	Outcome    string `json:"outcome"`
	Command    string `json:"command,omitempty"`
	Detail     string `json:"detail,omitempty"`
	RecordedAt string `json:"recorded_at"`
}

// Passed reports whether this record satisfies the gate for its file.
func (r Record) Passed() bool { return strings.EqualFold(r.Outcome, OutcomePass) }

// RecordPath is the run-scoped execution record for one issue.
//
// It lives beside the other per-issue pipeline artifacts so it is collected,
// cleaned up and shipped by the machinery that already exists. When epic #12's
// artifact manifest lands, this file stays the writer of record and the
// manifest gains a mirror — Summary() below is the shape to hand it, and
// applyToValidateContext in check.go is the single place that would grow the
// second sink. Shipping the local record first is what keeps this gate from
// being gated on #12.
func RecordPath(workspace string, issueNumber int) string {
	return filepath.Join(workspace, ".nightgauge", "pipeline",
		fmt.Sprintf("test-execution-%d.jsonl", issueNumber))
}

// AppendRecord writes one execution record, creating the file if needed.
func AppendRecord(workspace string, issueNumber int, rec Record) error {
	rec.V = RecordSchemaVersion
	rec.File = normalizePath(rec.File)
	if rec.RecordedAt == "" {
		rec.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.File == "" {
		return fmt.Errorf("execution record needs a file")
	}
	switch strings.ToLower(rec.Outcome) {
	case OutcomePass, OutcomeFail:
	default:
		return fmt.Errorf("execution record outcome must be %q or %q, got %q", OutcomePass, OutcomeFail, rec.Outcome)
	}

	path := RecordPath(workspace, issueNumber)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create pipeline dir: %w", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode execution record: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open execution record: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write execution record: %w", err)
	}
	return nil
}

// ReadRecords loads every execution record for an issue. A missing file is not
// an error — it is the ordinary state of a repo that excludes nothing.
//
// Malformed lines are skipped rather than fatal. A corrupt line is missing
// evidence, and missing evidence already blocks; letting it error instead would
// convert a bad byte into an unexplainable stage failure.
func ReadRecords(workspace string, issueNumber int) ([]Record, error) {
	f, err := os.Open(RecordPath(workspace, issueNumber))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.V != RecordSchemaVersion {
			continue
		}
		rec.File = normalizePath(rec.File)
		out = append(out, rec)
	}
	return out, sc.Err()
}

// PassedFiles indexes the files with at least one passing record.
//
// At least one, not the latest: a file that was executed and passed was
// executed and passed, and a later failing record on the same file is caught by
// the ordinary test gate rather than by this one. Requiring the last record to
// pass would let a flaky re-run order decide a gate whose subject is execution,
// not outcome stability.
func PassedFiles(records []Record) map[string]Record {
	out := map[string]Record{}
	for _, r := range records {
		if r.Passed() {
			if _, seen := out[r.File]; !seen {
				out[r.File] = r
			}
		}
	}
	return out
}

func normalizePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	return strings.TrimSuffix(p, "/")
}
