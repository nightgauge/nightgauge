package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The file tools serve what every participant of the conversation could
// read. A reply goes to the whole conversation, and the turn command names
// none of its participants, so every turn, the host operator's own included,
// is served only the files git tracks in the checkout of a repository the
// turn command lists as readable: the rule the run tools follow (#2472). An
// untracked or ignored file (a local .env, a credential, .nightgauge runtime
// state, a nested clone of another repository) is the operator's alone, and
// no turn is shown it, names it in a listing or matches it in a search. A
// file the repository's own ignore rules exclude is not served even when it
// is tracked: someone force-added it, and under .nightgauge/, whose rules
// ignore everything but the team's committed files, it is runtime state.

// trackedListTimeout bounds one checkout's git ls-files.
const trackedListTimeout = 30 * time.Second

var (
	// errNotServed is every refusal that could otherwise say something about
	// what is on the machine: a missing path, an untracked or ignored one, a
	// path in a repository the turn may not read or in no repository, and
	// anything a symlink leads to that is not served.
	errNotServed = fail(CodeNotFound, "no file this conversation may read is at that path: the file "+
		"tools serve only the files git tracks in a repository the turn may read")
	errUntrackable = fail(CodeNotFound, "git could not list that repository's tracked files on this "+
		"host, so none of its files is served")
)

// trackedFiles is one checkout's served paths: the files git's index lists
// that the checkout's ignore rules do not exclude, slash-separated and
// relative to the checkout, and every directory holding one. err is set when
// git could not list them: then none is served.
type trackedFiles struct {
	files map[string]bool
	dirs  map[string]bool
	err   error
}

// trackedLister lists one checkout's served files: a variable so a test can
// see which checkouts are listed, and when.
var trackedLister = listTracked

// listTracked lists the files git tracks in the checkout at dir, less those
// its ignore rules exclude. It reads the index and the ignore files and
// nothing else: no path is passed to git, ls-files neither refreshes the
// index nor runs a hook, and the variables that would point git at another
// repository are dropped.
func listTracked(dir string) *trackedFiles {
	ctx, cancel := context.WithTimeout(context.Background(), trackedListTimeout)
	defer cancel()
	t := &trackedFiles{files: map[string]bool{}, dirs: map[string]bool{}}
	cached, err := lsFiles(ctx, dir, "--cached")
	if err != nil {
		t.err = err
		return t
	}
	ignored, err := lsFiles(ctx, dir, "--cached", "--ignored", "--exclude-standard")
	if err != nil {
		t.err = err
		return t
	}
	excluded := map[string]bool{}
	for _, p := range ignored {
		excluded[p] = true
	}
	for _, p := range cached {
		if excluded[p] {
			continue
		}
		t.files[p] = true
		for d := path.Dir(p); d != "." && !t.dirs[d]; d = path.Dir(d) {
			t.dirs[d] = true
		}
	}
	return t
}

func lsFiles(ctx context.Context, dir string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"ls-files", "-z"}, args...)...)
	cmd.Dir = dir
	cmd.Env = withoutGitLocation(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

// gitLocationVars point git at a repository other than the one its working
// directory is in.
var gitLocationVars = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_INDEX_FILE=",
	"GIT_OBJECT_DIRECTORY="}

func withoutGitLocation(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		keep := true
		for _, v := range gitLocationVars {
			if strings.HasPrefix(kv, v) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// listReadable lists, once per turn and before the first path is judged,
// the served files of every checkout of a readable repository that a path
// under the root can lie in. git therefore never runs because of what a path
// names, so how long a refusal takes says nothing about its target, and it
// never runs in a repository the turn may not read.
func (s *Toolset) listReadable() {
	s.listOnce.Do(func() {
		if s.fs == nil {
			return
		}
		root, sep := s.fs.dir, string(filepath.Separator)
		for slug, dir := range s.checkouts {
			owner, repo, _ := strings.Cut(slug, "/")
			if !s.canReadRepo(owner, repo) {
				continue
			}
			if dir == root || strings.HasPrefix(dir, root+sep) || strings.HasPrefix(root, dir+sep) {
				s.tracked[slug] = trackedLister(dir)
			}
		}
	})
}

// checkoutOf returns the innermost checkout that holds abs, an absolute
// path under the symlink-resolved root.
func (s *Toolset) checkoutOf(abs string) (slug, dir string) {
	best := -1
	for sl, d := range s.checkouts {
		if (abs == d || strings.HasPrefix(abs, d+string(filepath.Separator))) && len(d) > best {
			slug, dir, best = sl, d, len(d)
		}
	}
	return slug, dir
}

// leadsToReadable reports whether abs is a directory on the way to the
// checkout of a repository the turn may read, so a listing or a search
// can reach it.
func (s *Toolset) leadsToReadable(abs string) bool {
	for slug, d := range s.checkouts {
		owner, repo, _ := strings.Cut(slug, "/")
		if strings.HasPrefix(d, abs+string(filepath.Separator)) && s.canReadRepo(owner, repo) {
			return true
		}
	}
	return false
}

// served reports whether the turn may be shown the path at abs, judged by
// its name alone, never by what is on disk: the innermost checkout that
// holds it must be a repository the turn command lists as readable, and the
// path must be that checkout's root, a served file there, a directory
// holding one, or a directory on the way to another readable checkout. A
// path in a repository the turn may not read, or in none, is refused as a
// missing one is: a refusal of its own would show where that repository's
// clone is.
func (s *Toolset) served(abs string) *Error {
	s.listReadable()
	slug, dir := s.checkoutOf(abs)
	if slug == "" {
		if s.leadsToReadable(abs) {
			return nil
		}
		return errNotServed
	}
	owner, repo, _ := strings.Cut(slug, "/")
	if !s.canReadRepo(owner, repo) {
		return errNotServed
	}
	if abs == dir || s.leadsToReadable(abs) {
		return nil
	}
	t := s.tracked[slug]
	if t == nil {
		return errNotServed
	}
	if t.err != nil {
		return errUntrackable
	}
	rel := filepath.ToSlash(strings.TrimPrefix(abs, dir+string(filepath.Separator)))
	if t.files[rel] || t.dirs[rel] {
		return nil
	}
	return errNotServed
}

// locate resolves a file tool's path and checks that the turn may be served
// it. The path is judged as named first (outside the root, secret-bearing,
// or not a served file of a readable repository) before anything on disk is
// consulted, so each of those refusals depends on the name alone. Then its
// symlinks are followed and where they lead is judged the same way; any
// refusal there is errNotServed, the refusal a missing file gets, because a
// target's own refusal would say that it exists.
func (s *Toolset) locate(p string) (*workspaceFS, string, *Error) {
	w, err := s.workspace()
	if err != nil {
		return nil, "", err
	}
	named, err := w.lexical(p)
	if err != nil {
		return nil, "", err
	}
	if err := s.served(w.abs(named)); err != nil {
		return nil, "", err
	}
	rel, err := w.resolve(named)
	if err != nil {
		return nil, "", errNotServed
	}
	if rel != named && s.served(w.abs(rel)) != nil {
		return nil, "", errNotServed
	}
	return w, rel, nil
}
