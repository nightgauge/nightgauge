package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OfflineState is the local fallback when GitHub API is unreachable.
// This mirrors the essential fields from the board.
type OfflineState struct {
	Repo        string        `json:"repo"`
	IssueNumber int           `json:"issueNumber"`
	ItemID      string        `json:"itemId"`
	Stage       PipelineStage `json:"stage"`
	Status      BoardStatus   `json:"status"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

// OfflineStore manages local state files for offline fallback.
type OfflineStore struct {
	dir string
	// err is the pipeline state directory's resolution error, returned by
	// every method so nothing reads or writes relative to the cwd.
	err error
}

// NewOfflineStore creates an offline store in the workspace's pipeline state
// directory. An unresolvable workspace (not a git repository) makes every
// method return that error.
func NewOfflineStore(workspaceRoot string) *OfflineStore {
	dir, err := PipelineStateDir(workspaceRoot)
	return &OfflineStore{dir: dir, err: err}
}

// Save persists offline state to disk.
func (s *OfflineStore) Save(state *OfflineState) error {
	if s.err != nil {
		return fmt.Errorf("offline state: %w", s.err)
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("create offline dir: %w", err)
	}

	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal offline state: %w", err)
	}

	filename := filepath.Join(s.dir, fmt.Sprintf("state-%d.json", state.IssueNumber))
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("write offline state: %w", err)
	}

	return nil
}

// Load reads offline state for a specific issue.
func (s *OfflineStore) Load(issueNumber int) (*OfflineState, error) {
	if s.err != nil {
		return nil, fmt.Errorf("offline state: %w", s.err)
	}
	filename := filepath.Join(s.dir, fmt.Sprintf("state-%d.json", issueNumber))
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read offline state: %w", err)
	}

	var state OfflineState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse offline state: %w", err)
	}

	return &state, nil
}

// Remove deletes the offline state file for an issue (after reconciliation).
func (s *OfflineStore) Remove(issueNumber int) error {
	if s.err != nil {
		return fmt.Errorf("offline state: %w", s.err)
	}
	filename := filepath.Join(s.dir, fmt.Sprintf("state-%d.json", issueNumber))
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove offline state: %w", err)
	}
	return nil
}
