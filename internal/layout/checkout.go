package layout

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// checkoutDirName is the directory CHECKOUT is inside a checkout's own git
// dir (ADR-024 § 1, § 7).
const checkoutDirName = "nightgauge-worktree"

// Per-checkout entries: every unkeyed singleton and per-checkout runtime file
// or directory that lives in CHECKOUT (ADR-024 § 2, § 7). A file shared by
// several orchestrators must be keyed by issue or run id and live in CLONE; a
// file that is not keyed belongs to exactly one checkout and lives here.
const (
	// Run control, written by the checkout's orchestrator and daemon.
	CheckoutCurrentRun = "current-run.json"
	CheckoutRunState   = "run-state.json"
	CheckoutBatchState = "batch-state.json"
	CheckoutQueueState = "queue-state.json"
	// CheckoutPlan is the hooks' PLAN.md fallback.
	CheckoutPlan = "PLAN.md"
	// CheckoutServeLock is the lock `nightgauge serve` holds for its lifetime:
	// one daemon per checkout (ADR-024 § 7).
	CheckoutServeLock = "serve.lock"
	// CheckoutBackendLog is the daemon's log.
	CheckoutBackendLog = "go-backend.log"

	// Per-checkout runtime state that sat in the working tree's .nightgauge/.
	CheckoutAttention         = "attention"
	CheckoutAttentionCoverage = "attention-coverage.json"
	CheckoutAutonomous        = "autonomous"
	CheckoutHealth            = "health"
	CheckoutGraph             = "graph"
	CheckoutContainment       = "containment"
	CheckoutNotifications     = "notifications"
	CheckoutSkills            = "skills"
	CheckoutTriage            = "triage"
	CheckoutFocus             = "focus.yaml"
	CheckoutPerformanceMode   = "performance-mode.yaml"
	CheckoutCarefulLock       = "careful.lock"
)

// CheckoutEntry is one per-checkout file or directory and where an older
// build kept it, relative to the checkout's working tree.
type CheckoutEntry struct {
	// Name is the entry's name inside CHECKOUT.
	Name string
	// Dir reports a directory rather than a single file.
	Dir bool
	// Legacy is the pre-ADR-024 location relative to the checkout's working
	// tree, slash-separated.
	Legacy string
	// CloneLegacyClass is set for the run-control singletons the per-clone
	// move (layout v1) placed in CLONE/pipeline or CLONE/logs: the class they
	// sat in there, under the same name. A v1 clone moves them to its main
	// checkout's CHECKOUT.
	CloneLegacyClass string
}

// CheckoutEntries is every per-checkout entry, in the order `nightgauge
// layout` and the migration report them.
var CheckoutEntries = []CheckoutEntry{
	{Name: CheckoutCurrentRun, Legacy: ".nightgauge/pipeline/current-run.json", CloneLegacyClass: ClassPipeline},
	{Name: CheckoutRunState, Legacy: ".nightgauge/pipeline/run-state.json", CloneLegacyClass: ClassPipeline},
	{Name: CheckoutBatchState, Legacy: ".nightgauge/pipeline/batch-state.json", CloneLegacyClass: ClassPipeline},
	{Name: CheckoutQueueState, Legacy: ".nightgauge/pipeline/queue-state.json", CloneLegacyClass: ClassPipeline},
	{Name: CheckoutPlan, Legacy: ".nightgauge/pipeline/PLAN.md", CloneLegacyClass: ClassPipeline},
	{Name: CheckoutBackendLog, Legacy: ".nightgauge/logs/go-backend.log", CloneLegacyClass: ClassLogs},
	{Name: CheckoutAttention, Dir: true, Legacy: ".nightgauge/attention"},
	{Name: CheckoutAttentionCoverage, Legacy: ".nightgauge/attention-coverage.json"},
	{Name: CheckoutAutonomous, Dir: true, Legacy: ".nightgauge/autonomous"},
	{Name: CheckoutHealth, Dir: true, Legacy: ".nightgauge/health"},
	{Name: CheckoutGraph, Dir: true, Legacy: ".nightgauge/graph"},
	{Name: CheckoutContainment, Dir: true, Legacy: ".nightgauge/containment"},
	{Name: CheckoutNotifications, Dir: true, Legacy: ".nightgauge/notifications"},
	{Name: CheckoutSkills, Dir: true, Legacy: ".nightgauge/skills"},
	{Name: CheckoutTriage, Dir: true, Legacy: ".nightgauge/triage"},
	{Name: CheckoutFocus, Legacy: ".nightgauge/focus.yaml"},
	{Name: CheckoutPerformanceMode, Legacy: ".nightgauge/performance-mode.yaml"},
	{Name: CheckoutCarefulLock, Legacy: ".nightgauge/careful.lock"},
}

