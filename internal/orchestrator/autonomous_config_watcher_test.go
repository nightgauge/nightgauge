package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
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
	// Join before the configReloadInterval / machine-config-path restores run —
	// the watcher reads configReloadInterval once at startup, so a restore while
	// it is live is a write racing that read. t.Cleanup is LIFO, so registering
	// here (after those two) puts this first, and unlike a body-tail join it
	// still runs when an assertion below t.Fatals.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("watcher did not exit on context cancel")
		}
	})

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
		"nightgauge/nightgauge":     true,
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

	done := make(chan struct{})
	go func() {
		WatchAutonomousConfig(ctx, reloader, tmp)
		close(done)
	}()
	// Same LIFO-ordered join as TestWatchAutonomousConfigReappliesAllowlistOnChange:
	// it must run before the configReloadInterval / machine-config-path restores,
	// and it must run even when an assertion below t.Fatals.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("watcher did not exit on context cancel")
		}
	})

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

// fakeRepoSetReloader is fakeReloader plus manifest-driven repo replacement,
// so a test can assert the #704 path without pulling in a whole scheduler.
type fakeRepoSetReloader struct {
	fakeReloader
	mu       sync.Mutex
	replaced [][]depgraph.RepoConfig
	changed  bool
}

func (f *fakeRepoSetReloader) ReplaceRepos(repos []depgraph.RepoConfig) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]depgraph.RepoConfig, len(repos))
	copy(cp, repos)
	f.replaced = append(f.replaced, cp)
	return f.changed
}

func (f *fakeRepoSetReloader) replaceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.replaced)
}

func (f *fakeRepoSetReloader) lastReplaced() []depgraph.RepoConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replaced) == 0 {
		return nil
	}
	return f.replaced[len(f.replaced)-1]
}

