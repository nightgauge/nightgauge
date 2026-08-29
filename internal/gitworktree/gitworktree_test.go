package gitworktree

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// helperEnv makes the test binary re-exec itself as a lock-holding helper
// process — the only way to prove the CROSS-process half of the guard, which
// an in-process mutex would satisfy vacuously.
const helperEnv = "NIGHTGAUGE_GITWORKTREE_LOCK_HELPER"

func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		os.Exit(runLockHelper(dir))
	}
	// The code under test shells out to a bare `git`, so it reads the
	// operator's / CI image's ambient config unless the whole process is
	// isolated (#542).
	gittest.IsolateProcess()
	os.Exit(m.Run())
}

// runLockHelper takes the cross-process worktree lock on dir's repository,
// announces it on stdout, and holds it until stdin closes.
func runLockHelper(dir string) int {
	release := acquire(dir)
	defer release()
	fmt.Println("held")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
	return 0
}

// repoFixture is a real single-commit repository.
func repoFixture(t *testing.T) string {
	t.Helper()
	root := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gittest.Run(t, root, "add", "README.md")
	gittest.Run(t, root, "commit", "-m", "initial")
	return root
}

// TestPruneCannotRunInsideACreationWindow is the red-proving test for #1163.
//
// The defect's window is inside git itself — between the mkdir() of
// `$GIT_COMMON_DIR/worktrees/<id>/` and the write of the `locked` file that
// git's own source says exists "to prevent it from being pruned while being
// created". That window is microseconds wide, so a test that merely races a
// real `git worktree add` against a real prune passes with or without the fix
// and proves nothing (see TestRealWorktreeAddSurvivesAConcurrentPruneStorm,
// which is kept for end-to-end coverage and is explicitly NOT the red-prover).
//
// So the creating side here is a faithful model of git's sequence rather than
// git itself: mkdir the registration directory, wait, then write `locked`. The
// wait is the window, widened from microseconds to milliseconds — the ONLY
// difference from what git does. The pruning side is the real production
// Prune(), running the real `git worktree prune` under the real lock.
//
// With the serialisation in place the half-built entry survives and `locked`
// is written. Neuter the serialisation (make Do call fn without acquire) and
// the prune deletes the directory mid-window, so the write fails with exactly
// the production symptom: ENOENT on `.git/worktrees/<id>/locked`.
func TestPruneCannotRunInsideACreationWindow(t *testing.T) {
	root := repoFixture(t)
	gitDir := filepath.Join(root, ".git")

	const iterations = 20
	const window = 15 * time.Millisecond

	stop := make(chan struct{})
	var pruneWG sync.WaitGroup
	pruneWG.Add(1)
	go func() {
		defer pruneWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Production prune, production lock.
			if out, err := Prune(root); err != nil {
				t.Errorf("git worktree prune failed: %v\n%s", err, out)
				return
			}
		}
	}()

	var failures int
	for i := 0; i < iterations; i++ {
		id := fmt.Sprintf("issue-%d", i)
		if err := createRegistrationLikeGit(root, gitDir, id, window); err != nil {
			failures++
			t.Errorf("iteration %d: %v", i, err)
		}
		// Retire the entry so the next iteration starts clean.
		_ = os.RemoveAll(filepath.Join(gitDir, "worktrees", id))
	}
	close(stop)
	pruneWG.Wait()

	if failures > 0 {
		t.Fatalf("%d/%d creations were destroyed by a concurrent prune", failures, iterations)
	}
}

// createRegistrationLikeGit reproduces the ordering of git's
// add_worktree(): mkdir the registration directory, then write `locked` into
// it, then the rest of the registration. `window` is the interval between the
// first two steps — the interval git leaves open for microseconds and #1163 is
// about.
func createRegistrationLikeGit(repoRoot, gitDir, id string, window time.Duration) error {
	return Do(repoRoot, func(s *Session) error {
		dir := filepath.Join(gitDir, "worktrees", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir registration: %w", err)
		}

		time.Sleep(window)

		// git: "create 'locked' file to prevent the worktree from being
		// pruned while being created". If a prune got in, this is where the
		// production failure lands, with this exact message.
		lockedPath := filepath.Join(dir, "locked")
		if err := os.WriteFile(lockedPath, []byte("initializing\n"), 0o644); err != nil {
			return fmt.Errorf("could not open '%s' for writing: %w", lockedPath, err)
		}

		// The remainder of the registration git writes before unlocking.
		treePath := filepath.Join(repoRoot, ".worktrees", id)
		if err := os.WriteFile(filepath.Join(dir, "gitdir"), []byte(filepath.Join(treePath, ".git")+"\n"), 0o644); err != nil {
			return fmt.Errorf("write gitdir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "commondir"), []byte("../..\n"), 0o644); err != nil {
			return fmt.Errorf("write commondir: %w", err)
		}
		return os.Remove(lockedPath)
	})
}

// TestPruneDoesDeleteAHalfBuiltRegistration is the premise check for the test
// above: it asserts that `git worktree prune` really does delete a
// registration directory that has neither `locked` nor `gitdir`. If this ever
// stops being true — a git version changes the classification, say — the
// red-prover above would go green for a reason that has nothing to do with
// nightgauge's lock, and this test is what says so.
func TestPruneDoesDeleteAHalfBuiltRegistration(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(root, ".git", "worktrees", "issue-999")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if out, err := Prune(root); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected `git worktree prune` to delete a registration with no `locked` and no `gitdir`; stat err = %v.\n"+
			"The window this package guards may no longer exist in this git version — re-derive #1163 before trusting the guard's tests.", err)
	}
}

