package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sync"
)

// ErrRootNotAbsolute is returned when a class resolver is handed an empty or
// relative repository root. Resolving against such a root would place the
// directory relative to the process's working directory, whichever repository
// (or none) that happens to be, so the resolvers refuse it.
var ErrRootNotAbsolute = errors.New("layout: repository root must be an absolute path")

// ErrNotGitRepository is returned when a per-clone resolver is handed a root
// that is not inside a git repository. Per-clone data lives in the git
// directory (ADR-024 § 7); there is no fallback into the working tree or the
// process's working directory.
var ErrNotGitRepository = errors.New("not a git repository")

// ErrUnsafeCloneDir reports a per-clone root or class directory the resolvers
// refuse to hand to a writer: a symlink or a non-directory (ADR-024 § 17).
var ErrUnsafeCloneDir = errors.New("layout: unsafe per-clone directory")

// cloneDirName is the directory CLONE is inside the git common dir.
const cloneDirName = "nightgauge"

// Class names: the per-clone class directories under CLONE.
const (
	ClassPipeline = "pipeline"
	ClassPlans    = "plans"
	ClassRetros   = "retros"
	ClassLogs     = "logs"
)

// Classes lists every per-clone class, in the order `nightgauge layout`
// reports them.
var Classes = []string{ClassPipeline, ClassPlans, ClassRetros, ClassLogs}

// CloneDir returns CLONE = <git-common-dir>/nightgauge for the repository
// root is in (ADR-024 § 1, § 7). Every linked worktree of a clone resolves to
// the main clone's directory, so a run started in a worktree and inspected
// from the main checkout sees the same state. Git never tracks anything under
// its own directory, so nothing here can be committed.
//
// The git common dir comes from GitCommonDir: `git rev-parse
// --path-format=absolute --git-common-dir` run at root with the inherited GIT_*
// location variables cleared, cached once per process per root.
//
// CLONE is created on first resolution with mode 0700, or with git's group
// mode when the clone is group-shared (core.sharedRepository). A CLONE that is
// a symlink or not a directory is refused. When the git directory is not
// writable, CLONE is not created and the path is still returned: read-only
// commands report without writing, and a writer's own error names the path.
func CloneDir(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: resolving the per-clone directory from %q", ErrRootNotAbsolute, root)
	}
	root = filepath.Clean(root)
	common, err := GitCommonDir(root)
	if err != nil {
		return "", notGitRepository(root, err)
	}
	dir := filepath.Join(common, cloneDirName)
	if err := ensureCloneRoot(dir, common); err != nil {
		return "", err
	}
	return dir, nil
}

// notGitRepository wraps a failed git-common-dir lookup. A git that ran and
// refused is "not a git repository"; a git that could not run at all says so,
// because the fix differs.
func notGitRepository(root string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%w: %s (per-clone data lives in the git directory, ADR-024 § 7)",
			ErrNotGitRepository, root)
	}
	return fmt.Errorf("%w: %s: resolve the git directory: %v", ErrNotGitRepository, root, err)
}

// ensuredCloneRoots records the CLONE directories already created or verified
// in this process, so the check costs one Lstat per root per process.
var ensuredCloneRoots sync.Map

// ensureCloneRoot creates dir (CLONE) when absent and refuses it when it is a
// symlink or not a directory.
func ensureCloneRoot(dir, common string) error {
	if _, ok := ensuredCloneRoots.Load(dir); ok {
		return nil
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		mode := cloneDirMode(common)
		if mkErr := os.Mkdir(dir, mode.Perm()); mkErr != nil {
			if !errors.Is(mkErr, fs.ErrExist) {
				// An unwritable git directory: report nothing here. A
				// read-only command must still work, and a writer's
				// MkdirAll or open fails naming the path.
				return nil
			}
		} else {
			// Mkdir applies the umask; make the mode exact, including the
			// setgid bit a group-shared clone's directories carry.
			_ = os.Chmod(dir, mode)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("%w: stat %s: %v", ErrUnsafeCloneDir, dir, err)
	}
	if err := refuseUnsafeDir(dir, info); err != nil {
		return err
	}
	ensuredCloneRoots.Store(dir, struct{}{})
	return nil
}

// refuseUnsafeDir reports ErrUnsafeCloneDir for a symlink or a non-directory.
func refuseUnsafeDir(dir string, info fs.FileInfo) error {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%w: %s is a symlink; remove it", ErrUnsafeCloneDir, dir)
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a directory; remove it", ErrUnsafeCloneDir, dir)
	}
	return nil
}

