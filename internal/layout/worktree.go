package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ErrWorktreeBase reports a pipeline.worktree_base the resolver refuses: a
// relative value, a value in the committed team tier, or one that resolves
// inside the working tree (ADR-024 § 9). The error text names the file and
// line the value came from and the fix.
var ErrWorktreeBase = errors.New("invalid pipeline.worktree_base")

// ErrWorktreeEscape reports a worktree path that, after its existing
// components' symlinks are evaluated, is not a direct child of the worktree
// base. A crafted base, repository name or issue number never places a
// worktree anywhere else (ADR-024 § 9).
var ErrWorktreeEscape = errors.New("worktree path resolves outside the worktree base")

// WorktreeBaseKey is the dotted config key the setting is read from.
const WorktreeBaseKey = "pipeline.worktree_base"

// worktreesDirName is the STATE subdirectory the default bases live under.
const worktreesDirName = "worktrees"

// repoKeyLen is how many hex characters of the SHA-256 name a clone.
const repoKeyLen = 12

// WorktreeBaseSetting is pipeline.worktree_base as a machine- or local-tier
// config file declared it. The zero value means unset.
type WorktreeBaseSetting struct {
	// Value is the configured string exactly as written; "" when unset.
	Value string
	// Source is the file the value was read from.
	Source string
	// Line is the value's 1-based line in Source; 0 when unknown.
	Line int
}

// where renders "file:line" (or just the file) for an error message.
func (s WorktreeBaseSetting) where() string {
	switch {
	case s.Source == "":
		return "the config"
	case s.Line > 0:
		return fmt.Sprintf("%s:%d", s.Source, s.Line)
	default:
		return s.Source
	}
}

// WorktreeBase resolves the directory the pipeline creates the worktrees of
// the repository at repoRoot in (ADR-024 § 1, § 9). It does not create it.
//
//   - Unset: STATE/worktrees/<repo-key>, where <repo-key> is RepoKey of the
//     repository's canonical git common dir, so every linked worktree of one
//     clone shares one base and two clones never share one.
//   - Set: the configured absolute path, with a leading "~" expanded. A
//     relative value is refused, never reinterpreted against some root.
//
// Either way, a base that resolves (after symlink evaluation of its existing
// components) inside the working tree at repoRoot is refused: a worktree
// inside the tree is a second copy of the repository that search, watchers,
// linters and `git add -A` all traverse.
func WorktreeBase(repoRoot string, setting WorktreeBaseSetting) (string, error) {
	if repoRoot == "" || !filepath.IsAbs(repoRoot) {
		return "", fmt.Errorf("%w: resolving the worktree base from %q", ErrRootNotAbsolute, repoRoot)
	}
	repoRoot = filepath.Clean(repoRoot)

	var base, fix string
	if strings.TrimSpace(setting.Value) == "" {
		state, err := StateHomePath()
		if err != nil {
			return "", err
		}
		common, err := GitCommonDir(repoRoot)
		if err != nil {
			return "", fmt.Errorf("resolve the default worktree base for %s: %w", repoRoot, err)
		}
		base = filepath.Join(state, worktreesDirName, RepoKey(common))
		fix = fmt.Sprintf("set %s to an absolute directory outside the repository", EnvStateHome)
	} else {
		fix = fmt.Sprintf("delete the key, or set %s to an absolute path outside the repository "+
			"in the machine config or .nightgauge/config.local.yaml", WorktreeBaseKey)
		expanded, err := expandHome(strings.TrimSpace(setting.Value))
		if err != nil {
			return "", fmt.Errorf("%w: %s = %q in %s: %v; %s",
				ErrWorktreeBase, WorktreeBaseKey, setting.Value, setting.where(), err, fix)
		}
		if !filepath.IsAbs(expanded) {
			return "", fmt.Errorf("%w: %s = %q in %s is a relative path, which is no longer "+
				"read against the repository root; %s",
				ErrWorktreeBase, WorktreeBaseKey, setting.Value, setting.where(), fix)
		}
		base = filepath.Clean(expanded)
	}

	resolvedBase, err := EvalExisting(base)
	if err != nil {
		return "", fmt.Errorf("resolve worktree base %s: %w", base, err)
	}
	resolvedRoot, err := EvalExisting(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root %s: %w", repoRoot, err)
	}
	if within(resolvedRoot, resolvedBase) {
		what := fmt.Sprintf("the default worktree base %s", base)
		if setting.Value != "" {
			what = fmt.Sprintf("%s = %q in %s", WorktreeBaseKey, setting.Value, setting.where())
		}
		return "", fmt.Errorf("%w: %s resolves inside the working tree %s; %s",
			ErrWorktreeBase, what, repoRoot, fix)
	}
	return base, nil
}