// TestRealWorktreeAddSurvivesAConcurrentPruneStorm drives the production
// entry points end to end: real `git worktree add` against a real prune loop
// on the same repository.
//
// It is deliberately NOT the red-proof. The genuine window is microseconds
// wide, so this test would pass most runs even with the serialisation removed
// — treating it as the evidence would be the vacuous-assertion defect class.
// Its job is to show the guarded production path still works, and that the
// lock does not deadlock add against prune.
func TestRealWorktreeAddSurvivesAConcurrentPruneStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("iteration-heavy")
	}
	root := repoFixture(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if out, err := Prune(root); err != nil {
				t.Errorf("prune: %v\n%s", err, out)
				return
			}
		}
	}()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("wt-%d", i))
		if out, err := Add(root, "--detach", dir, "HEAD"); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("iteration %d: git worktree add failed: %v\n%s", i, err, out)
		}
		if out, err := Remove(root, dir); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("iteration %d: git worktree remove failed: %v\n%s", i, err, out)
		}
	}
	close(stop)
	wg.Wait()
}

// TestDoSerialisesConcurrentCallers asserts the property directly: two
// critical sections for the same repository never overlap.
func TestDoSerialisesConcurrentCallers(t *testing.T) {
	root := repoFixture(t)

	var inside, overlaps int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = Do(root, func(s *Session) error {
					if atomic.AddInt32(&inside, 1) != 1 {
						atomic.AddInt32(&overlaps, 1)
					}
					time.Sleep(200 * time.Microsecond)
					atomic.AddInt32(&inside, -1)
					return nil
				})
			}
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Fatalf("critical sections overlapped %d times — worktree mutation is not serialised", overlaps)
	}
}

// TestLockKeyIsTheRepositoryNotThePath: a teardown naming a linked worktree
// and a creation naming the repo root must contend on ONE lock. Keying on the
// caller's path would give them two, and serialise nothing.
func TestLockKeyIsTheRepositoryNotThePath(t *testing.T) {
	root := repoFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := Add(root, "--detach", linked, "HEAD"); err != nil {
		t.Fatalf("add linked worktree: %v\n%s", err, out)
	}

	if got, want := lockKey(linked), lockKey(root); got != want {
		t.Fatalf("linked worktree keyed to %q, repo root to %q — they must share one lock", got, want)
	}
	// A relative spelling of the same repository must not fork the key either.
	rel := filepath.Join(root, ".", "..", filepath.Base(root))
	if got, want := lockKey(rel), lockKey(root); got != want {
		t.Fatalf("relative spelling keyed to %q, repo root to %q", got, want)
	}
}

// TestCrossProcessLockBlocksASecondProcess proves the half an in-process mutex
// cannot cover. `nightgauge worktree sweep` and `nightgauge cleanup` are
// separate processes from the daemon; if the guard were in-process only, this
// test would pass instantly and the daemon could still prune inside a CLI
// verb's creation window.
func TestCrossProcessLockBlocksASecondProcess(t *testing.T) {
	if !crossProcessLockSupported {
		t.Skip("platform has no advisory file lock")
	}
	root := repoFixture(t)

	helper := exec.Command(os.Args[0], "-test.run", "TestMainHelperNeverRuns")
	helper.Env = append(os.Environ(), helperEnv+"="+root)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	// Reap it on every exit path, including a failed assertion.
	defer func() {
		_ = stdin.Close()
		_ = helper.Wait()
	}()

	ready := make([]byte, len("held\n"))
	if _, err := io.ReadFull(stdout, ready); err != nil {
		t.Fatalf("helper never reported holding the lock: %v", err)
	}
	if !strings.HasPrefix(string(ready), "held") {
		t.Fatalf("unexpected helper output %q", string(ready))
	}

	const hold = 250 * time.Millisecond
	go func() {
		time.Sleep(hold)
		_ = stdin.Close()
	}()

	start := time.Now()
	if err := Do(root, func(s *Session) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	waited := time.Since(start)

	// Allow slack for process scheduling, but require that the second process
	// genuinely waited rather than walking straight in.
	if waited < hold/2 {
		t.Fatalf("Do returned after %v while another PROCESS held the lock for %v — "+
			"the cross-process guard is not in effect", waited, hold)
	}
}
