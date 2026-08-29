// Package gitworktree is the single chokepoint through which nightgauge
// mutates a repository's worktree set, and the place that serialises those
// mutations per repository.
//
// # Why a chokepoint exists at all (#1163)
//
// `git worktree add` creates `$GIT_COMMON_DIR/worktrees/<id>/` with mkdir()
// and, as its very next action, writes a `locked` file into it — git's own
// source comment says the file is there "to prevent it from being pruned
// while being created". Between those two syscalls the entry has neither
// `locked` nor `gitdir`, which is exactly the state git's
// should_prune_worktree() classifies as prunable. A prune that lands inside
// that window deletes the half-built directory and the add dies with:
//
//	fatal: could not open '.git/worktrees/<id>/locked' for writing:
//	       No such file or directory
//
// Nightgauge does both operations against the same repo root, concurrently and
// by design: runs dispatch up to `max_concurrent` while the scheduler's sweep
// ticker reclaims merged worktrees. The failure surfaces as an unexplained
// pipeline start failure that names nothing about the sweep, so it triages as
// a mystery.
//
// The fix is serialisation, and serialisation only works if EVERY mutation
// goes through it — a guard one caller bypasses is not a guard. So the
// mutating git invocations do not live at their call sites any more; they live
// here, and every caller in the tree reaches them through this package.
//
// # What is and is not serialised
//
// Mutations (add / remove / prune) are serialised. `git worktree list` is not:
// it is read-only, it is used by long scans that would otherwise hold the lock
// for the duration of a whole sweep, and a stale listing is already a case
// every consumer handles — an entry can always vanish between the listing and
// the act.
//
// # Two locks, deliberately
//
// The in-process mutex is keyed on the repository's git common directory, so
// two callers naming the same repository by different paths (a repo root and
// one of its linked worktrees, a symlinked checkout) still contend. That
// covers the daemon, which is where the concurrency the product ships lives.
//
// It does not cover a second process, and nightgauge genuinely has several:
// `nightgauge worktree sweep` and `nightgauge cleanup` run as separate CLI
// processes against a repo the daemon may be dispatching into, and the
// operator's own shell is a third. So the in-process mutex is layered over an
// advisory flock on `$GIT_COMMON_DIR/nightgauge-worktree.lock`, which every
// nightgauge process — daemon or CLI verb — takes for the same critical
// section. The kernel releases an flock when its holder dies, so a crashed
// process cannot wedge the repository.
//
// Neither lock can constrain git itself: `git gc` runs `git worktree prune
// --expire`, and an operator's hand-run `git worktree prune` takes no lock of
// ours. Those remain able to hit the window. This package shrinks the exposure
// to third parties rather than eliminating it, which is the most a
// cooperative, advisory scheme can do.
package gitworktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Session is the right to mutate one repository's worktree set. It is handed
// to the callback of Do and is valid only for that callback's lifetime.
//
// Its methods do NOT re-acquire the lock: that is the whole point of the type.
// A compound teardown (git remove → os.RemoveAll → prune) must be one critical
// section, not three, or a creation can slip between the removal and the
// prune. Making the already-held case a distinct type rather than a re-entrant
// mutex means the compiler, not a convention, keeps the two apart.
type Session struct {
	repoRoot string
}

// RepoRoot is the directory this session's git commands run from.
func (s *Session) RepoRoot() string { return s.repoRoot }

// Add runs `git worktree add` with the given arguments (everything after
// "add").
func (s *Session) Add(args ...string) ([]byte, error) {
	return s.run(append([]string{"worktree", "add"}, args...))
}

// Remove runs `git worktree remove <path> --force`.
//
// --force is not a parameter here: every caller in the tree has already made
// its own decision about whether the worktree holds work worth keeping (see
// blockingChanges in internal/execution), and a non-forced remove would refuse
// on the exhaust those callers have deliberately classified as disposable.
func (s *Session) Remove(path string) ([]byte, error) {
	return s.run([]string{"worktree", "remove", path, "--force"})
}

// Prune runs `git worktree prune` — the operation whose window this package
// exists to keep away from a concurrent add.
func (s *Session) Prune() ([]byte, error) {
	return s.run([]string{"worktree", "prune"})
}

func (s *Session) run(args []string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.repoRoot
	return cmd.CombinedOutput()
}