// manifestWatchFixture wires the machine-tier config the watcher always needs,
// plus a workspace manifest, and starts the watcher. Returns the manifest path.
func manifestWatchFixture(t *testing.T, sched configReloader, resolve ManifestRepoResolver) (string, func()) {
	t.Helper()
	prevInterval := configReloadInterval
	configReloadInterval = 50 * time.Millisecond

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	prevPathFn := config.SwapMachineConfigPathForTest(func() (string, error) { return cfgPath, nil })

	projectDir := filepath.Join(tmp, ".nightgauge")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("owner: nightgauge\nproject: 1\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("owner: nightgauge\n"), 0o644); err != nil {
		t.Fatalf("write machine config: %v", err)
	}

	vscodeDir := filepath.Join(tmp, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	manifestPath := filepath.Join(vscodeDir, "nightgauge-workspace.yaml")
	if err := os.WriteFile(manifestPath, []byte("workspace:\n  name: w\nrepositories:\n  - name: alpha\n    path: .\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		WatchAutonomousConfigWithManifest(ctx, sched, tmp, resolve)
		close(done)
	}()

	stop := func() {
		cancel()
		<-done
		configReloadInterval = prevInterval
		prevPathFn()
	}
	return manifestPath, stop
}

// TestWatchManifestReplacesRepoSetOnChange is the daemon half of #704: a repo
// added to the manifest while the daemon runs must reach the scheduler, or the
// extension's tree shows a repository nothing is processing.
func TestWatchManifestReplacesRepoSetOnChange(t *testing.T) {
	resolved := []depgraph.RepoConfig{
		{Owner: "nightgauge", Name: "alpha", Project: 1},
		{Owner: "nightgauge", Name: "beta", Project: 2},
	}
	sched := &fakeRepoSetReloader{fakeReloader: fakeReloader{running: true}, changed: true}

	manifestPath, stop := manifestWatchFixture(t, sched, func() []depgraph.RepoConfig {
		return resolved
	})
	t.Cleanup(stop)

	if err := os.WriteFile(manifestPath,
		[]byte("workspace:\n  name: w\nrepositories:\n  - name: alpha\n    path: .\n  - name: beta\n    path: ../beta\n"),
		0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Bump the mtime forward on every poll rather than once up front.
	//
	// The watcher records its baseline mtime when its goroutine first runs,
	// which is not ordered against this test body. A single write racing that
	// baseline can be absorbed into it — the watcher starts up already
	// believing it has seen this version — and the change is then never
	// observed. Re-touching each iteration means a lost race costs one tick
	// instead of the whole test, so the assertion measures the watcher rather
	// than the scheduler's goroutine start-up timing.
	deadline := time.After(3 * time.Second)
	bump := 0
	for sched.replaceCount() == 0 {
		bump++
		future := time.Now().Add(time.Duration(bump) * time.Second)
		if err := os.Chtimes(manifestPath, future, future); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("manifest change did not reach the scheduler within 3s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	got := sched.lastReplaced()
	if len(got) != 2 || got[1].Name != "beta" {
		t.Errorf("ReplaceRepos got %+v, want the two resolved repos", got)
	}
}

// bumpUntil re-touches path until cond() holds or the deadline expires, and
// reports whether cond ever held.
//
// A single up-front write races the watcher's baseline stat: if the write lands
// first, the watcher starts up already believing it has seen this version and
// the change is never observed. A test that then asserts "nothing happened"
// passes for entirely the wrong reason — which is exactly what the first draft
// of TestWatchManifestIgnoresEmptyResolution did, surviving deletion of the
// guard it existed to pin.
func bumpUntil(t *testing.T, path string, cond func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for bump := 1; ; bump++ {
		if cond() {
			return true
		}
		future := time.Now().Add(time.Duration(bump) * time.Second)
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		select {
		case <-deadline:
			return cond()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestWatchManifestIgnoresEmptyResolution pins that an unreadable or mid-edit
// manifest never narrows the scheduler to nothing.
//
// The resolver call count is what makes this test real: without it, a watcher
// that never looked at the manifest at all would satisfy "ReplaceRepos was not
// called" just as well as one that looked and correctly declined.
func TestWatchManifestIgnoresEmptyResolution(t *testing.T) {
	sched := &fakeRepoSetReloader{fakeReloader: fakeReloader{running: true}}

	var mu sync.Mutex
	resolverCalls := 0
	manifestPath, stop := manifestWatchFixture(t, sched, func() []depgraph.RepoConfig {
		mu.Lock()
		resolverCalls++
		mu.Unlock()
		return nil // resolver could not read the manifest
	})
	t.Cleanup(stop)

	if err := os.WriteFile(manifestPath, []byte("repositories: [ this is not valid yaml"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	reached := bumpUntil(t, manifestPath, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return resolverCalls > 0
	}, 3*time.Second)
	if !reached {
		t.Fatal("watcher never invoked the resolver — the assertion below would be vacuous")
	}

	if n := sched.replaceCount(); n != 0 {
		t.Errorf("ReplaceRepos called %d times on an empty resolution, want 0", n)
	}
}

// TestWatchManifestSkipsWhenSchedulerStopped mirrors the machine-tier watcher's
// contract: nothing is applied to a scheduler that is not running.
//
// It then starts the scheduler and asserts the change DOES land. Without that
// second half the test would pass against a watcher that never works at all.
func TestWatchManifestSkipsWhenSchedulerStopped(t *testing.T) {
	sched := &fakeRepoSetReloader{fakeReloader: fakeReloader{running: false}, changed: true}

	manifestPath, stop := manifestWatchFixture(t, sched, func() []depgraph.RepoConfig {
		return []depgraph.RepoConfig{{Owner: "nightgauge", Name: "alpha", Project: 1}}
	})
	t.Cleanup(stop)

	if err := os.WriteFile(manifestPath,
		[]byte("workspace:\n  name: w\nrepositories:\n  - name: alpha\n    path: .\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	bumpUntil(t, manifestPath, func() bool { return sched.replaceCount() > 0 }, 400*time.Millisecond)
	if n := sched.replaceCount(); n != 0 {
		t.Fatalf("ReplaceRepos called %d times while stopped, want 0", n)
	}

	// Now prove the watcher is alive and the zero above meant "stopped",
	// not "broken".
	sched.fakeReloader.mu.Lock()
	sched.fakeReloader.running = true
	sched.fakeReloader.mu.Unlock()

	if !bumpUntil(t, manifestPath, func() bool { return sched.replaceCount() > 0 }, 3*time.Second) {
		t.Error("ReplaceRepos never fired after the scheduler started — the watcher is not working")
	}
}

// TestWatchManifestNilResolverKeepsLegacyBehavior proves the manifest watch is
// strictly additive — WatchAutonomousConfig's original contract is unchanged.
func TestWatchManifestNilResolverKeepsLegacyBehavior(t *testing.T) {
	sched := &fakeRepoSetReloader{fakeReloader: fakeReloader{running: true}, changed: true}

	manifestPath, stop := manifestWatchFixture(t, sched, nil)
	t.Cleanup(stop)

	writeYAML(t, manifestPath, "workspace:\n  name: w\nrepositories:\n  - name: beta\n    path: ../beta\n")

	time.Sleep(300 * time.Millisecond)
	if n := sched.replaceCount(); n != 0 {
		t.Errorf("ReplaceRepos called %d times with a nil resolver, want 0", n)
	}
}