// GitDir returns the canonical git dir of the checkout dir is in: `git
// rev-parse --absolute-git-dir`, symlink-evaluated, with the inherited GIT_*
// location variables cleared. For the main checkout it is the git common dir;
// for a linked worktree it is <git-common-dir>/worktrees/<name>, which git
// deletes when the worktree is removed. Cached per dir for the process.
func GitDir(dir string) (string, error) {
	if v, ok := gitDirCache.Load(dir); ok {
		return v.(string), nil
	}
	cmd := exec.Command("git", "rev-parse", "--absolute-git-dir")
	cmd.Dir = dir
	cmd.Env = withoutGitLocationEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --absolute-git-dir in %s: %w", dir, err)
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" || !filepath.IsAbs(gitDir) {
		return "", fmt.Errorf("git rev-parse --absolute-git-dir in %s returned %q", dir, gitDir)
	}
	if resolved, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = resolved
	}
	gitDir = filepath.Clean(gitDir)
	gitDirCache.Store(dir, gitDir)
	return gitDir, nil
}

var gitDirCache sync.Map

// CheckoutDir returns CHECKOUT = <absolute-git-dir>/nightgauge-worktree for
// the checkout root is in (ADR-024 § 1, § 7): the unkeyed singletons and
// per-checkout runtime files of one checkout. The main checkout's git dir is
// the git common dir, so its CHECKOUT sits beside CLONE; a linked worktree's
// is inside its own git dir and is deleted with it.
//
// CHECKOUT is created with CLONE's mode rules (0700, or git's group mode in a
// core.sharedRepository clone) and refused when it is a symlink or not a
// directory. When the git dir is not writable it is not created and the path
// is still returned, so read-only commands report without writing.
func CheckoutDir(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: resolving the per-checkout directory from %q", ErrRootNotAbsolute, root)
	}
	root = filepath.Clean(root)
	gitDir, err := GitDir(root)
	if err != nil {
		return "", notGitRepository(root, err)
	}
	dir := filepath.Join(gitDir, checkoutDirName)
	if err := ensureCloneRoot(dir, gitDir); err != nil {
		return "", err
	}
	return dir, nil
}

// CheckoutPath is the path of name inside CHECKOUT for the checkout root is
// in, after the name is validated (a relative path without "." or ".."
// components). It creates nothing beyond CHECKOUT itself. An entry that
// exists as a symlink is refused: a writer must never follow a link out of
// CHECKOUT (ADR-024 § 17).
func CheckoutPath(root, name string) (string, error) {
	clean, err := cleanClassFileName(name)
	if err != nil {
		return "", err
	}
	dir, err := CheckoutDir(root)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", name, err)
	}
	p := filepath.Join(dir, clean)
	if info, err := os.Lstat(p); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s is a symlink; remove it", ErrUnsafeCloneDir, p)
	}
	return p, nil
}

// CheckoutSubdir is CheckoutPath for a directory entry, created with mode
// 0700 when absent. Use it for a writer that owns a per-checkout directory
// (attention/, health/, ...).
func CheckoutSubdir(root, name string) (string, error) {
	p, err := CheckoutPath(root, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", p, err)
	}
	if info, err := os.Lstat(p); err != nil {
		return "", err
	} else if err := refuseUnsafeDir(p, info); err != nil {
		return "", err
	}
	return p, nil
}

// displayCheckout is how help text spells CHECKOUT: the main checkout's.
func displayCheckout() string { return path.Join(displayGitDir, checkoutDirName) }

// CheckoutDisplay names CHECKOUT, or an entry in it, for help and flag text,
// as seen from the main checkout: .git/nightgauge-worktree[/name]. A linked
// worktree's is .git/worktrees/<name>/nightgauge-worktree; `nightgauge
// layout` prints the resolved absolute path.
func CheckoutDisplay(name ...string) string {
	return path.Join(append([]string{displayCheckout()}, name...)...)
}

// WriteCheckoutFile writes data to name inside CHECKOUT through a temporary
// file renamed into place, confined to CHECKOUT (ADR-024 § 7, § 17). It
// returns the absolute path written.
func WriteCheckoutFile(root, name string, data io.Reader) (string, error) {
	dir, err := CheckoutDir(root)
	if err != nil {
		return "", err
	}
	return writeConfined(dir, name, data, false)
}

// AppendCheckoutFile appends data to name inside CHECKOUT, creating it and its
// parents, confined to CHECKOUT. It returns the absolute path appended to.
func AppendCheckoutFile(root, name string, data io.Reader) (string, error) {
	dir, err := CheckoutDir(root)
	if err != nil {
		return "", err
	}
	return writeConfined(dir, name, data, true)
}

// CloneLockName is the lock file in CLONE that appends to shared JSONL take
// for the duration of the append (ADR-024 § 7). internal/layout/clonelock
// takes it.
const CloneLockName = ".lock"

// CloneRootOf returns the CLONE directory p lies in, and true, when p is
// inside a per-clone root: an ancestor named "nightgauge" whose parent is a
// git directory (it holds HEAD and objects/). It runs no git and touches only
// the ancestors' metadata, so an appender can find the clone lock from the
// path it was handed.
func CloneRootOf(p string) (string, bool) {
	if p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	cur := filepath.Clean(p)
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		if filepath.Base(cur) == cloneDirName && isGitDir(parent) {
			return cur, true
		}
		cur = parent
	}
}

func isGitDir(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || !info.Mode().IsRegular() {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "objects"))
	return err == nil && info.IsDir()
}