// worktreeRepoNameRE is the shape a repository name must have to become part
// of a worktree directory name: one path component, no separators.
var worktreeRepoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// WorktreeDirName is the directory name of a pipeline worktree,
// <repo>-issue-<N> (ADR-024 § 9). repo may be "owner/name"; only the name is
// used. The repo-name prefix keeps two repositories' issue #N apart.
func WorktreeDirName(repo string, issueNumber int) (string, error) {
	name := repo
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if !worktreeRepoNameRE.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: repository name %q cannot name a worktree directory", ErrWorktreeEscape, repo)
	}
	if issueNumber <= 0 {
		return "", fmt.Errorf("%w: issue number %d is not positive", ErrWorktreeEscape, issueNumber)
	}
	return fmt.Sprintf("%s-issue-%d", name, issueNumber), nil
}

// WorktreePath returns base/<repo>-issue-<N> after proving it stays inside
// base: the parents' and any existing leaf's symlinks are evaluated, and the
// result must be exactly the leaf directly under the evaluated base. A leaf
// that already exists as a symlink to anywhere else is refused.
func WorktreePath(base, repo string, issueNumber int) (string, error) {
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("%w: worktree base %q is not absolute", ErrWorktreeEscape, base)
	}
	leaf, err := WorktreeDirName(repo, issueNumber)
	if err != nil {
		return "", err
	}
	base = filepath.Clean(base)
	path := filepath.Join(base, leaf)
	if err := CheckWorktreeContainment(base, path); err != nil {
		return "", err
	}
	return path, nil
}

// CheckWorktreeContainment reports ErrWorktreeEscape unless path, after
// symlink evaluation of every existing component, is a direct child of base
// after the same evaluation.
func CheckWorktreeContainment(base, path string) error {
	resolvedBase, err := EvalExisting(base)
	if err != nil {
		return fmt.Errorf("%w: resolve %s: %v", ErrWorktreeEscape, base, err)
	}
	resolvedPath, err := EvalExisting(path)
	if err != nil {
		return fmt.Errorf("%w: resolve %s: %v", ErrWorktreeEscape, path, err)
	}
	rel, err := filepath.Rel(resolvedBase, resolvedPath)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.ContainsRune(rel, filepath.Separator) || rel != filepath.Base(path) {
		return fmt.Errorf("%w: %s resolves to %s, which is not directly inside %s",
			ErrWorktreeEscape, path, resolvedPath, resolvedBase)
	}
	return nil
}

// EnsureWorktreeBase creates base (and a missing STATE root above it) with
// mode 0700. Worktrees hold source code and must not be readable by other
// local users.
func EnsureWorktreeBase(base string) error {
	if base == "" || !filepath.IsAbs(base) {
		return fmt.Errorf("%w: worktree base %q is not absolute", ErrWorktreeEscape, base)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create worktree base %s: %w", base, err)
	}
	return nil
}

// RepoKey is the first 12 hex characters of the SHA-256 of a canonical git
// common dir: the name of one clone's per-machine directories (ADR-024 § 1).
func RepoKey(commonDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(commonDir)))
	return hex.EncodeToString(sum[:])[:repoKeyLen]
}

// GitCommonDir returns the canonical git common dir of the repository dir is
// in: absolute, cleaned and symlink-evaluated, so every linked worktree of a
// clone and every spelling of its path give the same answer. Inherited GIT_*
// location variables are cleared first: a GIT_DIR set by a hook would
// otherwise answer for a different repository.
//
// A successful answer is cached per dir for the life of the process: a
// directory does not change which clone it belongs to, and the lookup runs on
// every worktree-path resolution.
func GitCommonDir(dir string) (string, error) {
	if v, ok := gitCommonDirCache.Load(dir); ok {
		return v.(string), nil
	}
	common, err := gitCommonDir(dir)
	if err != nil {
		return "", err
	}
	gitCommonDirCache.Store(dir, common)
	return common, nil
}

var gitCommonDirCache sync.Map

func gitCommonDir(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	cmd.Env = withoutGitLocationEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir in %s: %w", dir, err)
	}
	common := strings.TrimSpace(string(out))
	if common == "" || !filepath.IsAbs(common) {
		return "", fmt.Errorf("git rev-parse --git-common-dir in %s returned %q", dir, common)
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	return filepath.Clean(common), nil
}

// gitLocationEnv are the variables that redirect which repository git reads.
var gitLocationEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"}

func withoutGitLocationEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range gitLocationEnv {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// EvalExisting evaluates the symlinks of the longest existing prefix of path
// and appends the rest unchanged, so a path whose tail does not exist yet (a
// worktree about to be created) still resolves through its parents' links
// (macOS /var -> /private/var is the routine case).
func EvalExisting(path string) (string, error) {
	path = filepath.Clean(path)
	var rest []string
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(rest) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, rest[i])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path, nil
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// within reports whether child is parent or lies under it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// expandHome expands a leading "~" or "~/" to the user's home directory.
// "~user" is not supported and is returned as an error.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		if strings.HasPrefix(p, "~") {
			return "", errors.New("only a leading ~ or ~/ is expanded")
		}
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("the home directory is not set, so ~ cannot be expanded")
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