// cloneDirMode is the mode CLONE is created with: 0700, unless the clone is
// group-shared. Git gives the directories of a repository with
// core.sharedRepository=group (or all) group write permission and the setgid
// bit, so a group-writable git common dir is read as group-shared and CLONE
// follows it: 0770 plus setgid (ADR-024 § 7). Reading the mode git already
// applied costs one stat and no git spawn.
func cloneDirMode(common string) fs.FileMode {
	if runtime.GOOS == "windows" {
		return 0o700
	}
	info, err := os.Stat(common)
	if err != nil || info.Mode().Perm()&0o020 == 0 {
		return 0o700
	}
	return 0o770 | fs.ModeSetgid
}

// PipelineStateDir is the directory of the pipeline class: per-issue stage
// contexts, runtime snapshots, run history and traces: CLONE/pipeline.
func PipelineStateDir(root string) (string, error) { return classDir(root, ClassPipeline) }

// PlansDir is the directory of issue-keyed implementation plans: CLONE/plans.
func PlansDir(root string) (string, error) { return classDir(root, ClassPlans) }

// RetrosDir is the directory of issue-keyed retrospectives: CLONE/retros.
func RetrosDir(root string) (string, error) { return classDir(root, ClassRetros) }

// CloneLogsDir is the directory of per-clone logs (for example
// autonomous-exits.jsonl and the dated GitHub API ledger): CLONE/logs.
func CloneLogsDir(root string) (string, error) { return classDir(root, ClassLogs) }

// ClassDir resolves one of Classes by name; an unknown class is an error.
func ClassDir(root, class string) (string, error) {
	for _, c := range Classes {
		if c == class {
			return classDir(root, class)
		}
	}
	return "", fmt.Errorf("layout: unknown per-clone class %q (want one of %v)", class, Classes)
}

// displayGitDir is how help text spells the git common dir: the main
// checkout's .git, which is what it is for every clone that is not bare or a
// separate-git-dir checkout. A linked worktree resolves to the same directory.
const displayGitDir = ".git"

// PipelineStateDisplay names the pipeline class directory for help and flag
// text, as seen from the main checkout: .git/nightgauge/pipeline. It moves
// with PipelineStateDir; TestCloneLayoutDisplay pins the two together.
// `nightgauge layout` prints the resolved absolute path.
func PipelineStateDisplay() string { return displayDir(ClassPipeline) }

// CloneLogsDisplay names the per-clone logs directory for help and flag text,
// the way PipelineStateDisplay names the pipeline class directory.
func CloneLogsDisplay() string { return displayDir(ClassLogs) }

func displayDir(class string) string { return path.Join(displayGitDir, cloneDirName, class) }

// classDir validates root and joins the class under CLONE. The error names
// the class so a caller's log says which resolution failed. A class directory
// that exists as a symlink or a non-directory is refused: a writer must never
// follow a link out of CLONE (ADR-024 § 17).
func classDir(root, class string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: resolving %s directory from %q", ErrRootNotAbsolute, class, root)
	}
	clone, err := CloneDir(root)
	if err != nil {
		return "", fmt.Errorf("resolving %s directory: %w", class, err)
	}
	dir := filepath.Join(clone, class)
	if info, err := os.Lstat(dir); err == nil {
		if err := refuseUnsafeDir(dir, info); err != nil {
			return "", err
		}
	}
	return dir, nil
}