// Do runs fn holding repoRoot's worktree-mutation lock, both in-process and
// (best effort) across processes. fn's error is returned unchanged.
//
// fn must not block on anything slow that is not itself a worktree mutation —
// it holds a lock every run's provisioning contends for. The SDK-CLI build
// that follows a worktree creation, for instance, deliberately runs outside
// it.
func Do(repoRoot string, fn func(s *Session) error) error {
	release := acquire(repoRoot)
	defer release()
	return fn(&Session{repoRoot: repoRoot})
}

// Add creates a worktree under repoRoot — the single-operation wrapper of Do.
func Add(repoRoot string, args ...string) (out []byte, err error) {
	_ = Do(repoRoot, func(s *Session) error {
		out, err = s.Add(args...)
		return nil
	})
	return out, err
}

// Remove removes a worktree registered under repoRoot.
func Remove(repoRoot, path string) (out []byte, err error) {
	_ = Do(repoRoot, func(s *Session) error {
		out, err = s.Remove(path)
		return nil
	})
	return out, err
}

// Prune prunes repoRoot's stale worktree registrations.
func Prune(repoRoot string) (out []byte, err error) {
	_ = Do(repoRoot, func(s *Session) error {
		out, err = s.Prune()
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Locking
// ---------------------------------------------------------------------------

var (
	registryMu sync.Mutex
	locks      = map[string]*sync.Mutex{}
	keyCache   = map[string]string{}
)

// flockTimeout bounds the wait for the cross-process lock. A wedged holder
// must not be able to stall every dispatch in the daemon indefinitely, so the
// wait expires and the caller proceeds under the in-process lock alone,
// loudly. A crashed holder never needs the timeout — the kernel drops its
// flock.
var flockTimeout = 30 * time.Second

// acquire takes both locks for repoRoot and returns the release func.
func acquire(repoRoot string) func() {
	key := lockKey(repoRoot)

	registryMu.Lock()
	mu, ok := locks[key]
	if !ok {
		mu = &sync.Mutex{}
		locks[key] = mu
	}
	registryMu.Unlock()

	mu.Lock()

	unflock := lockFileFor(key)
	return func() {
		if unflock != nil {
			unflock()
		}
		mu.Unlock()
	}
}

// lockKey resolves repoRoot to its repository's git common directory, so every
// path naming the same repository — the main checkout, any linked worktree, a
// symlinked or relative spelling of either — maps to one lock. Without it a
// teardown that names a worktree and a creation that names the repo root would
// take two different locks and serialise nothing.
//
// Results are memoised per input path: the mapping cannot change for a live
// repository, and resolving it costs a git subprocess.
func lockKey(repoRoot string) string {
	clean := repoRoot
	if abs, err := filepath.Abs(repoRoot); err == nil {
		clean = abs
	}

	registryMu.Lock()
	cached, ok := keyCache[clean]
	registryMu.Unlock()
	if ok {
		return cached
	}

	key := resolveCommonDir(clean)

	registryMu.Lock()
	keyCache[clean] = key
	registryMu.Unlock()
	return key
}

func resolveCommonDir(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
				return resolved
			}
			return filepath.Clean(p)
		}
	}
	// Not a git repository, or a git too old for --path-format. Fall back to
	// the path itself: a caller in that state cannot be racing a worktree
	// prune anyway, and a per-path lock is still correct, merely coarser.
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		return resolved
	}
	return dir
}

// lockFileFor takes the advisory cross-process lock, returning the release
// func, or nil when no cross-process lock could be taken (a non-repository
// path, an unwritable git dir, an unsupported platform, or a timeout). Every
// nil path is fail-open by design: the in-process lock is still held, and
// refusing to provision a worktree because a lock file could not be opened
// would turn a hardening measure into an outage.
func lockFileFor(key string) func() {
	info, err := os.Stat(key)
	if err != nil || !info.IsDir() {
		return nil
	}
	path := filepath.Join(key, "nightgauge-worktree.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	if err := flockExclusive(f, flockTimeout); err != nil {
		_ = f.Close()
		if err != errFlockUnsupported {
			fmt.Fprintf(os.Stderr,
				"[WARN] gitworktree: cross-process worktree lock on %s unavailable (%v) — proceeding with the in-process lock only; a concurrent nightgauge process could still prune inside this operation's window\n",
				path, err)
		}
		return nil
	}
	return func() {
		_ = funlock(f)
		_ = f.Close()
	}
}
