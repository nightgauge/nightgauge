package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// fakeReloader records FilterRepos calls and exposes a configurable
// IsRunning result.
type fakeReloader struct {
	mu      sync.Mutex
	calls   [][]string
	running bool
}

func (f *fakeReloader) FilterRepos(repos []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(repos))
	copy(cp, repos)
	f.calls = append(f.calls, cp)
}

func (f *fakeReloader) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeReloader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeReloader) lastCall() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// writeYAML writes the given YAML to path. Sleeps just long enough to
// guarantee the mtime tick is observable on coarse filesystem clocks.
func writeYAML(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// Bump mtime explicitly to a future timestamp so the watcher sees a
	// strictly-after-lastMtime change even on filesystems with 1s mtime
	// resolution.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestWatchAutonomousConfigReappliesAllowlistOnChange(t *testing.T) {
	prevInterval := configReloadInterval
	configReloadInterval = 50 * time.Millisecond
	t.Cleanup(func() { configReloadInterval = prevInterval })

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	prevPathFn := config.SwapMachineConfigPathForTest(func() (string, error) { return cfgPath, nil })
	t.Cleanup(prevPathFn)

	// config.Load(workspaceRoot) returns DefaultConfig when no project-tier
	// file exists — which would discard the machine tier entirely. Mirror
	// the production layout (project YAML present, machine YAML providing
	// the autonomous block) so the merged Load surfaces our enabled_repos.
	projectDir := filepath.Join(tmp, ".nightgauge")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("owner: nightgauge\nproject: 1\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	writeYAML(t, cfgPath, `
owner: nightgauge
autonomous:
  enabled_repos:
    - nightgauge
`)

	reloader := &fakeReloader{running: true}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		WatchAutonomousConfig(ctx, reloader, tmp)
		close(done)
	}()

	// Wait for initial mtime to be recorded (a single tick).
	time.Sleep(150 * time.Millisecond)
	if reloader.callCount() != 0 {
		t.Fatalf("expected no FilterRepos calls before config edit, got %d", reloader.callCount())
	}

	// Update the file with a wider allowlist.
	writeYAML(t, cfgPath, `
owner: nightgauge
autonomous:
  enabled_repos:
    - nightgauge
    - acme-mobile
    - acme-dashboard
`)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reloader.callCount() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if reloader.callCount() == 0 {
		t.Fatal("watcher did not call FilterRepos after config change")
	}

	got := reloader.lastCall()
	want := map[string]bool{
		"nightgauge/nightgauge":      true,
		"nightgauge/acme-mobile":    true,
		"nightgauge/acme-dashboard": true,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d repos, got %d (%v)", len(want), len(got), got)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected repo in allowlist: %s", r)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not exit on context cancel")
	}
}

func TestWatchAutonomousConfigSkipsWhenSchedulerStopped(t *testing.T) {
	prevInterval := configReloadInterval
	configReloadInterval = 50 * time.Millisecond
	t.Cleanup(func() { configReloadInterval = prevInterval })

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	prevPathFn := config.SwapMachineConfigPathForTest(func() (string, error) { return cfgPath, nil })
	t.Cleanup(prevPathFn)

	projectDir := filepath.Join(tmp, ".nightgauge")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("owner: nightgauge\nproject: 1\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	writeYAML(t, cfgPath, `
owner: nightgauge
autonomous:
  enabled_repos: [nightgauge]
`)

	reloader := &fakeReloader{running: false}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchAutonomousConfig(ctx, reloader, tmp)

	// Bump mtime; the watcher must observe the change but skip FilterRepos.
	time.Sleep(100 * time.Millisecond)
	writeYAML(t, cfgPath, `
owner: nightgauge
autonomous:
  enabled_repos: [nightgauge, acme-mobile]
`)

	time.Sleep(500 * time.Millisecond)

	if reloader.callCount() != 0 {
		t.Fatalf("expected FilterRepos to be skipped while scheduler stopped, got %d calls", reloader.callCount())
	}
}
