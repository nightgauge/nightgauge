package ipc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

type countingOps struct {
	guard, cleanup, archive, dispatch atomic.Int32
	failDispatch                      atomic.Bool
	s                                 *Server
}

func (o *countingOps) GuardBranch(RecoveryRequest) error { o.guard.Add(1); return nil }
func (o *countingOps) CleanupBranch(RecoveryRequest) (bool, string, error) {
	o.cleanup.Add(1)
	return true, "deleted", nil
}
func (o *countingOps) ArchiveSnapshot(req RecoveryRequest, token string) error {
	o.archive.Add(1)
	return serverRecoveryOps{s: o.s}.ArchiveSnapshot(req, token)
}
func (o *countingOps) Dispatch(RecoveryRequest, string) error {
	o.dispatch.Add(1)
	if o.failDispatch.Load() {
		return errors.New("host refused")
	}
	return nil
}

type recoveryFixture struct {
	s    *Server
	ops  *countingOps
	req  RecoveryRequest
	snap string
	now  *atomic.Int64
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	root := layouttest.Repo(t)
	s := &Server{
		writer:         io.Discard,
		workspaceRoot:  root,
		activeRuntimes: map[string]*runEntry{},
		methods:        map[string]Handler{},
	}
	f := &recoveryFixture{s: s, now: &atomic.Int64{}}
	f.now.Store(time.Now().UnixNano())
	s.recovery.now = func() time.Time { return time.Unix(0, f.now.Load()) }
	s.recovery.keyDir = t.TempDir()
	f.ops = &countingOps{s: s}
	s.recovery.ops = f.ops

	dir := s.pipelineStateDir("")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runID := "0192f8a0-0000-7000-8000-000000000001"
	f.snap = filepath.Join(dir, state.SnapshotFilename(7, runID))
	body := []byte(`{"runId":"` + runID + `"}`)
	if err := os.WriteFile(f.snap, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	f.req = RecoveryRequest{
		Repo: "o/r", Issue: 7, RunID: runID, Branch: "feat/7-thing",
		Stage: "feature-dev", Phase: "implementation", SnapshotSHA256: hex.EncodeToString(sum[:]),
	}
	return f
}

func TestRecoveryClaim_ExactlyOneConcurrentWinner(t *testing.T) {
	f := newRecoveryFixture(t)
	var wg sync.WaitGroup
	var won, held atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.recoveryClaimRun(f.req)
			switch {
			case err == nil:
				won.Add(1)
			case strings.HasPrefix(err.Error(), codeRecoveryClaimHeld):
				held.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || held.Load() != 15 {
		t.Fatalf("won=%d held=%d, want 1/15", won.Load(), held.Load())
	}
}

func TestRecoveryClaim_ExpiredClaimIsReclaimable(t *testing.T) {
	f := newRecoveryFixture(t)
	if _, err := f.s.recoveryClaimRun(f.req); err != nil {
		t.Fatal(err)
	}
	f.now.Add(int64(recoveryClaimTTL + time.Second))
	if _, err := f.s.recoveryClaimRun(f.req); err != nil {
		t.Fatalf("expired claim should be reclaimable: %v", err)
	}
}

func TestRecoveryClaim_ChangedSnapshotRefusedWithoutWriting(t *testing.T) {
	f := newRecoveryFixture(t)
	info, _ := os.Stat(f.snap)
	// Same length, same mtime, different bytes: only the content hash sees it.
	if err := os.WriteFile(f.snap, []byte(strings.Repeat("x", int(info.Size()))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.snap, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.recoveryClaimRun(f.req)
	if err == nil || !strings.HasPrefix(err.Error(), codeRecoverySnapshotChanged) {
		t.Fatalf("err = %v, want %s", err, codeRecoverySnapshotChanged)
	}
	if f.s.recoveryClaimHeld(f.req.RunID) {
		t.Fatal("a refused claim must not record a lease")
	}
}

func TestRecoveryClaim_LiveOwnerRefused(t *testing.T) {
	f := newRecoveryFixture(t)
	e := &runEntry{lastSeen: f.s.recoveryNow(), issue: 7}
	f.s.activeRuntimes[f.req.RunID] = e
	_, err := f.s.recoveryClaimRun(f.req)
	if err == nil || !strings.HasPrefix(err.Error(), codeRunWrongOwner) {
		t.Fatalf("err = %v, want %s", err, codeRunWrongOwner)
	}
}

func TestRecoveryClaim_MalformedIdentityRefused(t *testing.T) {
	f := newRecoveryFixture(t)
	for name, mut := range map[string]func(*RecoveryRequest){
		"run id traversal": func(r *RecoveryRequest) { r.RunID = "../evil" },
		"run id dots":      func(r *RecoveryRequest) { r.RunID = "a..b" },
		"bad sha":          func(r *RecoveryRequest) { r.SnapshotSHA256 = "abc" },
		"branch traversal": func(r *RecoveryRequest) { r.Branch = "feat/../main" },
		"zero issue":       func(r *RecoveryRequest) { r.Issue = 0 },
		"relative tree":    func(r *RecoveryRequest) { r.Worktree = "x/y" },
	} {
		req := f.req
		mut(&req)
		if _, err := f.s.recoveryClaimRun(req); err == nil || !strings.HasPrefix(err.Error(), codeRecoveryInvalid) {
			t.Errorf("%s: err = %v, want %s", name, err, codeRecoveryInvalid)
		}
	}
}

func TestRecoveryClaim_FailsClosedForOrdinaryWriters(t *testing.T) {
	f := newRecoveryFixture(t)
	if _, err := f.s.recoveryClaimRun(f.req); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.resolveRun("pipeline.notifyStageProgress", verbRunProgress, f.req.RunID, "", 7)
	if err == nil || !strings.Contains(err.Error(), codeRecoveryClaimHeld) {
		t.Fatalf("err = %v, want %s", err, codeRecoveryClaimHeld)
	}
}

type fakeFS struct{ calls atomic.Int32 }

func (f *fakeFS) EvalSymlinks(p string) (string, error) {
	f.calls.Add(1)
	if strings.Contains(p, "escape") {
		return "/outside/target", nil
	}
	return p, nil
}
func (f *fakeFS) Lstat(string) (os.FileInfo, error) { f.calls.Add(1); return nil, os.ErrNotExist }

func TestContainedPath(t *testing.T) {
	roots := []string{"/approved/root"}
	for _, p := range []string{"../x", "x/../../y", "/approved/root/../../etc", "relative/path"} {
		fs := &fakeFS{}
		if _, err := containedPath(fs, p, roots); err == nil {
			t.Errorf("%q accepted", p)
		}
		if fs.calls.Load() != 0 {
			t.Errorf("%q made %d filesystem calls before refusal", p, fs.calls.Load())
		}
	}
	if _, err := containedPath(&fakeFS{}, "/approved/root/escape", roots); err == nil {
		t.Error("outward symlink accepted")
	}
	if _, err := containedPath(&fakeFS{}, "/approved/root", roots); err == nil {
		t.Error("the root itself must not count as contained")
	}
	if _, err := containedPath(&fakeFS{}, "/approved/root/wt", roots); err != nil {
		t.Errorf("contained path refused: %v", err)
	}
}
