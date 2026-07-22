package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MergeLockManager serializes merges per repository to avoid conflicts.
// Only one PR can be merged at a time per repo.
type MergeLockManager struct {
	mu    sync.Mutex
	locks map[string]*repoLock
}

type repoLock struct {
	mu         sync.Mutex
	holder     string // "repo#issue" of the current holder
	acquiredAt time.Time
}

// NewMergeLockManager creates a merge lock manager.
func NewMergeLockManager() *MergeLockManager {
	return &MergeLockManager{
		locks: make(map[string]*repoLock),
	}
}

// Acquire locks the merge slot for a repo. Blocks until available.
// Returns a release function that must be called after merge completes.
func (m *MergeLockManager) Acquire(ctx context.Context, repo string, issueNumber int) (func(), error) {
	m.mu.Lock()
	lock, ok := m.locks[repo]
	if !ok {
		lock = &repoLock{}
		m.locks[repo] = lock
	}
	m.mu.Unlock()

	// Try to acquire with context cancellation
	done := make(chan struct{})
	go func() {
		lock.mu.Lock()
		close(done)
	}()

	select {
	case <-done:
		lock.holder = fmt.Sprintf("%s#%d", repo, issueNumber)
		lock.acquiredAt = time.Now()

		return func() {
			lock.holder = ""
			lock.mu.Unlock()
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Status returns the current merge lock status for all repos.
func (m *MergeLockManager) Status() map[string]MergeLockStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := make(map[string]MergeLockStatus)
	for repo, lock := range m.locks {
		status[repo] = MergeLockStatus{
			Repo:   repo,
			Locked: lock.holder != "",
			Holder: lock.holder,
		}
	}
	return status
}

// MergeLockStatus reports the merge lock state for a repo.
type MergeLockStatus struct {
	Repo   string `json:"repo"`
	Locked bool   `json:"locked"`
	Holder string `json:"holder,omitempty"`
}
