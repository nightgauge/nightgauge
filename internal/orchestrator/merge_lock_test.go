package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMergeLockAcquireRelease(t *testing.T) {
	mgr := NewMergeLockManager()

	release, err := mgr.Acquire(context.Background(), "nightgauge/nightgauge", 1311)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	status := mgr.Status()
	if !status["nightgauge/nightgauge"].Locked {
		t.Error("should be locked")
	}
	if status["nightgauge/nightgauge"].Holder != "nightgauge/nightgauge#1311" {
		t.Errorf("holder = %q", status["nightgauge/nightgauge"].Holder)
	}

	release()

	// After release, lock status holder should be empty
	// (status may still show the entry)
}

func TestMergeLockSerialization(t *testing.T) {
	mgr := NewMergeLockManager()

	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)
		issueNum := 1300 + i
		go func() {
			defer wg.Done()
			release, err := mgr.Acquire(context.Background(), "repo", issueNum)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}

			mu.Lock()
			order = append(order, issueNum)
			mu.Unlock()

			time.Sleep(10 * time.Millisecond) // Simulate merge work
			release()
		}()
	}

	wg.Wait()

	// All 3 should have executed (order doesn't matter for this test)
	if len(order) != 3 {
		t.Errorf("expected 3 merges, got %d", len(order))
	}
}

func TestMergeLockContextCancel(t *testing.T) {
	mgr := NewMergeLockManager()

	// Acquire first lock
	release, err := mgr.Acquire(context.Background(), "repo", 1)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Try to acquire with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var acquireDone atomic.Bool
	go func() {
		_, err := mgr.Acquire(ctx, "repo", 2)
		if err == nil {
			acquireDone.Store(true)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	release()

	// The second acquire should have timed out
	if acquireDone.Load() {
		t.Error("second acquire should have been cancelled")
	}
}

func TestMergeLockDifferentRepos(t *testing.T) {
	mgr := NewMergeLockManager()

	// Locks for different repos should not block each other
	release1, err := mgr.Acquire(context.Background(), "repo-a", 1)
	if err != nil {
		t.Fatalf("Acquire repo-a: %v", err)
	}

	release2, err := mgr.Acquire(context.Background(), "repo-b", 2)
	if err != nil {
		t.Fatalf("Acquire repo-b: %v", err)
	}

	release1()
	release2()
}
