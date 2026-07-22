package survival

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StoreRelPath is the survival store's path under the workspace, relative to the
// repo root. It lives beside the other pipeline state under .nightgauge.
var StoreRelPath = filepath.Join(".nightgauge", "pipeline", "survival-records.jsonl")

// Store is an append-only JSONL journal of survival records. Capture appends a
// `pending` line; finalize appends a terminal line with the same merge commit
// SHA. Load folds the journal by SHA (last write wins), so a terminal line
// supersedes its earlier pending line without any in-place mutation — preserving
// the append-only invariant the spike (#4134 §1.1) requires.
//
// Append/Finalize are guarded so the journal stays bounded in practice: capture
// is skipped when the SHA is already present, and finalize is only ever invoked
// on a pending→terminal transition (terminal records are idempotent in
// DecideVerdict), so at most two lines accrue per merge.
type Store struct {
	path string
}

// NewStore returns a Store rooted at the workspace's survival journal.
func NewStore(workspaceRoot string) *Store {
	return &Store{path: filepath.Join(workspaceRoot, StoreRelPath)}
}

// Path is the absolute journal path (exposed for diagnostics/tests).
func (s *Store) Path() string { return s.path }

// Load reads the journal and folds it by merge commit SHA (last write wins),
// returning records in first-seen order. A missing file yields an empty slice
// and no error. Malformed lines are skipped (fail-open: a single corrupt line
// must not blind the whole sweep).
func (s *Store) Load() ([]Record, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	byKey := map[string]Record{}
	var order []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if jsonErr := json.Unmarshal(line, &rec); jsonErr != nil {
			continue // skip a corrupt line rather than fail the whole load
		}
		if rec.MergeCommitSHA == "" {
			continue
		}
		if _, seen := byKey[rec.MergeCommitSHA]; !seen {
			order = append(order, rec.MergeCommitSHA)
		}
		byKey[rec.MergeCommitSHA] = rec
	}
	if scanErr := sc.Err(); scanErr != nil {
		return nil, scanErr
	}

	out := make([]Record, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out, nil
}

// Pending returns the subset of the folded journal still awaiting a verdict.
func (s *Store) Pending() ([]Record, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	var pending []Record
	for _, r := range all {
		if r.Verdict == Pending {
			pending = append(pending, r)
		}
	}
	return pending, nil
}

// Append writes a pending survival record, skipping it (returns added=false) when
// the merge commit SHA is already journaled — making capture idempotent across
// pipeline re-runs. A record with an empty SHA is rejected.
func (s *Store) Append(rec Record) (added bool, err error) {
	if rec.MergeCommitSHA == "" {
		return false, fmt.Errorf("survival: refusing to append record with empty merge_commit_sha")
	}
	if rec.Kind == "" {
		rec.Kind = Kind
	}

	existing, err := s.Load()
	if err != nil {
		return false, err
	}
	for _, e := range existing {
		if e.MergeCommitSHA == rec.MergeCommitSHA {
			return false, nil // already captured (any verdict) — idempotent
		}
	}
	if err := s.appendLine(rec); err != nil {
		return false, err
	}
	return true, nil
}

// Finalize appends a terminal record line for an already-journaled SHA. Load's
// last-write-wins fold makes it supersede the pending line. It is a no-op error
// to finalize a record that was never captured.
func (s *Store) Finalize(rec Record) error {
	if rec.MergeCommitSHA == "" {
		return fmt.Errorf("survival: refusing to finalize record with empty merge_commit_sha")
	}
	if rec.Kind == "" {
		rec.Kind = Kind
	}
	return s.appendLine(rec)
}

// appendLine atomically appends one JSON line to the journal, creating the
// parent directory and file as needed.
func (s *Store) appendLine(rec Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("survival: create store dir: %w", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("survival: marshal record: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("survival: open store: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("survival: write record: %w", err)
	}
	return nil
}
